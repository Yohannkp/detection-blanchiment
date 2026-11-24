#!/usr/bin/env bash
# Vérification de bout en bout du socle, stack lancée (docker compose up -d --build) :
# 1 000 opérations valides + 100 doublons + 20 messages corrompus, puis contrôle de
# Postgres, de la DLQ, de l'outbox, de la réconciliation et du graphe Neo4j. Code de sortie ≠ 0 si un contrôle échoue.
set -euo pipefail
cd "$(dirname "$0")/.."

psql() { docker compose exec -T postgres psql -U postgres -d grand_livre -tAc "$1"; }
fin_topic() { # somme des offsets de fin sur toutes les partitions
  docker compose exec -T redpanda rpk topic describe "$1" -p | awk 'NR>1 {s+=$NF} END {print s+0}'
}
metrique() { curl -fs localhost:18090/metrics | awk -v k="$1" '$1==k {print $2} END {}' | tail -1; }
attendre() { # attendre(description, commande qui doit réussir), 60 s au plus
  for _ in $(seq 60); do eval "$2" && return 0; sleep 1; done
  echo "ÉCHEC : $1" >&2; exit 1
}

tx0=$(psql "SELECT count(*) FROM transactions")
dlq0=$(fin_topic transactions.dlq)
valid0=$(fin_topic transactions.valid)
dup0=$(metrique ingestion_doublons_absorbes_total); dup0=${dup0:-0}

(cd ingestion && go run ./cmd/injecter -mode normal -n 1000 \
  && go run ./cmd/injecter -mode doublons -n 100 \
  && go run ./cmd/injecter -mode corrompus -n 20)

# normal : 1 000 opérations ; doublons : 1 seule opération retenue sur 100 copies.
attendre "1 001 opérations au grand livre" '[ $(( $(psql "SELECT count(*) FROM transactions") - tx0 )) -eq 1001 ]'
attendre "20 messages en DLQ" '[ $(( $(fin_topic transactions.dlq) - dlq0 )) -eq 20 ]'
attendre "99 doublons absorbés" '[ $(( $(metrique ingestion_doublons_absorbes_total) - dup0 )) -eq 99 ]'
attendre "outbox publiée" '[ "$(psql "SELECT count(*) FROM outbox WHERE published_at IS NULL")" -eq 0 ]'
attendre "1 001 messages sur transactions.valid" '[ $(( $(fin_topic transactions.valid) - valid0 )) -ge 1001 ]'

ecart=$(psql "SELECT coalesce(sum(ecart_minor), 0) + coalesce(sum(ecritures_manquantes), 0) FROM v_reconciliation")
[ "$ecart" -eq 0 ] || { echo "ÉCHEC : écart de réconciliation $ecart" >&2; exit 1; }

# Le graphe Neo4j doit rattraper le grand livre (même nombre d'opérations, même somme, par devise).
(cd ingestion && go run ./cmd/reconcilier -attendre 90s >/dev/null)   || { echo "ÉCHEC : Postgres et Neo4j ne concordent pas" >&2; (cd ingestion && go run ./cmd/reconcilier) >&2; exit 1; }

# Détection : chaque scénario doit ouvrir son alerte sur le compte attendu.
for cas in fanin:fan_in cycle:cycle especes:especes_fractionnees; do
  mode=${cas%%:*}; regle=${cas#*:}
  entite=$(cd ingestion && go run ./cmd/injecter -mode "$mode" | awk '/^entite:/ {print $2}')
  attendre "alerte $regle sur $entite" "[ \"\$(psql \"SELECT count(*) FROM alertes WHERE regle = '$regle' AND entite = '$entite'\")\" -ge 1 ]"
done
attendre "annonces d'alertes publiées" '[ "$(psql "SELECT count(*) FROM outbox WHERE topic = '"'alerts.aml'"' AND published_at IS NULL")" -eq 0 ]'

# API de l'analyste : santé (écart nul) et alertes des scénarios visibles dans la file.
attendre "API de l'analyste" 'curl -fs localhost:18100/sante >/dev/null'
curl -fs localhost:18100/sante | grep -q '"devises_en_ecart":0,"ecritures_manquantes":0' || { echo "ÉCHEC : l'API ne voit pas un grand livre équilibré" >&2; exit 1; }
curl -fs "localhost:18100/alertes?regle=cycle&statut=toutes&limite=500" | grep -q '"regle":"cycle"'   || { echo "ÉCHEC : l'alerte de cycle n'apparaît pas dans la file de l'API" >&2; exit 1; }

echo "OK : 1 001 opérations écrites, 99 doublons absorbés, 20 messages en DLQ, outbox publiée, écart 0,00 €, graphe Neo4j concordant, alertes fan-in, cycle et espèces fractionnées levées et visibles dans l'API"
curl -fs localhost:18090/metrics
