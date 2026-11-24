-- Le score du modèle de SERVICE aide-t-il l'analyste à traiter d'abord les bonnes alertes ?
-- Seules comptent les alertes dont TOUTES les preuves tombent dans la période de test de ce modèle
-- (après le 8 septembre 2022 à 20 h 45, cf. modeles/production/score.json) et avant la fin du jeu
-- (11 septembre : 91 % d'illicites ensuite) ; les autres portent sur des opérations vues à
-- l'entraînement ou à la validation, ou sur une période où presque tout est illicite.
--   docker compose exec -T postgres psql -U postgres -d grand_livre -f - < scripts/evaluer_scores.sql

\pset footer off
WITH dernier_score AS (
  SELECT DISTINCT ON (alert_id) alert_id, score FROM alertes_scores
  WHERE modele LIKE 'detection-blanchiment-lgbm-service %'
  ORDER BY alert_id, preuves DESC, calcule_le DESC
),
a AS (
  SELECT s.alert_id, s.score, bool_or(v.is_laundering) AS utile
  FROM dernier_score s
  JOIN alertes_operations ao USING (alert_id)
  JOIN transactions t USING (transaction_id)
  JOIN evaluation.verite_terrain v USING (transaction_id)
  GROUP BY 1, 2
  HAVING min(t.booked_at) > '2022-09-08 20:45' AND max(t.booked_at) < '2022-09-11'
),
classees AS (SELECT *, row_number() OVER (ORDER BY score DESC) AS rang, count(*) OVER () AS n FROM a)
SELECT count(*) AS alertes,
       count(*) FILTER (WHERE utile) AS utiles,
       count(*) FILTER (WHERE NOT utile) AS inutiles,
       round(avg(utile::int), 3) AS taux_de_base,
       round(avg(utile::int) FILTER (WHERE rang <= n / 4), 3) AS quart_le_mieux_note,
       round(avg(utile::int) FILTER (WHERE rang > 3 * n / 4), 3) AS quart_le_moins_bien_note,
       -- AUC : probabilité qu'une alerte utile soit mieux notée qu'une alerte inutile
       round((SELECT avg(CASE WHEN x.score > y.score THEN 1 WHEN x.score = y.score THEN 0.5 ELSE 0 END)
              FROM a x JOIN a y ON x.utile AND NOT y.utile)::numeric, 3) AS auc
FROM classees;
