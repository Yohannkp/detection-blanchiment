// Package graph écrit les opérations validées dans Neo4j : un nœud :Account par compte, une
// relation :TRANSFERRED par opération. L'écriture est idempotente (MERGE sur transaction_id,
// garanti par une contrainte d'unicité) : relire un message de transactions.valid ne change rien.
package graph

import (
	"context"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/Yohannkp/detection-blanchiment/ingestion/internal/tx"
)

// Topic reçoit chaque opération une fois écrite dans le graphe (lu par le détecteur).
const Topic = "transactions.graphe"

// Schema crée les contraintes d'unicité (idempotent).
var Schema = []string{
	`CREATE CONSTRAINT compte_unique IF NOT EXISTS FOR (a:Account) REQUIRE a.id IS UNIQUE`,
	`CREATE CONSTRAINT virement_unique IF NOT EXISTS FOR ()-[r:TRANSFERRED]-() REQUIRE r.transaction_id IS UNIQUE`,
}

const mergeBatch = `
UNWIND $rows AS row
MERGE (d:Account {id: row.debit})
  ON CREATE SET d.interne = row.debit STARTS WITH 'INTERNE:'
MERGE (c:Account {id: row.credit})
  ON CREATE SET c.interne = row.credit STARTS WITH 'INTERNE:'
MERGE (d)-[r:TRANSFERRED {transaction_id: row.id}]->(c)
  ON CREATE SET r.amount_minor = row.amount_minor, r.currency = row.currency,
                r.booked_at = datetime(row.booked_at), r.payment_type = row.payment_type,
                r.payment_format = CASE row.payment_format WHEN '' THEN null ELSE row.payment_format END`

type Writer struct{ Driver neo4j.DriverWithContext }

func (w Writer) EnsureSchema(ctx context.Context) error {
	for _, q := range Schema {
		if _, err := neo4j.ExecuteQuery(ctx, w.Driver, q, nil, neo4j.EagerResultTransformer); err != nil {
			return err
		}
	}
	return nil
}

// Write enregistre le lot dans une seule transaction Neo4j et renvoie le nombre d'opérations
// réellement créées (le reste était déjà présent : doublons absorbés).
func (w Writer) Write(ctx context.Context, batch []tx.Transaction) (created int, err error) {
	rows := make([]map[string]any, len(batch))
	for i, t := range batch {
		debit, credit := t.Accounts()
		rows[i] = map[string]any{
			"id": t.TransactionID, "debit": debit, "credit": credit,
			"amount_minor": t.AmountMinor, "currency": t.Currency,
			"booked_at": t.BookedAt.UTC().Format(time.RFC3339Nano), "payment_type": t.PaymentType,
			"payment_format": t.PaymentFormat,
		}
	}
	res, err := neo4j.ExecuteQuery(ctx, w.Driver, mergeBatch, map[string]any{"rows": rows},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithWritersRouting())
	if err != nil {
		return 0, err
	}
	return res.Summary.Counters().RelationshipsCreated(), nil
}

// Totals renvoie, par devise, le nombre d'opérations et la somme des montants présents dans le graphe.
func (w Writer) Totals(ctx context.Context) (map[string][2]int64, error) {
	res, err := neo4j.ExecuteQuery(ctx, w.Driver,
		`MATCH ()-[r:TRANSFERRED]->() RETURN r.currency AS devise, count(r) AS n, sum(r.amount_minor) AS somme`,
		nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, err
	}
	out := map[string][2]int64{}
	for _, rec := range res.Records {
		devise, _ := rec.Get("devise")
		n, _ := rec.Get("n")
		somme, _ := rec.Get("somme")
		out[devise.(string)] = [2]int64{n.(int64), somme.(int64)}
	}
	return out, nil
}
