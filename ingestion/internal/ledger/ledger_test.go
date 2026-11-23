package ledger

// Tests d'intégration sur un vrai Postgres. Ils tournent si TEST_DATABASE_URL pointe vers une
// base VIDE où l'on peut créer des tables (la CI fournit un conteneur Postgres neuf) :
//   TEST_DATABASE_URL=postgres://postgres:postgres@localhost:15434/test go test ./...

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
)

func openDB(t *testing.T) Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL absent : tests d'intégration Postgres ignorés")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('transactions') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		ddl, err := os.ReadFile("../../../db/001_grand_livre.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(ddl)); err != nil {
			t.Fatal(err)
		}
	}
	return Store{Pool: pool}
}

var seq int

// src fabrique la Source d'un message, empreinte calculée comme le fait l'ingestion.
func src(tr tx.Transaction) Source {
	b, _ := json.Marshal(tr)
	return Source{Digest: sha256.Sum256(b)}
}

func newTx(t *testing.T) tx.Transaction {
	seq++
	return tx.Transaction{
		TransactionID: fmt.Sprintf("%s-%d-%d", t.Name(), time.Now().UnixNano(), seq),
		BookedAt:      time.Now().UTC().Truncate(time.Microsecond),
		PaymentType:   "transfer",
		DebtorIBAN:    tx.MakeIBAN("FR", "30001000010000000000100"),
		CreditorIBAN:  tx.MakeIBAN("FR", "30001000010000000000200"),
		AmountMinor:   123456,
		Currency:      "EUR",
	}
}

func count(t *testing.T, s Store, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.Pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEcritureEtDoublon(t *testing.T) {
	s, ctx := openDB(t), context.Background()
	tr := newTx(t)
	for i := range 3 {
		res, err := s.Write(ctx, tr, src(tr))
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]Result{true: Written, false: Duplicate}[i == 0]; res != want {
			t.Fatalf("essai %d : résultat %v, attendu %v", i, res, want)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM ledger_entries WHERE transaction_id = $1`, tr.TransactionID); n != 2 {
		t.Fatalf("%d écritures, attendu 2", n)
	}
	if n := count(t, s, `SELECT count(*) FROM outbox WHERE payload->>'transaction_id' = $1`, tr.TransactionID); n != 1 {
		t.Fatalf("%d lignes d'outbox, attendu 1", n)
	}
	if n := count(t, s, `SELECT coalesce(sum(ecart_minor), 0) FROM v_reconciliation`); n != 0 {
		t.Fatalf("écart de réconciliation %d", n)
	}
}

// 50 copies du même message écrites en même temps : une seule doit compter.
func TestDoublonsConcurrents(t *testing.T) {
	s, ctx := openDB(t), context.Background()
	tr := newTx(t)
	var wg sync.WaitGroup
	results := make(chan Result, 50)
	for range 50 {
		wg.Go(func() {
			res, err := s.Write(ctx, tr, src(tr))
			if err != nil {
				t.Error(err)
			}
			results <- res
		})
	}
	wg.Wait()
	close(results)
	written := 0
	for r := range results {
		if r == Written {
			written++
		}
	}
	if written != 1 {
		t.Fatalf("%d écritures retenues sur 50 copies, attendu 1", written)
	}
	if n := count(t, s, `SELECT count(*) FROM ledger_entries WHERE transaction_id = $1`, tr.TransactionID); n != 2 {
		t.Fatalf("%d écritures comptables, attendu 2", n)
	}
}

func TestConflitIdempotence(t *testing.T) {
	s, ctx := openDB(t), context.Background()
	tr := newTx(t)
	if _, err := s.Write(ctx, tr, src(tr)); err != nil {
		t.Fatal(err)
	}
	tr.AmountMinor++ // même identifiant, autre montant
	_, err := s.Write(ctx, tr, src(tr))
	if r, ok := IsRejection(err); !ok || r.Reason != tx.ReasonIdempotencyConf {
		t.Fatalf("attendu un conflit d'idempotence, obtenu %v", err)
	}
}

func TestAjoutSeul(t *testing.T) {
	s, ctx := openDB(t), context.Background()
	tr := newTx(t)
	if _, err := s.Write(ctx, tr, src(tr)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE ledger_entries SET amount_minor = 1 WHERE transaction_id = $1`,
		`DELETE FROM ledger_entries WHERE transaction_id = $1`,
		`UPDATE transactions SET amount_minor = 1 WHERE transaction_id = $1`,
		`DELETE FROM transactions WHERE transaction_id = $1`,
	} {
		if _, err := s.Pool.Exec(ctx, q, tr.TransactionID); err == nil {
			t.Errorf("accepté alors que le grand livre est en ajout seul : %s", q)
		}
	}
}

// Chaque cas insère une opération de 100 € de A vers B, puis des écritures fautives : le COMMIT doit échouer.
func TestEquilibreVerifieAuCommit(t *testing.T) {
	s, ctx := openDB(t), context.Background()
	cas := map[string]string{
		"montants déséquilibrés":     `($1, 'A', 'EUR', -100), ($1, 'B', 'EUR', 99)`,
		"sans aucune écriture":       ``,
		"une seule jambe":            `($1, 'A', 'EUR', -100)`,
		"deux devises":               `($1, 'A', 'EUR', -100), ($1, 'B', 'USD', 100)`,
		"montant différent de l'op.": `($1, 'A', 'EUR', -5), ($1, 'B', 'EUR', 5)`,
		"autres comptes":             `($1, 'C', 'EUR', -100), ($1, 'D', 'EUR', 100)`,
		"sens inversé":               `($1, 'A', 'EUR', 100), ($1, 'B', 'EUR', -100)`,
		"trois jambes":               `($1, 'A', 'EUR', -100), ($1, 'B', 'EUR', 100), ($1, 'B', 'EUR', 1)`,
	}
	for nom, ecritures := range cas {
		id := newTx(t).TransactionID
		q, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Exec(ctx, `INSERT INTO transactions (transaction_id, booked_at, payment_type, debtor_account,
			creditor_account, amount_minor, currency, payload_sha256, source_partition, source_offset)
			VALUES ($1, now(), 'transfer', 'A', 'B', 100, 'EUR', '\x00', 0, 0)`, id); err != nil {
			t.Fatal(err)
		}
		if ecritures != "" {
			if _, err := q.Exec(ctx, `INSERT INTO ledger_entries (transaction_id, account, currency, amount_minor) VALUES `+ecritures, id); err != nil {
				t.Fatalf("%s : %v", nom, err)
			}
		}
		if err := q.Commit(ctx); err == nil {
			t.Errorf("%s : accepté au commit", nom)
		}
	}
}

func TestOutboxPublieeUneFois(t *testing.T) {
	s, ctx := openDB(t), context.Background()
	tr := newTx(t)
	if _, err := s.Write(ctx, tr, src(tr)); err != nil {
		t.Fatal(err)
	}
	// Échec de publication : rien n'est marqué, tout reste à publier.
	_, err := s.PublishOutbox(ctx, 10_000, func([]OutboxMessage) error { return errors.New("Kafka injoignable") })
	if err == nil {
		t.Fatal("échec de publication avalé")
	}
	pending := count(t, s, `SELECT count(*) FROM outbox WHERE published_at IS NULL`)
	if pending == 0 {
		t.Fatal("lignes marquées publiées malgré l'échec")
	}
	var got int
	for {
		n, err := s.PublishOutbox(ctx, 10_000, func(m []OutboxMessage) error { got += len(m); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if int64(got) != pending || count(t, s, `SELECT count(*) FROM outbox WHERE published_at IS NULL`) != 0 {
		t.Fatalf("publiées %d, attendu %d", got, pending)
	}
}
