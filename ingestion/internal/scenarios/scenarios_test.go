package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

const schema = 7

// Ce que l'ingestion ferait du message : nil s'il serait accepté, sinon la raison du refus.
func lire(t *testing.T, m Message) (tx.Transaction, bool) {
	t.Helper()
	id, corps, err := wire.Decode(m.Valeur)
	if err != nil || id != schema {
		return tx.Transaction{}, false
	}
	op, err := tx.Parse(corps)
	return op, err == nil
}

func TestScenariosValidesEtAttendus(t *testing.T) {
	for _, nom := range []string{"schtroumpfage", "cycle", "paie", "fanin", "especes", "normal"} {
		r, err := Nouveau(schema).Par(nom)
		if err != nil {
			t.Fatal(err)
		}
		if r.NbMsg != len(r.Messages) || r.NbMsg == 0 {
			t.Fatalf("%s : %d messages annoncés, %d produits", nom, r.NbMsg, len(r.Messages))
		}
		comptes := map[string]bool{}
		ids := map[string]bool{}
		for _, m := range r.Messages {
			op, ok := lire(t, m)
			if !ok {
				t.Fatalf("%s : message refusé par l'ingestion", nom)
			}
			if ids[op.TransactionID] {
				t.Fatalf("%s : identifiant %s en double", nom, op.TransactionID)
			}
			ids[op.TransactionID] = true
			comptes[op.DebtorIBAN], comptes[op.CreditorIBAN] = true, true
		}
		// Chaque compte attendu (ou exclu) doit apparaître dans les opérations du scénario.
		for _, a := range append(append([]Attendu{}, r.Attendus...), r.Absents...) {
			if !comptes[a.Entite] {
				t.Errorf("%s : %s sur %s, compte absent des opérations", nom, a.Regle, a.Entite)
			}
		}
	}
}

func TestSchtroumpfageSousLeSeuil(t *testing.T) {
	r := Nouveau(schema).Schtroumpfage()
	if len(r.Attendus) != 7 || r.Attendus[0].Regle != "fan_in" {
		t.Fatalf("attendus : %+v", r.Attendus)
	}
	depots := map[string]int64{}
	for _, m := range r.Messages {
		op, _ := lire(t, m)
		if op.PaymentType == "cash_deposit" {
			depots[op.CreditorIBAN] += op.AmountMinor
		}
	}
	if len(depots) != 6 {
		t.Fatalf("%d mules qui déposent, 6 attendues", len(depots))
	}
	for mule, total := range depots {
		if total < 800_000 || total > 1_000_000 { // règle COSI : 8 000 à 10 000 € sur le mois
			t.Errorf("mule %s : %d centimes déposés, hors de la fourchette de la règle", mule, total)
		}
	}
}

func TestPaieSansRafaleAvantLaPaie(t *testing.T) {
	r := Nouveau(schema).Paie()
	if len(r.Attendus) != 0 || len(r.Absents) != 1 || r.Absents[0].Regle != "fan_out" {
		t.Fatalf("piège mal décrit : %+v / %+v", r.Attendus, r.Absents)
	}
	// Historique : jamais plus de 4 fournisseurs en 96 h, sinon le scénario déclencherait lui-même la règle.
	var dates []int64
	for _, m := range r.Messages[:30] {
		op, _ := lire(t, m)
		dates = append(dates, op.BookedAt.Unix())
	}
	for i := range dates {
		n := 0
		for _, d := range dates {
			if d >= dates[i] && d < dates[i]+96*3600 {
				n++
			}
		}
		if n > 4 {
			t.Fatalf("%d fournisseurs en 96 h dans l'historique", n)
		}
	}
}

func TestDoublonsEtCorrompus(t *testing.T) {
	g := Nouveau(schema)
	d := g.Doublons(100)
	if d.NbMsg != 100 {
		t.Fatalf("%d doublons", d.NbMsg)
	}
	for _, m := range d.Messages {
		if string(m.Valeur) != string(d.Messages[0].Valeur) {
			t.Fatal("les doublons doivent être identiques à l'octet près")
		}
	}
	c := g.Corrompus(20)
	for i, m := range c.Messages {
		if _, ok := lire(t, m); ok {
			t.Errorf("message corrompu n° %d accepté", i)
		}
	}
}

func TestSchtroumpfageLePremierDuMois(t *testing.T) {
	g := Nouveau(schema)
	g.Now = func() time.Time { return time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC) }
	for _, m := range g.Schtroumpfage().Messages {
		op, _ := lire(t, m)
		if op.BookedAt.Month() != time.December {
			t.Fatalf("opération du %v : hors du mois de la règle COSI", op.BookedAt)
		}
	}
}

func TestPrefixeCommun(t *testing.T) {
	for _, nom := range []string{"schtroumpfage", "paie", "normal", "doublons"} {
		r, _ := Nouveau(schema).Par(nom)
		for _, m := range r.Messages {
			if op, _ := lire(t, m); !strings.HasPrefix(op.TransactionID, r.Prefixe) || r.Prefixe == "" {
				t.Fatalf("%s : %s hors du préfixe %q", nom, op.TransactionID, r.Prefixe)
			}
		}
	}
}
