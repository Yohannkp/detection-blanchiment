#!/usr/bin/env bash
# Charge data/verite.csv (étiquettes AMLworld) dans evaluation.verite_terrain. Rejouable sans doublon.
# Les étiquettes ne passent jamais par Kafka : la détection ne peut pas les voir.
set -euo pipefail
cd "$(dirname "$0")/.."
fichier=${1:-data/verite.csv}
docker compose exec -T postgres psql -U postgres -d grand_livre -v ON_ERROR_STOP=1 -q -c "
  CREATE TEMP TABLE v (transaction_id text, is_laundering boolean);
  COPY v FROM STDIN WITH (FORMAT csv, HEADER true);
  INSERT INTO evaluation.verite_terrain SELECT * FROM v ON CONFLICT (transaction_id) DO NOTHING;
  SELECT count(*) AS etiquettes, count(*) FILTER (WHERE is_laundering) AS illicites FROM evaluation.verite_terrain;" < "$fichier"
