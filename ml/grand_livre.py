"""Lecture du grand livre Postgres (attaché à DuckDB sous le nom `pg`) dans la table `ops` de variables.py.

Un seul code pour l'entraînement du modèle de service et pour le scoreur : les variables d'une opération
y sont calculées sur la MÊME vue des comptes (celle du grand livre), donc sans écart entre les deux.

Identifiant de compte « banque/compte » : banque = code banque + guichet de l'IBAN fictif (caractères
5 à 14), comme la banque AMLworld à l'entraînement. Montants reconvertis en unités de la devise.
"""

from __future__ import annotations

import duckdb

_OPS = """
CREATE OR REPLACE TEMP TABLE ops AS
SELECT t.transaction_id,
       (t.booked_at AT TIME ZONE 'UTC')::TIMESTAMP AS ts,
       substr(t.debtor_account, 5, 10) || '/' || t.debtor_account AS src,
       substr(t.creditor_account, 5, 10) || '/' || t.creditor_account AS dst,
       t.amount_minor / (10 ** CASE t.currency WHEN 'XBT' THEN 8 ELSE 2 END) AS montant,
       t.currency::VARCHAR AS devise, t.currency::VARCHAR AS devise_recue,
       coalesce(t.payment_format, 'inconnu') AS format
FROM pg.public.transactions t
"""


def attacher(url: str) -> duckdb.DuckDBPyConnection:
    con = duckdb.connect()
    con.execute("INSTALL postgres; LOAD postgres;")
    con.execute(f"ATTACH '{url}' AS pg (TYPE postgres, READ_ONLY false)")
    return con


def charger_tout(con: duckdb.DuckDBPyConnection) -> None:
    """Tout le grand livre (entraînement du modèle de service)."""
    con.execute(_OPS)


def charger_comptes_de(con: duckdb.DuckDBPyConnection, ids: list[str]) -> None:
    """Toutes les opérations des émetteurs et bénéficiaires des opérations `ids` : suffisant pour calculer
    exactement leurs variables (tests/test_variables.py::test_sous_ensemble_des_comptes_suffit)."""
    con.execute("CREATE OR REPLACE TEMP TABLE _ids AS SELECT unnest(?::VARCHAR[]) AS transaction_id", [ids])
    con.execute("""
        CREATE OR REPLACE TEMP TABLE _comptes AS
        SELECT debtor_account AS compte FROM pg.public.transactions WHERE transaction_id IN (SELECT transaction_id FROM _ids)
        UNION SELECT creditor_account FROM pg.public.transactions WHERE transaction_id IN (SELECT transaction_id FROM _ids)""")
    con.execute(_OPS + """
        WHERE t.debtor_account IN (SELECT compte FROM _comptes) OR t.creditor_account IN (SELECT compte FROM _comptes)""")
