-- Évaluation de la détection contre la vérité terrain AMLworld (à lancer en superutilisateur :
-- les rôles applicatifs n'ont pas accès au schéma evaluation).
--   docker compose exec -T postgres psql -U postgres -d grand_livre -f - < scripts/evaluer_detection.sql
--
-- Unité de mesure : l'OPÉRATION. Une opération est « signalée » par une règle si elle figure dans
-- les preuves d'au moins une alerte de cette règle, vérifiée AVANT la fin de sa période (signalee_le) :
-- une preuve apportée par les jours suivants ne compte pas. Les réglages ont été choisis sur les jours 1 à 5
-- (calibrage) ; seuls les chiffres de la période « test » (6-10 septembre) les jugent. Les jours 11 à 18
-- sont isolés : le générateur y termine les cycles de blanchiment en cours, presque toutes les
-- opérations y sont illicites, ce qui gonflerait la précision.

\pset footer off
\echo
\echo '== Précision et rappel par règle (opérations du rejeu AMLworld) =='

WITH ops AS (
  SELECT t.transaction_id, v.is_laundering AS illicite,
         CASE WHEN t.booked_at < '2022-09-06' THEN '1-calibrage (1-5 sept.)'
              WHEN t.booked_at < '2022-09-11' THEN '2-test (6-10 sept.)'
              ELSE '3-fin du jeu (11-18 sept.)' END AS periode,
         CASE WHEN t.booked_at < '2022-09-06' THEN timestamptz '2022-09-06'
              WHEN t.booked_at < '2022-09-11' THEN timestamptz '2022-09-11'
              ELSE timestamptz '2022-09-19' END AS fin_periode
  FROM transactions t JOIN evaluation.verite_terrain v USING (transaction_id)
),
regles AS (SELECT unnest(ARRAY['fan_in', 'fan_out', 'cycle', 'toutes']) AS regle),
signal AS (
  SELECT r.regle, o.periode, o.illicite,
         EXISTS (SELECT 1 FROM alertes_operations ao JOIN alertes a USING (alert_id)
                 WHERE ao.transaction_id = o.transaction_id AND ao.signalee_le < o.fin_periode
                   AND (r.regle = 'toutes' OR a.regle = r.regle)) AS signalee
  FROM ops o CROSS JOIN regles r
),
agg AS (
  SELECT periode, regle,
         count(*) FILTER (WHERE signalee)              AS signalees,
         count(*) FILTER (WHERE signalee AND illicite) AS vrais,
         count(*) FILTER (WHERE illicite)              AS illicites
  FROM signal GROUP BY 1, 2
)
SELECT periode, regle, signalees, vrais, illicites,
       round(vrais::numeric / nullif(signalees, 0), 3) AS precision,
       round(vrais::numeric / nullif(illicites, 0), 3) AS rappel,
       round(2.0 * vrais / nullif(signalees + illicites, 0), 3) AS f1
FROM agg ORDER BY periode, array_position(ARRAY['fan_in', 'fan_out', 'cycle', 'toutes'], regle);

\echo '== Rappel par motif AMLworld, période test 6-10 sept. (toutes règles) =='
SELECT coalesce(v.motif, '(illicite hors motif)') AS motif,
       count(*) AS operations,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM alertes_operations ao WHERE ao.transaction_id = t.transaction_id AND ao.signalee_le < '2022-09-11')) AS detectees,
       round(count(*) FILTER (WHERE EXISTS (SELECT 1 FROM alertes_operations ao WHERE ao.transaction_id = t.transaction_id AND ao.signalee_le < '2022-09-11'))::numeric
             / count(*), 3) AS rappel
FROM transactions t JOIN evaluation.verite_terrain v USING (transaction_id)
WHERE v.is_laundering AND t.booked_at >= '2022-09-06' AND t.booked_at < '2022-09-11'
GROUP BY 1 ORDER BY rappel DESC;

\echo '== Alertes ouvertes et charge pour un analyste =='
SELECT a.regle, count(*) AS alertes, round(avg(n), 1) AS operations_par_alerte,
       count(*) FILTER (WHERE vrai) AS alertes_avec_au_moins_une_operation_illicite,
       round(count(*) FILTER (WHERE vrai)::numeric / count(*), 3) AS part_utile
FROM alertes a
JOIN LATERAL (SELECT count(DISTINCT ao.transaction_id) AS n, bool_or(v.is_laundering) AS vrai
              FROM alertes_operations ao JOIN evaluation.verite_terrain v USING (transaction_id)
              WHERE ao.alert_id = a.alert_id) x ON true
WHERE a.declencheur LIKE 'AMLW-%'
GROUP BY 1 ORDER BY 1;

\echo '== Latence : ingestion de l''opération déclenchante → alerte créée =='
\echo '   « scénarios » : comptes neufs de l''injecteur (banque 30002), mesure à vide ; le rejeu au plus vite mesure surtout la file d''attente'
SELECT CASE WHEN a.declencheur LIKE 'AMLW-%' THEN 'rejeu AMLworld (file d''attente)'
            WHEN a.entite LIKE 'FR__3000200002%' THEN 'scénarios injectés'
            ELSE 'trafic aléatoire de l''injecteur (rafale)' END AS origine,
       count(*) AS alertes,
       round(1000 * percentile_cont(0.5)  WITHIN GROUP (ORDER BY extract(epoch FROM a.creee_le - t.ingested_at))::numeric) AS p50_ms,
       round(1000 * max(extract(epoch FROM a.creee_le - t.ingested_at))::numeric) AS max_ms
FROM alertes a JOIN transactions t ON t.transaction_id = a.declencheur
GROUP BY 1 ORDER BY 1;
