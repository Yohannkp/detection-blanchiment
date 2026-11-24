package detect

// Tests d'intégration sur un vrai Redis. Ils tournent si TEST_REDIS_URL pointe vers une base
// que l'on peut vider (la CI fournit un conteneur neuf) :
//   TEST_REDIS_URL=redis://localhost:16379/15 go test ./internal/detect/

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
)

func ouvrir(t *testing.T) Etat {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL absent : tests d'intégration Redis ignorés")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	r := redis.NewClient(opts)
	t.Cleanup(func() { r.Close() })
	if err := r.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return Etat{R: r}
}

var t0 = time.Date(2022, 9, 1, 0, 0, 0, 0, time.UTC)

func iban(n int) string { return tx.MakeIBAN("FR", fmt.Sprintf("3000100001%011d00", n)) }

func virement(id string, debit, credit int, quand time.Time) tx.Transaction {
	return tx.Transaction{TransactionID: id, BookedAt: quand, PaymentType: "transfer",
		DebtorIBAN: iban(debit), CreditorIBAN: iban(credit), AmountMinor: 400_000, Currency: "EUR"}
}

// bilan : pour chaque alerte (identifiant), l'ensemble de ses preuves, après traitement de ops dans l'ordre donné.
func bilan(t *testing.T, e Etat, ops []tx.Transaction) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, o := range ops {
		alertes, err := e.Fenetres(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range alertes {
			out[a.ID] = append(out[a.ID], a.Preuves...)
		}
	}
	for id, p := range out {
		slices.Sort(p)
		out[id] = slices.Compact(p)
	}
	return out
}

func reseauFanIn() []tx.Transaction {
	ops := make([]tx.Transaction, SeuilFanIn+1)
	for i := range ops {
		ops[i] = virement(fmt.Sprintf("t%d", i), 10+i, 1, t0.Add(time.Duration(i)*time.Hour))
	}
	return ops
}

func TestFanIn(t *testing.T) {
	b := bilan(t, ouvrir(t), reseauFanIn())
	if len(b) != 1 {
		t.Fatalf("attendu une seule alerte, obtenu %v", b)
	}
	for _, p := range b {
		if len(p) != SeuilFanIn+1 {
			t.Fatalf("attendu %d preuves, obtenu %v", SeuilFanIn+1, p)
		}
	}
}

// Le cœur de la correction : n'importe quel ordre d'arrivée donne exactement les mêmes alertes.
func TestIndependantDeLOrdreDArrivee(t *testing.T) {
	e := ouvrir(t)
	reference := bilan(t, e, reseauFanIn())
	rng := rand.New(rand.NewPCG(1, 2))
	for essai := range 20 {
		ops := reseauFanIn()
		rng.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
		if essai == 0 {
			slices.Reverse(ops) // ordre exactement inverse
		}
		if err := e.R.FlushDB(context.Background()).Err(); err != nil {
			t.Fatal(err)
		}
		if b := bilan(t, e, ops); fmt.Sprint(b) != fmt.Sprint(reference) {
			t.Fatalf("ordre %v : %v, attendu %v", ids(ops), b, reference)
		}
	}
}

func ids(ops []tx.Transaction) []string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = o.TransactionID
	}
	return out
}

func TestRetraitementIdempotent(t *testing.T) {
	e := ouvrir(t)
	ops := reseauFanIn()
	premier := bilan(t, e, ops)
	if second := bilan(t, e, ops); fmt.Sprint(second) != fmt.Sprint(premier) {
		t.Fatalf("relecture : %v, attendu %v", second, premier)
	}
}

func TestFanInHorsFenetre(t *testing.T) {
	ops := make([]tx.Transaction, SeuilFanIn)
	for i := range ops { // 5 émetteurs espacés de 30 h : jamais 5 dans la même fenêtre de 96 h
		ops[i] = virement(fmt.Sprintf("t%d", i), 10+i, 1, t0.Add(time.Duration(i)*30*time.Hour))
	}
	if b := bilan(t, ouvrir(t), ops); len(b) != 0 {
		t.Fatalf("alerte alors que les émetteurs sont hors fenêtre : %v", b)
	}
}

func TestEspecesFractionnees(t *testing.T) {
	e := ouvrir(t)
	ops := make([]tx.Transaction, 4)
	for i := range ops {
		ops[i] = tx.Transaction{TransactionID: fmt.Sprintf("d%d", i), BookedAt: t0.Add(time.Duration(i) * 24 * time.Hour),
			PaymentType: "cash_deposit", CreditorIBAN: iban(7), AmountMinor: 240_000, Currency: "EUR"}
	}
	slices.Reverse(ops) // l'ordre d'arrivée ne compte pas non plus ici
	var alertes []Alerte
	for _, o := range ops {
		a, err := e.Fenetres(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range a {
			if x.Regle != EspecesFractionnees {
				t.Fatalf("la caisse interne ne doit jamais compter dans un fan-in ou fan-out : %+v", x)
			}
		}
		alertes = append(alertes, a...)
	}
	if len(alertes) == 0 || alertes[len(alertes)-1].Entite != iban(7) || len(alertes[len(alertes)-1].Preuves) != 4 {
		t.Fatalf("alerte espèces attendue sur le compte 7 avec 4 dépôts : %+v", alertes)
	}
}
