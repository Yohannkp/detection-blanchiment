package detect

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TopicAlertes reçoit l'annonce de chaque nouvelle alerte, publiée par l'outbox.
const TopicAlertes = "alerts.aml"

type Store struct{ Pool *pgxpool.Pool }

// Enregistrer écrit les alertes d'un lot dans UNE transaction : alerte (créée une fois), preuves
// (ajoutées au fil du flux) et, pour chaque nouvelle alerte, une ligne d'outbox vers alerts.aml.
// Tout est idempotent : rejouer un lot ne crée ni alerte, ni preuve, ni annonce en double.
// Renvoie le nombre d'alertes nouvelles.
func (s Store) Enregistrer(ctx context.Context, alertes []Alerte) (int, error) {
	if len(alertes) == 0 {
		return 0, nil
	}
	nouvelles := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(q pgx.Tx) error {
		for _, a := range alertes {
			tag, err := q.Exec(ctx, `INSERT INTO alertes (alert_id, regle, entite, ouverte_le, declencheur)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (alert_id) DO NOTHING`,
				a.ID, a.Regle, a.Entite, a.OuverteLe, a.Declencheur)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				nouvelles++
				annonce, err := json.Marshal(map[string]any{
					"alert_id": a.ID, "regle": a.Regle, "entite": a.Entite,
					"ouverte_le": a.OuverteLe.UTC().Format(time.RFC3339), "declencheur": a.Declencheur,
				})
				if err != nil {
					return err
				}
				if _, err := q.Exec(ctx, `INSERT INTO outbox (topic, message_key, payload) VALUES ($1, $2, $3)`,
					TopicAlertes, a.Entite, annonce); err != nil {
					return err
				}
			}
			if _, err := q.Exec(ctx, `INSERT INTO alertes_operations (alert_id, transaction_id, signalee_le)
				SELECT $1, unnest($2::text[]), $3 ON CONFLICT DO NOTHING`, a.ID, a.Preuves, a.Quand); err != nil {
				return err
			}
		}
		return nil
	})
	return nouvelles, err
}
