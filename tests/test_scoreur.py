"""Scoreur : mêmes colonnes et mêmes catégories qu'à l'entraînement, raisons cohérentes avec le score."""

import json
import sys
from datetime import timedelta
from pathlib import Path

import duckdb
import lightgbm as lgb
import numpy as np
import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "ml"))
sys.path.insert(0, str(Path(__file__).resolve().parent))
from scoreur import Modele  # noqa: E402
from test_variables import T0, aleatoires  # noqa: E402
from variables import CATEGORIELLES, construire  # noqa: E402


def variables(ops):
    c = duckdb.connect()
    c.execute("""CREATE TABLE ops (transaction_id VARCHAR, ts TIMESTAMP, src VARCHAR, dst VARCHAR,
                 montant DOUBLE, devise VARCHAR, devise_recue VARCHAR, format VARCHAR)""")
    c.executemany("INSERT INTO ops VALUES (?, ?, ?, ?, ?, ?, ?, ?)", ops)
    noms = construire(c)
    return c.sql("SELECT * FROM variables").df(), noms


def test_modele_exporte_puis_relu(tmp_path):
    df, noms = variables(aleatoires(500, graine=5, debut=T0))
    categories = {c: sorted(df[c].unique().tolist()) for c in CATEGORIELLES}
    X = df[noms].copy()
    for c in noms:
        X[c] = pd.Categorical(X[c], categories=categories[c]) if c in CATEGORIELLES else X[c].astype("float32")
    y = (df["log_montant"] > df["log_montant"].median()).to_numpy()  # cible apprenable
    m = lgb.train({"objective": "binary", "verbose": -1, "min_data_in_leaf": 5}, lgb.Dataset(X, y), 20)
    m.save_model(str(tmp_path / "modele.txt"))
    (tmp_path / "score.json").write_text(json.dumps({"version": "test", "variables": noms, "categories": categories}))

    modele = Modele(tmp_path)
    nouvelles, _ = variables(aleatoires(30, graine=6, debut=T0 + timedelta(days=1)))
    nouvelles.loc[0, "format"] = "inconnu"  # moyen de paiement jamais vu : traité comme manquant, sans erreur
    proba, contrib = modele.scorer(nouvelles)
    assert proba.shape == (30,) and contrib.shape == (30, len(noms))
    assert np.all((proba >= 0) & (proba <= 1))
    # Les contributions SHAP (+ biais) redonnent la log-cote prédite.
    biais = m.predict(modele.matrice(nouvelles), pred_contrib=True)[:, -1]
    assert np.allclose(contrib.sum(axis=1) + biais, np.log(proba / (1 - proba)), atol=1e-6)
