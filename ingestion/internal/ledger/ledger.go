// Package ledger écrit les opérations dans le grand livre Postgres et publie l'outbox.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
)

// ValidTopic reçoit, via l'outbox, chaque opération enregistrée au grand livre.
const ValidTopic = "transactions.valid"

// Result dit ce que l'écriture a produit.
type Result int

const (
	Written   Result = iota // nouvelle opération enregistrée
	Duplicate               // même transaction_id, même contenu : absorbée sans effet
)

type Store struct{ Pool *pgxpool.Pool }

// Source situe le message d'origine dans Kafka (traçabilité de l'audit) et porte l'empreinte
// SHA-256 de ses octets bruts : un renvoi réseau est identique octet pour octet, alors que deux
// messages qui ne diffèrent que par un champ ignoré au décodage ne le sont pas (→ conflit).
type Source struct {
	Partition int32
	Offset    int64
	Digest    [32]byte
}

// Item est une opération à écrire et la position de son message d'origine.
type Item struct {
	T   tx.Transaction
	Src Source
}

// Issue dit ce que l'écriture d'un Item a produit : Written, Duplicate, ou un refus (message pour la DLQ).
type Issue struct {
	Result Result
	Refus  *tx.Rejection
}

// Une seule instruction par opération : l'opération (clé primaire = idempotence), ses deux écritures
// comptables et sa ligne d'outbox. Si l'opération existe déjà, rien n'est inséré (0 ligne renvoyée).
const ecrireOperation = `
WITH op AS (
  INSERT INTO transactions (transaction_id, booked_at, payment_type, debtor_account, creditor_account,
                            amount_minor, currency, payment_format, payload_sha256, source_partition, source_offset)
  VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10, $11)
  ON CONFLICT (transaction_id) DO NOTHING
  RETURNING transaction_id),
ecritures AS (
  INSERT INTO ledger_entries (transaction_id, account, currency, amount_minor)
  SELECT transaction_id, $4, $7, -$6::bigint FROM op
  UNION ALL SELECT transaction_id, $5, $7, $6::bigint FROM op),
annonce AS (
  INSERT INTO outbox (topic, message_key, payload) SELECT $12, $4, $13 FROM op)
SELECT count(*) FROM op`

// WriteBatch écrit un lot d'opérations dans UNE transaction SQL (un seul COMMIT, donc une seule écriture
// disque synchronisée pour tout le lot, au lieu d'une par opération). Chaque opération a son point de
// sauvegarde : un refus des données (équilibre, contrainte) n'annule qu'elle, et devient un refus pour la DLQ.
// Le contrôle d'équilibre, normalement différé au COMMIT, est avancé à la fin de chaque opération
// (SET CONSTRAINTS … IMMEDIATE) : une opération fautive ne peut pas faire échouer tout le lot.
// Toute autre erreur (base injoignable…) est renvoyée telle quelle : l'appelant réessaie le lot entier,
// sans effet grâce à l'idempotence.
func (s Store) WriteBatch(ctx context.Context, items []Item) ([]Issue, error) {
	issues := make([]Issue, len(items))
	err := pgx.BeginFunc(ctx, s.Pool, func(q pgx.Tx) error {
		for i, it := range items {
			payload, err := json.Marshal(it.T)
			if err != nil {
				return err
			}
			debit, credit := it.T.Accounts()
			b := &pgx.Batch{}
			b.Queue("SAVEPOINT operation")
			var inserees int64
			b.Queue(ecrireOperation, it.T.TransactionID, it.T.BookedAt, it.T.PaymentType, debit, credit, it.T.AmountMinor,
				it.T.Currency, it.T.PaymentFormat, it.Src.Digest[:], it.Src.Partition, it.Src.Offset, ValidTopic, payload).
				QueryRow(func(r pgx.Row) error { return r.Scan(&inserees) })
			b.Queue("SET CONSTRAINTS ALL IMMEDIATE")
			b.Queue("SET CONSTRAINTS ALL DEFERRED")
			b.Queue("RELEASE SAVEPOINT operation")
			if err := q.SendBatch(ctx, b).Close(); err != nil {
				refus := RefusDesDonnees(err)
				if refus == nil {
					return err
				}
				if _, err := q.Exec(ctx, "ROLLBACK TO SAVEPOINT operation"); err != nil {
					return err
				}
				issues[i] = Issue{Refus: refus}
				continue
			}
			if inserees == 1 {
				issues[i] = Issue{Result: Written}
				continue
			}
			// Déjà enregistrée (doublon réseau, relecture après panne… ou même identifiant, autre contenu).
			var stored []byte
			if err := q.QueryRow(ctx, `SELECT payload_sha256 FROM transactions WHERE transaction_id = $1`,
				it.T.TransactionID).Scan(&stored); err != nil {
				return err
			}
			if !bytes.Equal(stored, it.Src.Digest[:]) {
				issues[i] = Issue{Refus: &tx.Rejection{Reason: tx.ReasonIdempotencyConf,
					Detail: fmt.Sprintf("transaction_id %s déjà enregistré avec un autre contenu", it.T.TransactionID)}}
				continue
			}
			issues[i] = Issue{Result: Duplicate}
		}
		return nil
	})
	return issues, err
}

// Write enregistre une seule opération (lot de taille 1). Un refus est renvoyé comme *tx.Rejection.
func (s Store) Write(ctx context.Context, t tx.Transaction, src Source) (Result, error) {
	issues, err := s.WriteBatch(ctx, []Item{{T: t, Src: src}})
	if err != nil {
		return 0, err
	}
	if issues[0].Refus != nil {
		return 0, issues[0].Refus
	}
	return issues[0].Result, nil
}

// RefusDesDonnees reconnaît une erreur de contenu : refus de Postgres sur les données (classes SQLSTATE 22
// et 23 : contrainte, équilibre, valeur invalide), qu'aucun nouvel essai ne corrigerait. nil sinon.
func RefusDesDonnees(err error) *tx.Rejection {
	if r, ok := IsRejection(err); ok {
		return r
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "23")) {
		return &tx.Rejection{Reason: "refus_base", Detail: pg.Code + " " + pg.Message}
	}
	return nil
}

// OutboxMessage est une ligne d'outbox à publier.
type OutboxMessage struct {
	ID      int64
	Topic   string
	Key     string
	Payload []byte
}

// PublishOutbox prend jusqu'à limit lignes non publiées, les passe à publish (qui doit
// réussir pour TOUTES), puis les marque publiées. Si publish échoue, rien n'est marqué : les
// lignes seront republiées (au moins une fois). FOR UPDATE SKIP LOCKED permet plusieurs relais.
func (s Store) PublishOutbox(ctx context.Context, limit int, publish func([]OutboxMessage) error) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(q pgx.Tx) error {
		rows, err := q.Query(ctx, `
			SELECT outbox_id, topic, message_key, payload::text FROM outbox
			 WHERE published_at IS NULL ORDER BY outbox_id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		msgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (OutboxMessage, error) {
			var m OutboxMessage
			var p string
			err := r.Scan(&m.ID, &m.Topic, &m.Key, &p)
			m.Payload = []byte(p)
			return m, err
		})
		if err != nil || len(msgs) == 0 {
			return err
		}
		if err := publish(msgs); err != nil {
			return fmt.Errorf("publication de l'outbox : %w", err)
		}
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		n = len(msgs)
		_, err = q.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE outbox_id = ANY($1)`, ids)
		return err
	})
	return n, err
}

// IsRejection dit si err est une erreur de contenu (DLQ) plutôt qu'une panne (réessayer).
func IsRejection(err error) (*tx.Rejection, bool) {
	var r *tx.Rejection
	ok := errors.As(err, &r)
	return r, ok
}
