#!/usr/bin/env bash
# Test de charge : envoie N opérations d'un coup et mesure quand chaque étage a tout absorbé
# (grand livre, graphe, détecteur). Plateforme lancée. Usage : bash scripts/mesurer_debit.sh [N]
set -euo pipefail
cd "$(dirname "$0")/.."
N=${1:-20000}
psql() { docker compose exec -T postgres psql -U postgres -d grand_livre -tAc "$1"; }
lag() { docker compose exec -T redpanda rpk group describe "$1" 2>/dev/null | awk '/^TOTAL-LAG/ {print $2}'; }
n0=$(psql "SELECT count(*) FROM transactions")
t0=$(date +%s.%N)
(cd ingestion && go run ./cmd/injecter -mode normal -n "$N" >/dev/null)
t_envoi=$(date +%s.%N)
until [ "$(psql "SELECT count(*) FROM transactions")" -ge $((n0 + N)) ]; do sleep 0.5; done
t_gl=$(date +%s.%N)
until [ "$(lag graph-sink)" = "0" ]; do sleep 0.5; done
t_gr=$(date +%s.%N)
until [ "$(lag detecteur)" = "0" ]; do sleep 0.5; done
t_de=$(date +%s.%N)
awk -v n="$N" -v a="$t0" -v e="$t_envoi" -v g="$t_gl" -v r="$t_gr" -v d="$t_de" 'BEGIN {
  printf "%d opérations : envoi %.1f s ; grand livre %.1f s (%.0f op/s) ; graphe %.1f s (%.0f op/s) ; détection %.1f s (%.0f op/s)\n",
    n, e-a, g-a, n/(g-a), r-a, n/(r-a), d-a, n/(d-a) }'
