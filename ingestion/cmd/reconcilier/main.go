// reconcilier compare le grand livre Postgres (source de vérité) et le graphe Neo4j, devise par
// devise : nombre d'opérations et somme des montants. Il contrôle aussi l'équilibre interne du
// grand livre (v_reconciliation).
//
//	go run ./cmd/reconcilier                  un relevé, sortie JSON
//	go run ./cmd/reconcilier -attendre 60s    attend que le graphe ait rattrapé le grand livre
//
// Code de sortie : 0 si tout concorde, 1 sinon. Un graphe en retard n'est pas un écart tant que
// l'aval rattrape (au moins une fois, avec délai) : -attendre distingue le retard de la perte.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/graph"
)

type ligne struct {
	Devise          string `json:"devise"`
	GrandLivreN     int64  `json:"grand_livre_operations"`
	GrandLivreSomme int64  `json:"grand_livre_somme_minor"`
	GrapheN         int64  `json:"graphe_operations"`
	GrapheSomme     int64  `json:"graphe_somme_minor"`
	EcartN          int64  `json:"ecart_operations"`
	EcartSomme      int64  `json:"ecart_somme_minor"`
}

type releve struct {
	Horodatage        time.Time `json:"horodatage"`
	Concorde          bool      `json:"concorde"`
	GrandLivreEquilib bool      `json:"grand_livre_equilibre"`
	OutboxEnAttente   int64     `json:"outbox_en_attente"`
	Devises           []ligne   `json:"devises"`
}

func main() {
	attendre := flag.Duration("attendre", 0, "attendre jusqu'à cette durée que les deux bases concordent")
	pgURL := flag.String("postgres", "postgres://postgres:postgres@localhost:15433/grand_livre", "Postgres")
	neoURI := flag.String("neo4j", "neo4j://localhost:17687", "Neo4j")
	neoMdp := flag.String("neo4j-mdp", "motdepasse-local", "mot de passe Neo4j")
	flag.Parse()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *pgURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	driver, err := neo4j.NewDriverWithContext(*neoURI, neo4j.BasicAuth("neo4j", *neoMdp, ""))
	if err != nil {
		log.Fatal(err)
	}
	defer driver.Close(ctx)

	fin := time.Now().Add(*attendre)
	for {
		r, err := releverUneFois(ctx, pool, graph.Writer{Driver: driver})
		if err != nil {
			log.Fatal(err)
		}
		if r.Concorde || time.Now().After(fin) {
			out, _ := json.MarshalIndent(r, "", "  ")
			fmt.Println(string(out))
			if !r.Concorde {
				os.Exit(1)
			}
			return
		}
		time.Sleep(time.Second)
	}
}

func releverUneFois(ctx context.Context, pool *pgxpool.Pool, g graph.Writer) (releve, error) {
	r := releve{Horodatage: time.Now().UTC(), Concorde: true}
	var ecart, manquantes int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(abs(ecart_minor)), 0), coalesce(sum(abs(ecritures_manquantes)), 0)
		FROM v_reconciliation`).Scan(&ecart, &manquantes); err != nil {
		return r, err
	}
	r.GrandLivreEquilib = ecart == 0 && manquantes == 0
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&r.OutboxEnAttente); err != nil {
		return r, err
	}

	rows, err := pool.Query(ctx, `SELECT currency, count(*), sum(amount_minor) FROM transactions GROUP BY currency ORDER BY currency`)
	if err != nil {
		return r, err
	}
	pg := map[string][2]int64{}
	var ordre []string
	for rows.Next() {
		var d string
		var n, s int64
		if err := rows.Scan(&d, &n, &s); err != nil {
			return r, err
		}
		pg[d] = [2]int64{n, s}
		ordre = append(ordre, d)
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	neo, err := g.Totals(ctx)
	if err != nil {
		return r, err
	}
	for d := range neo {
		if _, ok := pg[d]; !ok {
			ordre = append(ordre, d) // devise présente dans le graphe seulement : écart à montrer
		}
	}
	for _, d := range ordre {
		l := ligne{Devise: d, GrandLivreN: pg[d][0], GrandLivreSomme: pg[d][1], GrapheN: neo[d][0], GrapheSomme: neo[d][1]}
		l.EcartN, l.EcartSomme = l.GrandLivreN-l.GrapheN, l.GrandLivreSomme-l.GrapheSomme
		r.Devises = append(r.Devises, l)
		if l.EcartN != 0 || l.EcartSomme != 0 {
			r.Concorde = false
		}
	}
	r.Concorde = r.Concorde && r.GrandLivreEquilib
	return r, nil
}
