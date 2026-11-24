"""Chargement du jeu AMLworld COMPLET (toutes les opérations, y compris autovirements et change)
dans la table `ops` attendue par variables.py, et des étiquettes dans `etiquettes`.

Le modèle est entraîné et évalué sur le jeu complet, dans les conditions de l'article (et non sur le
sous-graphe de la démonstration, où la part d'opérations illicites est artificiellement élevée).
"""

from __future__ import annotations

import sys
from pathlib import Path

import duckdb

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "outils"))
from preparer_amlworld import COLONNES, DEVISES, FORMATS  # noqa: E402

FORMATS_TOUS = {**FORMATS, "Reinvestment": "reinvestment"}


def charger(con: duckdb.DuckDBPyConnection, csv: Path) -> None:
    """Crée `ops` (une ligne par opération, ordre du fichier gardé dans `ligne`) et `etiquettes`."""
    con.execute("CREATE OR REPLACE TEMP TABLE _devises (nom VARCHAR, code VARCHAR)")
    con.executemany("INSERT INTO _devises VALUES (?, ?)", [(n, c) for n, (c, _) in DEVISES.items()])
    con.execute("CREATE OR REPLACE TEMP TABLE _formats (nom VARCHAR, code VARCHAR)")
    con.executemany("INSERT INTO _formats VALUES (?, ?)", list(FORMATS_TOUS.items()))
    con.execute("SET preserve_insertion_order = true")
    con.execute(
        """
        CREATE OR REPLACE TABLE _brut AS
        SELECT row_number() OVER () AS ligne, *
        FROM read_csv(?, header = true, all_varchar = true, names = ?)
        """,
        [str(csv), COLONNES],
    )
    inconnus = con.sql("""
        SELECT DISTINCT devise_payee FROM _brut WHERE devise_payee NOT IN (SELECT nom FROM _devises)
        UNION SELECT DISTINCT devise_recue FROM _brut WHERE devise_recue NOT IN (SELECT nom FROM _devises)
        UNION SELECT DISTINCT format FROM _brut WHERE format NOT IN (SELECT nom FROM _formats)""").fetchall()
    if inconnus:
        raise ValueError(f"valeurs inconnues dans le CSV : {inconnus}")
    con.execute(
        """
        CREATE OR REPLACE TABLE ops AS
        SELECT 'AMLW-' || b.ligne AS transaction_id, b.ligne,
               strptime(b.ts, '%Y/%m/%d %H:%M') AS ts,
               b.banque_src || '/' || b.compte_src AS src,
               b.banque_dst || '/' || b.compte_dst AS dst,
               b.montant_paye::DOUBLE AS montant,
               dp.code AS devise, dr.code AS devise_recue, f.code AS format
        FROM _brut b
        JOIN _devises dp ON dp.nom = b.devise_payee
        JOIN _devises dr ON dr.nom = b.devise_recue
        JOIN _formats f ON f.nom = b.format
        """
    )
    con.execute("CREATE OR REPLACE TABLE etiquettes AS SELECT 'AMLW-' || ligne AS transaction_id, blanchiment = '1' AS y FROM _brut")
    con.execute("DROP TABLE _brut")
