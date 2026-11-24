"""Scoreur en direct : donne un score (0-1) et ses 3 raisons principales à chaque alerte.

Boucle : alertes dont le nombre de preuves n'a pas encore été scoré → opérations de tous les comptes
concernés, lues dans le grand livre par DuckDB (ml/grand_livre.py) → variables (ml/variables.py),
calculées exactement comme pour l'entraînement du modèle de SERVICE, lui-même appris sur le grand livre →
probabilité de chaque preuve → score de l'alerte = probabilité maximale, raisons = contributions SHAP
(calculées par LightGBM, pred_contrib) de la preuve la plus suspecte.

Exactitude : les variables d'une opération ne dépendent que des opérations de son émetteur et de son
bénéficiaire ; charger tous les comptes concernés suffit (tests/test_variables.py le vérifie).

Usage : python ml/scoreur.py   (DATABASE_URL, MODELE=dossier contenant modele.txt et score.json)
"""

from __future__ import annotations

import json
import logging
import os
import sys
import time
from pathlib import Path

import duckdb
import lightgbm as lgb
import numpy as np
import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parent))
from grand_livre import attacher, charger_comptes_de  # noqa: E402
from variables import construire  # noqa: E402

log = logging.getLogger("scoreur")

class Modele:
    def __init__(self, dossier: Path):
        self.booster = lgb.Booster(model_file=str(dossier / "modele.txt"))
        meta = json.loads((dossier / "score.json").read_text(encoding="utf-8"))
        self.variables, self.categories, self.version = meta["variables"], meta["categories"], meta.get("version", "?")

    def matrice(self, df: pd.DataFrame) -> pd.DataFrame:
        X = df[self.variables].copy()
        for c in X.columns:
            if c in self.categories:
                connues = self.categories[c]
                X[c] = pd.Categorical(X[c].where(X[c].isin(connues)), categories=connues)  # inconnue → manquante
            else:
                X[c] = X[c].astype("float32")
        return X

    def scorer(self, df: pd.DataFrame) -> tuple[np.ndarray, np.ndarray]:
        X = self.matrice(df)
        return self.booster.predict(X), self.booster.predict(X, pred_contrib=True)[:, :-1]


# Alertes à (re)scorer : celles dont le nombre de preuves PRÉSENTES au grand livre n'a pas encore de
# score. Une alerte sans aucune preuve retrouvée est ignorée (sinon elle reviendrait à chaque tour et
# bloquerait la file) ; une preuve qui arrive plus tard change le compte, donc déclenche un nouveau score.
A_SCORER = """
WITH preuves AS (
  SELECT ao.alert_id, list(DISTINCT ao.transaction_id) AS ids, count(DISTINCT ao.transaction_id) AS n
  FROM pg.public.alertes_operations ao JOIN pg.public.transactions t USING (transaction_id)
  GROUP BY ao.alert_id)
SELECT p.alert_id, p.ids FROM preuves p
ANTI JOIN pg.public.alertes_scores s ON s.alert_id = p.alert_id AND s.preuves = p.n AND s.modele = ?
ORDER BY p.alert_id
LIMIT ?
"""


def un_tour(con: duckdb.DuckDBPyConnection, modele: Modele, lot: int = 200) -> int:
    alertes = con.execute(A_SCORER, [modele.version, lot]).fetchall()
    if not alertes:
        return 0
    ids = sorted({tx for _, preuves in alertes for tx in preuves})
    charger_comptes_de(con, ids)
    construire(con)
    df = con.execute("SELECT * FROM variables WHERE transaction_id IN (SELECT transaction_id FROM _ids)").df()
    proba, contrib = modele.scorer(df)
    par_tx = {tx: i for i, tx in enumerate(df["transaction_id"])}
    lignes = []
    for alert_id, preuves in alertes:
        idx = [par_tx[tx] for tx in preuves]  # toutes présentes : la requête ne garde que celles du grand livre
        i = max(idx, key=lambda k: proba[k])
        ordre = np.argsort(-contrib[i])[:3]  # les 3 variables qui poussent le plus vers « blanchiment »
        raisons = [{"variable": modele.variables[j], "valeur": _json(df.iloc[i][modele.variables[j]]),
                    "contribution": round(float(contrib[i][j]), 4)} for j in ordre]
        lignes.append((alert_id, len(idx), float(proba[i]), df.iloc[i]["transaction_id"],
                       json.dumps(raisons, ensure_ascii=False), modele.version))
    con.executemany("""INSERT INTO pg.public.alertes_scores (alert_id, preuves, score, transaction_id, raisons, modele)
                       VALUES (?, ?, ?, ?, ?::JSON, ?) ON CONFLICT DO NOTHING""", lignes)
    return len(lignes)


def _json(v):
    if v is None or (isinstance(v, float) and np.isnan(v)):
        return None
    return v.item() if hasattr(v, "item") else v


def main() -> None:
    logging.basicConfig(level=logging.INFO, format='{"niveau":"%(levelname)s","message":"%(message)s"}')
    modele = Modele(Path(os.environ.get("MODELE", "modeles/production")))
    url = os.environ.get("DATABASE_URL", "postgresql://scoreur:scoreur@localhost:15433/grand_livre")
    attente = float(os.environ.get("INTERVALLE", "2"))
    log.info("scoreur démarré, modèle %s", modele.version)
    con = None
    while True:
        try:
            con = con or attacher(url)
            n = un_tour(con, modele)
            if n:
                log.info("%d alerte(s) scorée(s)", n)
                continue
        except Exception as e:  # panne Postgres : on se reconnecte au tour suivant, rien n'est perdu
            log.warning("tour échoué, nouvel essai : %s", e)
            if con is not None:
                con.close()
            con = None
        time.sleep(attente)


if __name__ == "__main__":
    main()
