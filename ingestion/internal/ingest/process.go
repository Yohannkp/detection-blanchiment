// Package ingest décide du sort de chaque message de transactions.raw :
// grand livre, doublon absorbé, ou DLQ. Il ne connaît Kafka et Postgres que par des interfaces.
package ingest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

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
	Write(ctx context.Context, t tx.Transaction, src ledger.Source) (ledger.Result, error)
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

// Process traite un message. Il ne renvoie une erreur QUE pour une panne (base, registre, DLQ
// injoignables) : l'appelant doit alors réessayer le même message sans avancer l'offset.
// Un message invalide n'est pas une erreur : il est dérouté en DLQ et la lecture continue.
func (p Processor) Process(ctx context.Context, m Message) error {
	t, digest, rej, err := p.decode(m)
	if err != nil {
		return err
	}
	if rej == nil {
		var res ledger.Result
		res, err = p.Store.Write(ctx, t, ledger.Source{Partition: m.Partition, Offset: m.Offset, Digest: digest})
		if err == nil {
			if res == ledger.Duplicate {
				p.Metrics.Add("ingestion_doublons_absorbes_total", 1)
			} else {
				p.Metrics.Add("ingestion_ecrites_total", 1)
			}
			return nil
		}
		if rej = asRejection(err); rej == nil {
			return fmt.Errorf("écriture au grand livre : %w", err)
		}
	}
	if err := p.DLQ.Send(ctx, m, rej); err != nil {
		return fmt.Errorf("envoi en DLQ : %w", err)
	}
	p.Metrics.Add(fmt.Sprintf(`ingestion_dlq_total{reason=%q}`, rej.Reason), 1)
	return nil
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

// asRejection range parmi les rejets les erreurs de contenu : conflit d'idempotence, et les
// refus de Postgres sur les données (classes SQLSTATE 22 et 23), qu'aucun nouvel essai ne corrigerait.
func asRejection(err error) *tx.Rejection {
	if r, ok := ledger.IsRejection(err); ok {
		return r
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "23")) {
		return &tx.Rejection{Reason: "refus_base", Detail: pg.Code + " " + pg.Message}
	}
	return nil
}
