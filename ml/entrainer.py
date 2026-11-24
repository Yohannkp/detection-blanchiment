"""Entraîne LightGBM sur le jeu AMLworld HI-Small complet, dans le protocole de l'article
(Altman et al., NeurIPS 2023) : découpage temporel 60/20/20 des opérations triées par horodatage,
F1 de la classe illicite au seuil 0,5. Suivi dans MLflow ; le meilleur modèle est enregistré avec
l'alias « production » ; importance SHAP des variables en artefact.

Deux modèles, même code :
  python ml/entrainer.py data/variables_hi_small.parquet
      modèle de RÉFÉRENCE (jeu complet), comparable à l'article ; non utilisé en direct ;
  python ml/entrainer.py data/variables_grand_livre.parquet --nom detection-blanchiment-lgbm-service --exporter modeles/production
      modèle de SERVICE, appris sur la vue du grand livre (celle du scoreur), exporté pour le scoreur.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile
from pathlib import Path

import duckdb
import lightgbm as lgb
import mlflow
import numpy as np
import pandas as pd
import shap
from sklearn.metrics import average_precision_score, f1_score, precision_recall_curve, precision_score, recall_score

sys.path.insert(0, str(Path(__file__).resolve().parent))
from variables import CATEGORIELLES  # noqa: E402

PARAMS = {
    "objective": "binary", "learning_rate": 0.05, "num_leaves": 63, "min_child_samples": 100,
    "feature_fraction": 0.8, "bagging_fraction": 0.8, "bagging_freq": 1, "verbose": -1,
    "metric": "average_precision",  # arrêt anticipé sur la PR-AUC de validation (classe très rare)
    # Régularisation des feuilles : sans elle, avec 0,08 % de cas positifs, des feuilles minuscules prennent
    # des valeurs extrêmes (constaté : contributions SHAP de 67 en log-cote).
    "min_sum_hessian_in_leaf": 1.0, "lambda_l2": 10.0,
}


def charger(parquet: Path) -> tuple[pd.DataFrame, np.ndarray, dict[str, list[str]]]:
    """Variables (triées par temps) en float32, catégories figées (elles servent aussi au score en direct)."""
    df = duckdb.sql(f"SELECT * FROM '{parquet.as_posix()}' ORDER BY ts, ligne").df()
    y = df.pop("y").to_numpy(dtype=bool)
    df = df.drop(columns=["ligne", "ts", "transaction_id"])
    categories = {c: sorted(df[c].dropna().unique().tolist()) for c in CATEGORIELLES}
    for c in df.columns:
        if c in CATEGORIELLES:
            df[c] = pd.Categorical(df[c], categories=categories[c])
        else:
            df[c] = df[c].astype("float32")
    return df, y, categories


def decouper(n: int) -> tuple[slice, slice, slice]:
    a, b = int(0.6 * n), int(0.8 * n)
    return slice(0, a), slice(a, b), slice(b, n)


def mesures(y: np.ndarray, p: np.ndarray, seuil: float) -> dict[str, float]:
    yp = p >= seuil
    return {"f1": f1_score(y, yp), "precision": precision_score(y, yp, zero_division=0),
            "rappel": recall_score(y, yp), "pr_auc": average_precision_score(y, p)}


def meilleur_seuil(y: np.ndarray, p: np.ndarray) -> float:
    prec, rap, seuils = precision_recall_curve(y, p)
    f1 = 2 * prec * rap / np.maximum(prec + rap, 1e-12)
    return float(seuils[np.argmax(f1[:-1])])


def entrainer(X, y, tr, va, poids: float, graine: int) -> lgb.Booster:
    params = {**PARAMS, "scale_pos_weight": poids, "seed": graine}
    return lgb.train(
        params, lgb.Dataset(X[tr], y[tr]), num_boost_round=3000,
        valid_sets=[lgb.Dataset(X[va], y[va])],
        callbacks=[lgb.early_stopping(100, verbose=False)],
    )


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("parquet", type=Path)
    ap.add_argument("--graines", type=int, default=3)
    ap.add_argument("--validation-seulement", action="store_true",
                    help="mise au point : n'affiche ni ne calcule rien sur le test (décisions prises sur la validation)")
    ap.add_argument("--nom", default="detection-blanchiment-lgbm", help="nom du modèle dans le registre MLflow")
    ap.add_argument("--exporter", type=Path, help="dossier où exporter le modèle publié pour le scoreur")
    ap.add_argument("--poids", type=float, nargs="+", default=[1.0, 3.0, 10.0],
                    help="valeurs de scale_pos_weight essayées (choix sur la validation)")
    a = ap.parse_args()

    X, y, categories = charger(a.parquet)
    tr, va, te = decouper(len(y))
    debut_test = str(duckdb.sql(f"SELECT ts FROM '{a.parquet.as_posix()}' ORDER BY ts, ligne LIMIT 1 OFFSET {te.start}").fetchall()[0][0])
    debut_val = str(duckdb.sql(f"SELECT ts FROM '{a.parquet.as_posix()}' ORDER BY ts, ligne LIMIT 1 OFFSET {va.start}").fetchall()[0][0])
    print(f"début de la validation : {debut_val} ; début du test : {debut_test}")
    print(f"{len(y)} opérations, {y.sum()} illicites ; train {y[tr].mean():.4%}, val {y[va].mean():.4%}, test {y[te].mean():.4%}")

    mlflow.set_tracking_uri("sqlite:///mlflow.db")
    mlflow.set_experiment("detection-blanchiment")

    # 1. Poids de la classe illicite choisi sur la VALIDATION (F1 au seuil 0,5), graine 0.
    choix = {}
    for poids in a.poids:
        with mlflow.start_run(run_name=f"choix-poids-{poids:g}"):
            m = entrainer(X, y, tr, va, poids, 0)
            r = mesures(y[va], m.predict(X[va], num_iteration=m.best_iteration), 0.5)
            mlflow.log_params({**PARAMS, "scale_pos_weight": poids, "graine": 0, "iterations": m.best_iteration})
            mlflow.log_metrics({f"val_{k}": v for k, v in r.items()})
            choix[poids] = r["f1"]
            print(f"poids {poids:g} : F1 validation {r['f1']:.4f} (PR-AUC {r['pr_auc']:.4f}, {m.best_iteration} itérations)")
    poids = max(choix, key=choix.get)
    if a.validation_seulement:
        print(f"poids retenu sur la validation : {poids:g} (test non calculé)")
        return

    # 2. Plusieurs graines avec ce poids ; mesures sur le TEST, jamais vu jusqu'ici.
    resultats, meilleur = [], None
    for graine in range(a.graines):
        with mlflow.start_run(run_name=f"final-graine-{graine}") as run:
            m = entrainer(X, y, tr, va, poids, graine)
            p_va = m.predict(X[va], num_iteration=m.best_iteration)
            p_te = m.predict(X[te], num_iteration=m.best_iteration)
            seuil = meilleur_seuil(y[va], p_va)
            r05, rs = mesures(y[te], p_te, 0.5), mesures(y[te], p_te, seuil)
            mlflow.log_params({**PARAMS, "scale_pos_weight": poids, "graine": graine,
                               "iterations": m.best_iteration, "seuil_validation": seuil})
            mlflow.log_metrics({**{f"test_{k}_seuil05": v for k, v in r05.items()},
                                **{f"test_{k}_seuil_val": v for k, v in rs.items()}})
            resultats.append({"graine": graine, "seuil": seuil, **{f"{k}@0.5": v for k, v in r05.items()},
                              **{f"{k}@seuil_val": v for k, v in rs.items()}})
            rv = mesures(y[va], p_va, 0.5)
            mlflow.log_metrics({f"val_{k}_seuil05": v for k, v in rv.items()})
            print(f"graine {graine} : validation F1@0,5 {rv['f1']:.4f} ; test F1@0,5 {r05['f1']:.4f}, F1@seuil val ({seuil:.3f}) {rs['f1']:.4f}, PR-AUC {r05['pr_auc']:.4f}")
            # Le modèle publié est choisi sur la VALIDATION : le test ne sert qu'à mesurer.
            if meilleur is None or rv["f1"] > meilleur[0]:
                meilleur = (rv["f1"], m, run.info.run_id, seuil, p_te)

    res = pd.DataFrame(resultats)
    resume = {c: (res[c].mean(), res[c].std()) for c in res.columns if c not in ("graine",)}
    print("\nTest (moyenne ± écart-type sur", a.graines, "graines) :")
    for c, (mu, sd) in resume.items():
        print(f"  {c:22s} {mu:.4f} ± {sd:.4f}")

    # 3. Meilleur modèle : enregistré avec ses métadonnées de score, importance SHAP en artefact.
    _, m, run_id, seuil, p_te = meilleur
    with mlflow.start_run(run_id=run_id):
        echantillon = X[te].sample(20_000, random_state=0)
        valeurs = shap.TreeExplainer(m).shap_values(echantillon)
        importance = pd.Series(np.abs(valeurs).mean(axis=0), index=X.columns).sort_values(ascending=False)
        with tempfile.TemporaryDirectory() as d:
            (Path(d) / "importance_shap.csv").write_text(importance.to_csv(header=["mean_abs_shap"]), encoding="utf-8")
            (Path(d) / "score.json").write_text(json.dumps({
                "variables": list(X.columns), "categories": categories, "seuil": 0.5,
                "seuil_validation": seuil, "scale_pos_weight": poids}, ensure_ascii=False, indent=2), encoding="utf-8")
            res.to_csv(Path(d) / "resultats_graines.csv", index=False)
            mlflow.log_artifacts(d)
        info = mlflow.lightgbm.log_model(m, name="modele", registered_model_name=a.nom)
        mlflow.MlflowClient().set_registered_model_alias(a.nom, "production", info.registered_model_version)
    if a.exporter:  # pour le scoreur en direct, sans dépendre des chemins d'artefacts MLflow
        a.exporter.mkdir(parents=True, exist_ok=True)
        m.save_model(str(a.exporter / "modele.txt"), num_iteration=m.best_iteration)
        (a.exporter / "score.json").write_text(json.dumps({
            "version": f"{a.nom} v{info.registered_model_version}", "variables": list(X.columns),
            "categories": categories, "seuil": 0.5, "debut_test": debut_test}, ensure_ascii=False, indent=2), encoding="utf-8")
    print("\nImportance SHAP (moyenne |SHAP| sur 20 000 opérations du test) :")
    print(importance.head(15).to_string())
    print(f"\nModèle {a.nom} v{info.registered_model_version} → alias production (run {run_id})")


if __name__ == "__main__":
    main()
