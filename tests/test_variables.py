"""Variables du modèle : aucune fuite du futur, et quelques valeurs vérifiées à la main."""

import random
import sys
from datetime import datetime, timedelta
from pathlib import Path

import duckdb
import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "ml"))
from variables import construire  # noqa: E402

T0 = datetime(2022, 9, 1)


def calculer(ops: list[tuple]) -> dict[str, tuple]:
    c = duckdb.connect()
    c.execute("""CREATE TABLE ops (transaction_id VARCHAR, ts TIMESTAMP, src VARCHAR, dst VARCHAR,
                 montant DOUBLE, devise VARCHAR, devise_recue VARCHAR, format VARCHAR)""")
    c.executemany("INSERT INTO ops VALUES (?, ?, ?, ?, ?, ?, ?, ?)", ops)
    construire(c)
    return {r[0]: r[1:] for r in c.sql("SELECT * FROM variables").fetchall()}


def aleatoires(n: int, graine: int, debut: datetime, comptes: int = 12) -> list[tuple]:
    rng = random.Random(graine)
    ops = []
    for i in range(n):
        src, dst = f"B{rng.randint(1, 3)}/{rng.randint(1, comptes)}", f"B{rng.randint(1, 3)}/{rng.randint(1, comptes)}"
        ts = debut + timedelta(minutes=rng.randint(0, 10 * 24 * 60))
        ops.append((f"{graine}-{i}", ts, src, dst, round(rng.uniform(1, 9000), 2), "USD", "USD", "ach"))
    return ops


def test_aucune_fuite_du_futur():
    passe = aleatoires(400, graine=1, debut=T0)
    futur = aleatoires(400, graine=2, debut=T0 + timedelta(days=11))  # strictement après tout le passé
    avant, apres = calculer(passe), calculer(passe + futur)
    assert len(avant) == 400
    for tx_id, valeurs in avant.items():
        # approx : DuckDB additionne en parallèle, l'ordre des additions change le dernier chiffre d'une moyenne
        egales = [x == pytest.approx(y, rel=1e-9) if isinstance(x, float) else x == y for x, y in zip(apres[tx_id], valeurs)]
        assert all(egales), f"{tx_id} : une opération future a changé ses variables"


def test_meme_minute_invisible():
    # Deux opérations de la même minute ne se voient pas, même à 40 s d'écart (horodatage en direct).
    a = ("a", T0, "B1/1", "B1/2", 100.0, "USD", "USD", "ach")
    b = ("b", T0 + timedelta(seconds=40), "B1/1", "B1/3", 200.0, "USD", "USD", "ach")
    c = ("c", T0 + timedelta(minutes=1), "B1/1", "B1/4", 300.0, "USD", "USD", "ach")
    c_ = duckdb.connect()
    c_.execute("""CREATE TABLE ops (transaction_id VARCHAR, ts TIMESTAMP, src VARCHAR, dst VARCHAR,
                  montant DOUBLE, devise VARCHAR, devise_recue VARCHAR, format VARCHAR)""")
    c_.executemany("INSERT INTO ops VALUES (?, ?, ?, ?, ?, ?, ?, ?)", [a, b, c])
    construire(c_)
    sorties = dict(c_.sql("SELECT transaction_id, u_n_out_24h FROM variables").fetchall())
    assert sorties == {"a": 0, "b": 0, "c": 2}  # c, une minute plus tard, voit a et b


@pytest.fixture
def exemple():
    ops = [
        ("1", T0, "B1/u", "B1/m1", 1000.0, "USD", "USD", "ach"),
        ("2", T0 + timedelta(hours=1), "B1/u", "B2/m2", 2000.0, "USD", "USD", "ach"),
        ("3", T0 + timedelta(hours=2), "B2/m2", "B1/u", 1900.0, "USD", "USD", "wire"),  # retour vers u
        ("4", T0 + timedelta(hours=100), "B1/u", "B1/m1", 500.0, "EUR", "USD", "cheque"),  # 100 h plus tard
    ]
    c = duckdb.connect()
    c.execute("""CREATE TABLE ops (transaction_id VARCHAR, ts TIMESTAMP, src VARCHAR, dst VARCHAR,
                 montant DOUBLE, devise VARCHAR, devise_recue VARCHAR, format VARCHAR)""")
    c.executemany("INSERT INTO ops VALUES (?, ?, ?, ?, ?, ?, ?, ?)", ops)
    construire(c)
    return {r["transaction_id"]: r for r in c.sql("SELECT * FROM variables").df().to_dict("records")}


def test_valeurs_a_la_main(exemple):
    v2, v3, v4 = exemple["2"], exemple["3"], exemple["4"]
    assert (v2["u_n_out_24h"], v2["u_d_out_24h"], v2["u_s_out_24h"]) == (1, 1, 1000.0)  # seule la n°1 précède
    assert v3["aller_retour_7j"] == 1          # m2 rend à u ce que u lui a envoyé 1 h avant
    assert v2["aller_retour_7j"] == 0
    assert v3["v_n_out_24h"] == 2              # u (destinataire de la n°3) avait déjà émis 2 fois
    assert v4["u_n_out_96h"] == 0              # 100 h après : hors de la fenêtre de 96 h
    assert v4["u_n_out_7j"] == 2 and v4["u_d_out_7j"] == 2
    assert v4["paire_7j"] == 1                 # u → m1 déjà vue (n°1) dans les 7 jours
    assert v4["u_n_in_96h"] == 0 and v4["u_n_in_7j"] == 1  # le retour n°3 est 98 h avant : hors 96 h, dans 7 j
    assert bool(v4["change"]) and not bool(v2["change"])
    assert v4["u_minutes_depuis_sortie"] == 99 * 60
    assert exemple["1"]["u_minutes_depuis_sortie"] == -1  # aucune sortie antérieure


def test_sous_ensemble_des_comptes_suffit():
    """Le scoreur ne charge que les opérations des comptes concernés (émetteur ou bénéficiaire d'une
    opération à scorer) : leurs variables doivent être identiques à celles calculées sur tout l'historique."""
    tout = aleatoires(600, graine=3, debut=T0, comptes=20)
    cibles = [o for o in tout if o[0].endswith(("7", "3"))][:25]
    comptes = {o[2] for o in cibles} | {o[3] for o in cibles}
    sous_ensemble = [o for o in tout if o[2] in comptes or o[3] in comptes]
    assert len(sous_ensemble) < len(tout)
    complet, partiel = calculer(tout), calculer(sous_ensemble)
    for o in cibles:
        egales = [x == pytest.approx(y, rel=1e-9) if isinstance(x, float) else x == y
                  for x, y in zip(partiel[o[0]], complet[o[0]])]
        assert all(egales), f"{o[0]} : variables différentes sur le sous-ensemble"
