// injecter envoie des opérations de test sur transactions.raw.
//
//	go run ./cmd/injecter -mode normal -n 1000     virements et dépôts valides entre 200 comptes fictifs
//	go run ./cmd/injecter -mode doublons -n 100    la MÊME opération envoyée n fois (panne réseau simulée)
//	go run ./cmd/injecter -mode corrompus -n 20    messages invalides (IBAN, devise, montant, JSON, schéma)
//
// Le simulateur complet (scénarios de blanchiment, rejeu AMLworld) viendra au lot 2 ; cet outil
// sert à vérifier le socle de bout en bout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

func main() {
	mode := flag.String("mode", "normal", "normal | doublons | corrompus")
	n := flag.Int("n", 100, "nombre de messages")
	brokers := flag.String("brokers", "localhost:19092", "brokers Kafka")
	registry := flag.String("registry", "http://localhost:18081", "Schema Registry")
	flag.Parse()

	schemaID, err := wire.NewRegistry(*registry, "transactions.raw-value").Latest()
	if err != nil {
		log.Fatal(err)
	}
	c, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(*brokers, ",")...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))
	accounts := make([]string, 200)
	for i := range accounts {
		// BBAN français fictif : banque 30001, guichet 00001, compte, clé RIB 00 (non vérifiée ici).
		accounts[i] = tx.MakeIBAN("FR", fmt.Sprintf("3000100001%011d00", i+1))
	}
	run := time.Now().UnixNano()

	var recs []*kgo.Record
	add := func(key string, value []byte) {
		recs = append(recs, &kgo.Record{Topic: "transactions.raw", Key: []byte(key), Value: value})
	}
	valid := func(i int) tx.Transaction {
		t := tx.Transaction{
			TransactionID: fmt.Sprintf("inj-%d-%d", run, i),
			BookedAt:      time.Now().UTC(),
			PaymentType:   "transfer",
			AmountMinor:   1000 + rng.Int64N(500_000), // 10 € à 5 010 €
			Currency:      "EUR",
		}
		d, cr := rng.IntN(len(accounts)), rng.IntN(len(accounts)-1)
		if cr >= d {
			cr++
		}
		t.DebtorIBAN, t.CreditorIBAN = accounts[d], accounts[cr]
		if rng.IntN(10) == 0 {
			t.PaymentType, t.DebtorIBAN = "cash_deposit", ""
		}
		return t
	}
	encode := func(t tx.Transaction) []byte { return wire.Encode(schemaID, mustJSON(t)) }
	key := func(t tx.Transaction) string { d, _ := t.Accounts(); return d }

	switch *mode {
	case "normal":
		for i := range *n {
			t := valid(i)
			add(key(t), encode(t))
		}
	case "doublons":
		t := valid(0)
		for range *n {
			add(key(t), encode(t))
		}
	case "corrompus":
		for i := range *n {
			t := valid(i)
			var v []byte
			switch i % 6 {
			case 0: // clé de contrôle fausse
				t.CreditorIBAN = t.CreditorIBAN[:4] + "9" + t.CreditorIBAN[5:]
				if tx.ValidateIBAN(t.CreditorIBAN) == nil {
					t.CreditorIBAN = t.CreditorIBAN[:4] + "8" + t.CreditorIBAN[5:]
				}
				v = encode(t)
			case 1:
				t.Currency = "XYZ"
				v = encode(t)
			case 2:
				t.AmountMinor = -t.AmountMinor
				v = encode(t)
			case 3:
				v = wire.Encode(schemaID, []byte(`{"transaction_id": "tronqué`))
			case 4:
				v = mustJSON(t) // sans en-tête de schéma
			case 5:
				v = wire.Encode(9999, mustJSON(t)) // schéma inconnu du registre
			}
			add(key(t), v)
		}
	default:
		log.Fatalf("mode inconnu %q", *mode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d message(s) « %s » envoyés (schéma %d)\n", len(recs), *mode, schemaID)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
