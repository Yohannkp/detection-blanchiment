package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/ledger"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/metrics"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

type fakeStore struct {
	seen map[string]bool
	err  error
}

func (s *fakeStore) Write(_ context.Context, t tx.Transaction, _ ledger.Source) (ledger.Result, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.seen[t.TransactionID] {
		return ledger.Duplicate, nil
	}
	s.seen[t.TransactionID] = true
	return ledger.Written, nil
}

type fakeSchemas struct{ err error }

func (f fakeSchemas) Known(id int) (bool, error) { return id == 1, f.err }

type fakeDLQ struct {
	reasons []string
	err     error
}

func (d *fakeDLQ) Send(_ context.Context, _ Message, r *tx.Rejection) error {
	if d.err != nil {
		return d.err
	}
	d.reasons = append(d.reasons, r.Reason)
	return nil
}

func setup() (Processor, *fakeStore, *fakeDLQ) {
	s, d := &fakeStore{seen: map[string]bool{}}, &fakeDLQ{}
	return Processor{Store: s, Schemas: fakeSchemas{}, DLQ: d, Metrics: metrics.New()}, s, d
}

func valid(id string) []byte {
	b, _ := json.Marshal(tx.Transaction{
		TransactionID: id, BookedAt: time.Now(), PaymentType: "transfer", AmountMinor: 100, Currency: "EUR",
		DebtorIBAN:   tx.MakeIBAN("FR", "30001000010000000000100"),
		CreditorIBAN: tx.MakeIBAN("FR", "30001000010000000000200"),
	})
	return wire.Encode(1, b)
}

func TestDoublonsAbsorbes(t *testing.T) {
	p, _, d := setup()
	for range 100 {
		if err := p.Process(context.Background(), Message{Value: valid("a")}); err != nil {
			t.Fatal(err)
		}
	}
	if w, dup := p.Metrics.Get("ingestion_ecrites_total"), p.Metrics.Get("ingestion_doublons_absorbes_total"); w != 1 || dup != 99 {
		t.Fatalf("écrites %d, doublons %d ; attendu 1 et 99", w, dup)
	}
	if len(d.reasons) != 0 {
		t.Fatalf("un doublon ne va pas en DLQ : %v", d.reasons)
	}
}

func TestRejetsVersDLQ(t *testing.T) {
	p, _, d := setup()
	msgs := [][]byte{
		[]byte(`{"pas":"d'en-tête"}`),
		wire.Encode(42, []byte(`{}`)), // schéma inconnu
		wire.Encode(1, []byte(`{"transaction_id":`)),
	}
	for _, m := range msgs {
		if err := p.Process(context.Background(), Message{Value: m}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{tx.ReasonSchema, tx.ReasonSchema, tx.ReasonMalformed}
	if len(d.reasons) != len(want) {
		t.Fatalf("DLQ %v, attendu %v", d.reasons, want)
	}
	for i := range want {
		if d.reasons[i] != want[i] {
			t.Fatalf("DLQ %v, attendu %v", d.reasons, want)
		}
	}
}

func TestRefusDeLaBaseVersDLQ(t *testing.T) {
	p, s, d := setup()
	s.err = &pgconn.PgError{Code: "23514", Message: "check"}
	if err := p.Process(context.Background(), Message{Value: valid("b")}); err != nil {
		t.Fatal(err)
	}
	if len(d.reasons) != 1 || d.reasons[0] != "refus_base" {
		t.Fatalf("DLQ %v", d.reasons)
	}
}

// Une panne ne doit JAMAIS envoyer le message en DLQ ni le compter : l'appelant réessaie.
func TestPanneRenvoieErreur(t *testing.T) {
	for name, p := range map[string]Processor{
		"base":     func() Processor { p, s, _ := setup(); s.err = errors.New("connexion refusée"); return p }(),
		"registre": func() Processor { p, _, _ := setup(); p.Schemas = fakeSchemas{err: errors.New("timeout")}; return p }(),
	} {
		if err := p.Process(context.Background(), Message{Value: valid("c")}); err == nil {
			t.Errorf("%s : panne avalée", name)
		}
		if len(p.DLQ.(*fakeDLQ).reasons) != 0 {
			t.Errorf("%s : message envoyé en DLQ pendant une panne", name)
		}
	}

	p, _, d := setup()
	d.err = errors.New("DLQ injoignable")
	if err := p.Process(context.Background(), Message{Value: []byte("x")}); err == nil {
		t.Error("échec d'envoi en DLQ avalé : le message serait perdu")
	}
}
