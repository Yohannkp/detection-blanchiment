"""Calcule les variables et les enregistre en Parquet (cache de l'entraînement), depuis l'une des deux sources :

  python ml/calculer_variables.py data/HI-Small_Trans.csv data/variables_hi_small.parquet
      jeu AMLworld COMPLET : modèle de référence, comparable à l'article ;
  python ml/calculer_variables.py --grand-livre postgresql://postgres:postgres@localhost:15433/grand_livre data/variables_grand_livre.parquet
      le grand livre de la plateforme (vue que voit le scoreur) : modèle de SERVICE. Il faut un rôle qui
      lit evaluation.verite_terrain (superutilisateur) ; seules les opérations étiquetées sont gardées.
"""

import argparse
import sys
import time
from pathlib import Path

import duckdb

sys.path.insert(0, str(Path(__file__).resolve().parent))
from variables import construire  # noqa: E402


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("source", nargs="?", type=Path, help="CSV AMLworld")
    ap.add_argument("sortie", type=Path)
    ap.add_argument("--grand-livre", metavar="URL", help="lire le grand livre Postgres au lieu du CSV")
    a = ap.parse_args()
    debut = time.time()
    if a.grand_livre:
        from grand_livre import attacher, charger_tout
        con = attacher(a.grand_livre)
        charger_tout(con)
        con.execute("""CREATE OR REPLACE TABLE etiquettes AS
                       SELECT transaction_id, is_laundering AS y FROM pg.evaluation.verite_terrain""")
        con.execute("CREATE OR REPLACE TEMP TABLE _lignes AS SELECT transaction_id, ts, CAST(substr(transaction_id, 6) AS BIGINT) AS ligne FROM ops WHERE transaction_id LIKE 'AMLW-%'")
    else:
        from donnees import charger
        con = duckdb.connect()
        charger(con, a.source)
        con.execute("CREATE OR REPLACE TEMP TABLE _lignes AS SELECT transaction_id, ts, ligne FROM ops")
    con.execute("SET temp_directory = ?", [str(a.sortie.parent / "duckdb_tmp")])
    print(f"chargé : {con.sql('SELECT count(*) FROM ops').fetchall()[0][0]} opérations ({time.time() - debut:.0f} s)")
    noms = construire(con)
    print(f"{len(noms)} variables calculées ({time.time() - debut:.0f} s)")
    # Seules les opérations étiquetées partent à l'entraînement (les variables, elles, voient tout l'historique).
    con.execute(f"""COPY (SELECT l.ligne, l.ts, v.*, e.y FROM variables v JOIN _lignes l USING (transaction_id)
                         JOIN etiquettes e USING (transaction_id) ORDER BY l.ts, l.ligne)
                    TO '{a.sortie.as_posix()}' (FORMAT parquet)""")
    print(f"écrit : {a.sortie} ({time.time() - debut:.0f} s)")


if __name__ == "__main__":
    main()
