// Package ledger écrit les opérations dans le grand livre Postgres et publie l'outbox.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
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

// Write enregistre l'opération, ses deux écritures comptables et la ligne d'outbox dans UNE
// transaction SQL. La clé primaire transaction_id rend l'écriture idempotente : rejouer le même
// message (doublon réseau, reprise après panne) ne change rien. Un transaction_id déjà vu avec
// un contenu différent est un conflit : *tx.Rejection, le message part en DLQ.
func (s Store) Write(ctx context.Context, t tx.Transaction, src Source) (Result, error) {
	payload, err := json.Marshal(t)
	if err != nil {
		return 0, err
	}
	sum := src.Digest
	debit, credit := t.Accounts()

	var res Result
	err = pgx.BeginFunc(ctx, s.Pool, func(q pgx.Tx) error {
		tag, err := q.Exec(ctx, `
			INSERT INTO transactions (transaction_id, booked_at, payment_type, debtor_account, creditor_account,
			                          amount_minor, currency, payload_sha256, source_partition, source_offset)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (transaction_id) DO NOTHING`,
			t.TransactionID, t.BookedAt, t.PaymentType, debit, credit, t.AmountMinor, t.Currency,
			sum[:], src.Partition, src.Offset)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var stored []byte
			if err := q.QueryRow(ctx, `SELECT payload_sha256 FROM transactions WHERE transaction_id = $1`,
				t.TransactionID).Scan(&stored); err != nil {
				return err
			}
			if !bytes.Equal(stored, sum[:]) {
				return &tx.Rejection{Reason: tx.ReasonIdempotencyConf,
					Detail: fmt.Sprintf("transaction_id %s déjà enregistré avec un autre contenu", t.TransactionID)}
			}
			res = Duplicate
			return nil
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO ledger_entries (transaction_id, account, currency, amount_minor)
			VALUES ($1, $2, $4, -$5::bigint), ($1, $3, $4, $5::bigint)`,
			t.TransactionID, debit, credit, t.Currency, t.AmountMinor); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO outbox (topic, message_key, payload) VALUES ($1, $2, $3)`,
			ValidTopic, debit, payload)
		res = Written
		return err
	})
	return res, err
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
