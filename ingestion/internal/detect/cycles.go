package detect

import (
	"context"
	"fmt"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Pour chaque opération e = u → v du lot : existe-t-il un chemin v → … → u (1 à SautsMaxCycle
// opérations) tel que tout le cycle (chemin + e) tienne en 7 jours, sans passer par un hub ?
//
// e peut occuper N'IMPORTE QUELLE place dans le cycle : on cherche les opérations dans les 7 jours
// avant ET après e. Ainsi l'opération du cycle qui arrive en dernier au détecteur (le graph-sink l'a
// écrite après toutes les autres) trouve toujours le cycle complet, quel que soit l'ordre d'arrivée.
//
// Le compte d'origine est celui qui émet la plus ancienne opération du cycle (égalité départagée par
// transaction_id) : le même cycle trouvé depuis n'importe laquelle de ses opérations donne le même compte.
// Les prédicats sont dans le motif (quantified path pattern) : Neo4j élague pendant le parcours.
var requeteCycles = fmt.Sprintf(`
UNWIND $ids AS id
MATCH (u:Account)-[e:TRANSFERRED {transaction_id: id}]->(v:Account)
WHERE NOT u.interne AND NOT v.interne AND COUNT { (v)--() } <= %[2]d
CALL (u, v, e) {
  MATCH p = (v) ((a)-[r:TRANSFERRED WHERE r.booked_at >= e.booked_at - duration({hours: %[1]d})
                                      AND r.booked_at <= e.booked_at + duration({hours: %[1]d})]->(b)
                 WHERE NOT b.interne AND COUNT { (b)--() } <= %[2]d){1,%[3]d} (u)
  WITH relationships(p) + [e] AS rs
  WITH rs,
       reduce(m = rs[0], r IN rs | CASE WHEN r.booked_at < m.booked_at
            OR (r.booked_at = m.booked_at AND r.transaction_id < m.transaction_id) THEN r ELSE m END) AS premiere,
       reduce(f = rs[0].booked_at, r IN rs | CASE WHEN r.booked_at > f THEN r.booked_at ELSE f END) AS fin
  WHERE fin <= premiere.booked_at + duration({hours: %[1]d})
  RETURN startNode(premiere).id AS compte, fin, [r IN rs | r.transaction_id] AS chemin
  LIMIT 1
}
RETURN id, compte, fin, chemin`, int(FenetreCycle.Hours()), PlafondHubCycle, SautsMaxCycle)

// Cycle trouvé : opération examinée, compte d'origine, dernière date du cycle, toutes ses opérations.
type cycleTrouve struct {
	tx, compte string
	fin        time.Time
	chemin     []string
}

type Cycles struct{ Driver neo4j.DriverWithContext }

func (c Cycles) Chercher(ctx context.Context, ids []string) ([]cycleTrouve, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	res, err := neo4j.ExecuteQuery(ctx, c.Driver, requeteCycles, map[string]any{"ids": ids},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, err
	}
	out := make([]cycleTrouve, 0, len(res.Records))
	for _, r := range res.Records {
		id, _, err := neo4j.GetRecordValue[string](r, "id")
		if err != nil {
			return nil, err
		}
		compte, _, err := neo4j.GetRecordValue[string](r, "compte")
		if err != nil {
			return nil, err
		}
		fin, _, err := neo4j.GetRecordValue[time.Time](r, "fin")
		if err != nil {
			return nil, err
		}
		brut, _, err := neo4j.GetRecordValue[[]any](r, "chemin")
		if err != nil {
			return nil, err
		}
		chemin := make([]string, 0, len(brut))
		for _, x := range brut {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("chemin de cycle illisible pour %s", id)
			}
			chemin = append(chemin, s)
		}
		out = append(out, cycleTrouve{id, compte, fin, chemin})
	}
	return out, nil
}
