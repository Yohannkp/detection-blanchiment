// rejouer envoie sur transactions.raw les opérations préparées par outils/preparer_amlworld.py,
// dans l'ordre chronologique, à un débit choisi. Les horodatages d'origine (septembre 2022) sont
// conservés : la détection travaille en temps de l'événement, pas en temps d'envoi.
//
//	go run ./cmd/rejouer -debit 200               200 opérations par seconde
//	go run ./cmd/rejouer -debit 0 -limite 10000   au plus vite, 10 000 opérations
//	go run ./cmd/rejouer -depuis 50000            reprendre après les 50 000 premières
//
// Les identifiants sont déterministes (AMLW-<ligne>) : rejouer deux fois ne crée aucun doublon au
// grand livre, l'ingestion les absorbe.
package main

import (
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

var colonnes = []string{"transaction_id", "booked_at", "payment_type", "debtor_iban", "creditor_iban",
	"amount_minor", "currency", "payment_format"}

func main() {
	fichier := flag.String("fichier", "../data/rejeu.csv", "sortie de preparer_amlworld.py")
	debit := flag.Int("debit", 200, "opérations par seconde (0 = au plus vite)")
	limite := flag.Int("limite", 0, "nombre maximal d'opérations (0 = toutes)")
	depuis := flag.Int("depuis", 0, "ignorer les n premières opérations")
	brokers := flag.String("brokers", "localhost:19092", "brokers Kafka")
	registry := flag.String("registry", "http://localhost:18081", "Schema Registry")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	n, err := rejouer(ctx, *fichier, *debit, *limite, *depuis, *brokers, *registry)
	fmt.Printf("%d opération(s) envoyée(s)\n", n)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// lire décode une ligne et la valide avec les MÊMES règles que l'ingestion : un fichier mal
// préparé arrête le rejeu au lieu de remplir la DLQ.
func lire(rec []string) (tx.Transaction, error) {
	booked, err := time.Parse(time.RFC3339, rec[1])
	if err != nil {
		return tx.Transaction{}, err
	}
	amount, err := strconv.ParseInt(rec[5], 10, 64)
	if err != nil {
		return tx.Transaction{}, err
	}
	t := tx.Transaction{TransactionID: rec[0], BookedAt: booked, PaymentType: rec[2], DebtorIBAN: rec[3],
		CreditorIBAN: rec[4], AmountMinor: amount, Currency: rec[6], PaymentFormat: rec[7]}
	return t, t.Validate()
}

func rejouer(ctx context.Context, fichier string, debit, limite, depuis int, brokers, registry string) (int, error) {
	f, err := os.Open(fichier)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	entete, err := r.Read()
	if err != nil {
		return 0, err
	}
	if strings.Join(entete, ",") != strings.Join(colonnes, ",") {
		return 0, fmt.Errorf("en-tête inattendu %v (attendu %v)", entete, colonnes)
	}

	schemaID, err := wire.NewRegistry(registry, "transactions.raw-value").Latest()
	if err != nil {
		return 0, err
	}
	c, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...), kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(20*time.Millisecond))
	if err != nil {
		return 0, err
	}
	defer c.Close()

	var (
		mu    sync.Mutex // le rappel de Produce tourne dans une autre goroutine
		echec error
	)
	envoyes, ligne := 0, 0
	debut := time.Now()
	for limite == 0 || envoyes < limite {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return envoyes, err
		}
		ligne++
		if ligne <= depuis {
			continue
		}
		t, err := lire(rec)
		if err != nil {
			return envoyes, fmt.Errorf("ligne %d (%s) : %w", ligne+1, rec[0], err)
		}
		payload, err := json.Marshal(t)
		if err != nil {
			return envoyes, err
		}
		debitKey, _ := t.Accounts()
		c.Produce(ctx, &kgo.Record{Topic: "transactions.raw", Key: []byte(debitKey), Value: wire.Encode(schemaID, payload)},
			func(_ *kgo.Record, err error) {
				if err != nil {
					mu.Lock()
					echec = cmp.Or(echec, err)
					mu.Unlock()
				}
			})
		envoyes++
		if debit > 0 { // cadence : on attend l'instant prévu pour cette opération
			if attente := time.Until(debut.Add(time.Duration(envoyes) * time.Second / time.Duration(debit))); attente > 0 {
				select {
				case <-ctx.Done():
					return envoyes, ctx.Err()
				case <-time.After(attente):
				}
			}
		}
		if envoyes%10_000 == 0 {
			log.Printf("%d envoyées (horloge du rejeu : %s)", envoyes, t.BookedAt.Format("2006-01-02 15:04"))
		}
	}
	if err := c.Flush(ctx); err != nil {
		return envoyes, err
	}
	mu.Lock()
	defer mu.Unlock()
	return envoyes, echec
}
