// injecter envoie des opérations de test sur transactions.raw (scénarios : internal/scenarios).
//
//	go run ./cmd/injecter -mode normal -n 1000     virements et dépôts valides entre 200 comptes neufs
//	go run ./cmd/injecter -mode doublons -n 100    la MÊME opération envoyée n fois (panne réseau simulée)
//	go run ./cmd/injecter -mode corrompus -n 20    messages invalides (IBAN, devise, montant, JSON, schéma)
//	go run ./cmd/injecter -mode fanin              6 comptes neufs versent chacun 3 500 à 4 800 € à un même compte
//	go run ./cmd/injecter -mode cycle              80 000 € font le tour de 4 sociétés écrans
//	go run ./cmd/injecter -mode especes            4 dépôts d'espèces de 2 400 € sur un compte neuf
//	go run ./cmd/injecter -mode schtroumpfage      6 mules sous le seuil COSI, puis un même bénéficiaire
//	go run ./cmd/injecter -mode paie               80 salaires d'une entreprise établie (aucune alerte attendue)
//
// Pour les montages, affiche « entite: <IBAN> » (le compte de la première alerte attendue) et la liste
// complète des alertes attendues ou exclues.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/scenarios"
	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/wire"
)

func main() {
	mode := flag.String("mode", "normal", "normal | doublons | corrompus | fanin | cycle | especes | schtroumpfage | paie")
	n := flag.Int("n", 100, "nombre de messages (normal, doublons, corrompus)")
	brokers := flag.String("brokers", "localhost:19092", "brokers Kafka")
	registry := flag.String("registry", "http://localhost:18081", "Schema Registry")
	flag.Parse()

	schemaID, err := wire.NewRegistry(*registry, "transactions.raw-value").Latest()
	if err != nil {
		log.Fatal(err)
	}
	g := scenarios.Nouveau(schemaID)
	var r scenarios.Resultat
	switch *mode {
	case "normal":
		r = g.Normal(*n)
	case "doublons":
		r = g.Doublons(*n)
	case "corrompus":
		r = g.Corrompus(*n)
	default:
		if r, err = g.Par(*mode); err != nil {
			log.Fatal(err)
		}
	}

	c, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(*brokers, ",")...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.ProduceSync(ctx, scenarios.Records(r)...).FirstErr(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d message(s) « %s » envoyés (schéma %d)\n", r.NbMsg, r.Nom, schemaID)
	if len(r.Attendus) > 0 {
		fmt.Println("entite:", r.Attendus[0].Entite)
	}
	for _, a := range r.Attendus {
		fmt.Println("attendu:", a.Regle, a.Entite)
	}
	for _, a := range r.Absents {
		fmt.Println("exclu:", a.Regle, a.Entite)
	}
}
