// Package ingest décide du sort de chaque message de transactions.raw :
// grand livre, doublon absorbé, ou DLQ. Il ne connaît Kafka et Postgres que par des interfaces.
package ingest

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/ledger"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/metrics"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

// Message est un enregistrement Kafka lu sur transactions.raw.
type Message struct {
	Value     []byte
	Partition int32
	Offset    int64
}

type Store interface {
	WriteBatch(ctx context.Context, items []ledger.Item) ([]ledger.Issue, error)
}

type Schemas interface {
	Known(id int) (bool, error)
}

// DeadLetters envoie le message d'origine, intact, vers transactions.dlq avec la raison du rejet.
type DeadLetters interface {
	Send(ctx context.Context, m Message, r *tx.Rejection) error
}

type Processor struct {
	Store   Store
	Schemas Schemas
	DLQ     DeadLetters
	Metrics *metrics.Counters
}

// ProcessBatch traite un lot de messages : les valides sont écrits au grand livre en UNE transaction,
// les invalides (et ceux que la base refuse) partent en DLQ. Il ne renvoie une erreur QUE pour une panne
// (base, registre, DLQ injoignables) : l'appelant réessaie alors le lot entier sans avancer les offsets,
// sans effet grâce à l'idempotence (seule la DLQ peut recevoir un message deux fois, cf. README).
func (p Processor) ProcessBatch(ctx context.Context, msgs []Message) error {
	var items []ledger.Item
	var sources []Message
	refus := map[int]*tx.Rejection{} // indice dans msgs → raison
	for i, m := range msgs {
		t, digest, rej, err := p.decode(m)
		if err != nil {
			return err
		}
		if rej != nil {
			refus[i] = rej
			continue
		}
		items = append(items, ledger.Item{T: t, Src: ledger.Source{Partition: m.Partition, Offset: m.Offset, Digest: digest}})
		sources = append(sources, m)
	}
	envoyer := func(m Message, r *tx.Rejection) error {
		if err := p.DLQ.Send(ctx, m, r); err != nil {
			return fmt.Errorf("envoi en DLQ : %w", err)
		}
		return nil
	}
	// Refus de décodage envoyés AVANT l'écriture : si la DLQ tombe, rien n'est encore commité. Reste un cas :
	// la DLQ tombe juste après le COMMIT pour un refus de la base ; le lot réessayé revient alors en doublons
	// (aucune perte, mais compteurs écrites/doublons faussés pour ce lot).
	for i, m := range msgs {
		if r, ok := refus[i]; ok {
			if err := envoyer(m, r); err != nil {
				return err
			}
		}
	}
	var issues []ledger.Issue
	if len(items) > 0 {
		var err error
		if issues, err = p.Store.WriteBatch(ctx, items); err != nil {
			return fmt.Errorf("écriture au grand livre : %w", err)
		}
	}
	var ecrites, doublons int64
	for i, is := range issues {
		switch {
		case is.Refus != nil:
			if err := envoyer(sources[i], is.Refus); err != nil {
				return err
			}
		case is.Result == ledger.Duplicate:
			doublons++
		default:
			ecrites++
		}
	}
	// Compteurs mis à jour seulement quand tout le lot a réussi (un lot réessayé ne compte pas deux fois).
	p.Metrics.Add("ingestion_ecrites_total", ecrites)
	p.Metrics.Add("ingestion_doublons_absorbes_total", doublons)
	for _, is := range issues {
		if is.Refus != nil {
			p.Metrics.Add(fmt.Sprintf(`ingestion_dlq_total{reason=%q}`, is.Refus.Reason), 1)
		}
	}
	for _, r := range refus {
		p.Metrics.Add(fmt.Sprintf(`ingestion_dlq_total{reason=%q}`, r.Reason), 1)
	}
	return nil
}

// Process traite un seul message (lot de taille 1).
func (p Processor) Process(ctx context.Context, m Message) error {
	return p.ProcessBatch(ctx, []Message{m})
}

func (p Processor) decode(m Message) (t tx.Transaction, digest [32]byte, rej *tx.Rejection, err error) {
	id, payload, err := wire.Decode(m.Value)
	if err != nil {
		return t, digest, &tx.Rejection{Reason: tx.ReasonSchema, Detail: err.Error()}, nil
	}
	known, err := p.Schemas.Known(id)
	if err != nil {
		return t, digest, nil, err
	}
	if !known {
		return t, digest, &tx.Rejection{Reason: tx.ReasonSchema,
			Detail: fmt.Sprintf("identifiant de schéma %d absent du registre", id)}, nil
	}
	if t, err = tx.Parse(payload); err != nil {
		rej, _ = ledger.IsRejection(err)
		return t, digest, rej, nil
	}
	return t, sha256.Sum256(payload), nil, nil
}
