// Package scenarios fabrique les opérations de démonstration et de chaos (injecteur en ligne de commande
// et service Chaos) : trafic ordinaire, doublons, messages corrompus, et montages de blanchiment dont on
// sait quelles alertes ils DOIVENT lever (et, pour le piège, lesquelles ils ne doivent PAS lever).
package scenarios

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

// Message à publier sur transactions.raw (clé = compte débité : ordre garanti par compte).
type Message struct {
	Cle    string
	Valeur []byte
}

// Attendu : une alerte (règle, compte) que le scénario doit — ou ne doit pas — faire lever.
type Attendu struct {
	Regle  string `json:"regle"`
	Entite string `json:"entite"`
}

type Resultat struct {
	Nom      string    `json:"nom"`
	Resume   string    `json:"resume"`
	Messages []Message `json:"-"`
	NbMsg    int       `json:"messages"`
	Prefixe  string    `json:"prefixe"`  // toutes les opérations du scénario : la console les compte une à une
	Attendus []Attendu `json:"attendus"` // doivent apparaître
	Absents  []Attendu `json:"absents"`  // ne doivent pas apparaître (piège à faux positif)
}

// Generateur fabrique des scénarios ; chaque appel utilise des comptes neufs (jamais vus) et des
// identifiants d'opération uniques.
type Generateur struct {
	SchemaID int
	Rng      *rand.Rand
	Now      func() time.Time
	run      int64
	compteur atomic.Int64
}

func Nouveau(schemaID int) *Generateur {
	n := time.Now().UnixNano()
	return &Generateur{SchemaID: schemaID, Rng: rand.New(rand.NewPCG(uint64(n), 1)), Now: time.Now, run: n}
}

func (g *Generateur) id() string { return fmt.Sprintf("inj-%d-%d", g.run, g.compteur.Add(1)) }

// Neuf renvoie un IBAN français fictif valide, jamais utilisé (banque 30002).
func (g *Generateur) Neuf() string {
	return tx.MakeIBAN("FR", fmt.Sprintf("3000200002%011d00", (g.run/1000+g.compteur.Add(1))%100_000_000_000))
}

func (g *Generateur) virement(debit, credit string, montant int64, il time.Duration) tx.Transaction {
	return tx.Transaction{TransactionID: g.id(), BookedAt: g.Now().UTC().Add(-il), PaymentType: "transfer",
		DebtorIBAN: debit, CreditorIBAN: credit, AmountMinor: montant, Currency: "EUR"}
}

func (g *Generateur) depot(credit string, montant int64, il time.Duration) tx.Transaction {
	return tx.Transaction{TransactionID: g.id(), BookedAt: g.Now().UTC().Add(-il), PaymentType: "cash_deposit",
		CreditorIBAN: credit, AmountMinor: montant, Currency: "EUR"}
}

func (g *Generateur) message(t tx.Transaction) Message {
	debit, _ := t.Accounts()
	b, _ := json.Marshal(t)
	return Message{Cle: debit, Valeur: wire.Encode(g.SchemaID, b)}
}

func (g *Generateur) resultat(nom, resume string, ops []tx.Transaction, attendus, absents []Attendu) Resultat {
	r := Resultat{Nom: nom, Resume: resume, Attendus: attendus, Absents: absents, Prefixe: fmt.Sprintf("inj-%d-", g.run)}
	for _, t := range ops {
		r.Messages = append(r.Messages, g.message(t))
	}
	r.NbMsg = len(r.Messages)
	if r.Attendus == nil {
		r.Attendus = []Attendu{}
	}
	if r.Absents == nil {
		r.Absents = []Attendu{}
	}
	return r
}

// Normal : n virements et dépôts entre 200 comptes propres à l'essai (trafic de fond, test de charge).
// Comptes neufs à chaque essai : avec des comptes fixes, les fenêtres de 96 h se remplissaient d'un test
// à l'autre et la mesure de débit se dégradait (artefact du banc, pas du système).
func (g *Generateur) Normal(n int) Resultat {
	comptes := make([]string, 200)
	for i := range comptes {
		comptes[i] = g.Neuf()
	}
	ops := make([]tx.Transaction, n)
	for i := range ops {
		d, c := g.Rng.IntN(len(comptes)), g.Rng.IntN(len(comptes)-1)
		if c >= d {
			c++
		}
		ops[i] = g.virement(comptes[d], comptes[c], 1000+g.Rng.Int64N(500_000), 0)
		if g.Rng.IntN(10) == 0 {
			ops[i] = g.depot(comptes[c], ops[i].AmountMinor, 0)
		}
	}
	return g.resultat("normal", fmt.Sprintf("%d opérations ordinaires", n), ops, nil, nil)
}

// Doublons : la MÊME opération envoyée n fois (panne réseau simulée). Une seule doit compter.
func (g *Generateur) Doublons(n int) Resultat {
	t := g.virement(g.Neuf(), g.Neuf(), 100_000+g.Rng.Int64N(900_000), 0)
	r := g.resultat("doublons", fmt.Sprintf("la même opération envoyée %d fois : une seule doit être comptée", n), nil, nil, nil)
	m := g.message(t)
	for range n {
		r.Messages = append(r.Messages, m)
	}
	r.NbMsg = n
	return r
}

// Corrompus : n messages invalides (IBAN faux, devise inconnue, montant négatif, JSON tronqué, sans
// en-tête de schéma, schéma inconnu). Tous doivent partir en DLQ sans rien bloquer.
func (g *Generateur) Corrompus(n int) Resultat {
	r := g.resultat("corrompus", fmt.Sprintf("%d messages invalides : tous doivent partir en DLQ, rien ne doit bloquer", n), nil, nil, nil)
	for i := range n {
		t := g.virement(g.Neuf(), g.Neuf(), 1000+g.Rng.Int64N(500_000), 0)
		b, _ := json.Marshal(t)
		m := Message{Cle: t.DebtorIBAN}
		switch i % 6 {
		case 0: // un chiffre changé : la clé de contrôle mod-97 ne tombe plus juste
			c := []byte(t.CreditorIBAN)
			c[4] = '0' + (c[4]-'0'+1)%10
			t.CreditorIBAN = string(c)
			b, _ = json.Marshal(t)
			m.Valeur = wire.Encode(g.SchemaID, b)
		case 1:
			t.Currency = "XYZ"
			b, _ = json.Marshal(t)
			m.Valeur = wire.Encode(g.SchemaID, b)
		case 2:
			t.AmountMinor = -t.AmountMinor
			b, _ = json.Marshal(t)
			m.Valeur = wire.Encode(g.SchemaID, b)
		case 3:
			m.Valeur = wire.Encode(g.SchemaID, []byte(`{"transaction_id": "tronqué`))
		case 4:
			m.Valeur = b // sans en-tête de schéma
		case 5:
			m.Valeur = wire.Encode(9999, b) // schéma inconnu du registre
		}
		r.Messages = append(r.Messages, m)
	}
	r.NbMsg = n
	return r
}

// FanIn : 6 comptes neufs versent chacun 3 500 à 4 800 € au même compte en quelques minutes.
func (g *Generateur) FanIn() Resultat {
	cible := g.Neuf()
	ops := make([]tx.Transaction, 6)
	for i := range ops {
		ops[i] = g.virement(g.Neuf(), cible, 350_000+g.Rng.Int64N(130_000), time.Duration(6-i)*time.Minute)
	}
	return g.resultat("fanin", "6 comptes neufs versent chacun 3 500 à 4 800 € au même compte", ops, []Attendu{{"fan_in", cible}}, nil)
}

// Cycle : 80 000 € font le tour de 4 sociétés écrans (2 % de commission à chaque étape) et reviennent.
func (g *Generateur) Cycle() Resultat {
	c := []string{g.Neuf(), g.Neuf(), g.Neuf(), g.Neuf()}
	ops := make([]tx.Transaction, 4)
	for i := range ops {
		montant := int64(8_000_000 * math.Pow(0.98, float64(i)))
		ops[i] = g.virement(c[i], c[(i+1)%4], montant, time.Duration(4-i)*time.Minute)
	}
	return g.resultat("cycle", "80 000 € font le tour de 4 sociétés écrans (2 % de commission par étape) et reviennent à l'origine",
		ops, []Attendu{{"cycle", c[0]}}, nil)
}

// Especes : 4 dépôts d'espèces de 2 400 € sur un compte neuf dans le mois (9 600 €, juste sous la COSI).
func (g *Generateur) Especes() Resultat {
	cible := g.Neuf()
	ops := make([]tx.Transaction, 4)
	for i := range ops {
		ops[i] = g.depot(cible, 240_000, time.Duration(4-i)*time.Minute)
	}
	return g.resultat("especes", "4 dépôts d'espèces de 2 400 € sur un compte neuf : 9 600 €, juste sous le seuil COSI",
		ops, []Attendu{{"especes_fractionnees", cible}}, nil)
}

// Schtroumpfage (version française) : 6 mules déposent chacune des espèces en restant sous le seuil
// COSI (3 dépôts, environ 9 000 € sur le mois), puis virent le tout vers un même bénéficiaire.
// Attendus : une alerte « espèces fractionnées » par mule, et un fan-in sur le bénéficiaire.
func (g *Generateur) Schtroumpfage() Resultat {
	beneficiaire := g.Neuf()
	// La règle compte les dépôts par mois civil UTC : le 1er du mois avant 3 h, on resserre les dates pour
	// que les 3 dépôts d'une mule tombent dans le même mois (sinon pas d'alerte, et la CI échouerait).
	maintenant := g.Now().UTC()
	recul := min(3*time.Hour, maintenant.Sub(time.Date(maintenant.Year(), maintenant.Month(), 1, 0, 0, 0, 0, time.UTC)))
	il := func(minutes int) time.Duration { return time.Duration(minutes) * recul / 180 } // = minutes, sauf début de mois
	var ops []tx.Transaction
	attendus := []Attendu{{"fan_in", beneficiaire}}
	for range 6 {
		mule := g.Neuf()
		var total int64
		for j := range 3 {
			montant := 290_000 + g.Rng.Int64N(40_000) // 2 900 à 3 300 €
			total += montant
			ops = append(ops, g.depot(mule, montant, il(180-j*30)))
		}
		ops = append(ops, g.virement(mule, beneficiaire, total, il(10)))
		attendus = append(attendus, Attendu{"especes_fractionnees", mule})
	}
	return g.resultat("schtroumpfage", "6 mules déposent des espèces sous le seuil COSI, puis virent environ 54 000 € à un même bénéficiaire",
		ops, attendus, nil)
}

// Paie : une entreprise verse 80 salaires le même jour (fan-out massif mais légitime). Elle a déjà un
// historique fourni (30 fournisseurs payés, un par jour, au cours du mois écoulé) : le plafond d'historique de la règle fan-out
// la reconnaît comme un compte « hub » et aucune alerte ne doit être levée. C'est le piège à faux positif.
// (Une entreprise toute neuve, elle, déclencherait l'alerte sur ses premiers virements : limite assumée,
// le plafond compte les contreparties vues jusqu'ici.)
func (g *Generateur) Paie() Resultat {
	employeur := g.Neuf()
	var ops []tx.Transaction
	// Un fournisseur par jour pendant 30 jours : jamais plus de 4 dans une fenêtre de 96 h, donc l'historique
	// se construit sans déclencher la règle (sinon le scénario lui-même ferait sortir l'alerte).
	for i := range 30 {
		ops = append(ops, g.virement(employeur, g.Neuf(), 50_000+g.Rng.Int64N(500_000), time.Duration(40-i)*24*time.Hour))
	}
	for i := range 80 {
		ops = append(ops, g.virement(employeur, g.Neuf(), 180_000+g.Rng.Int64N(220_000), time.Duration(80-i)*time.Second))
	}
	return g.resultat("paie", "une entreprise établie (30 fournisseurs déjà payés) verse 80 salaires le même jour : aucune alerte ne doit sortir",
		ops, nil, []Attendu{{"fan_out", employeur}})
}

// Par nom (service Chaos).
func (g *Generateur) Par(nom string) (Resultat, error) {
	switch nom {
	case "schtroumpfage":
		return g.Schtroumpfage(), nil
	case "cycle":
		return g.Cycle(), nil
	case "paie":
		return g.Paie(), nil
	case "doublons":
		return g.Doublons(100), nil
	case "corrompus":
		return g.Corrompus(20), nil
	case "fanin":
		return g.FanIn(), nil
	case "normal": // trafic de fond (pendant la panne du graph-sink : le grand livre doit continuer)
		return g.Normal(300), nil
	case "especes":
		return g.Especes(), nil
	}
	return Resultat{}, fmt.Errorf("scénario inconnu %q", nom)
}

// Records transforme un scénario en messages Kafka pour transactions.raw.
func Records(r Resultat) []*kgo.Record {
	recs := make([]*kgo.Record, len(r.Messages))
	for i, m := range r.Messages {
		recs[i] = &kgo.Record{Topic: "transactions.raw", Key: []byte(m.Cle), Value: m.Valeur}
	}
	return recs
}
