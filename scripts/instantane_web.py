"""Instantané des vraies données pour la version en ligne de Filature (GitHub Pages, sans serveur).

Lit l'API de la plateforme lancée en local et écrit web/public/donnees/ : santé, files d'alertes (chaque
filtre de l'interface), dossiers. Les comptes inventés par les tests et le banc de charge (banques 30001 et
30002, cf. ingestion/internal/scenarios) sont écartés : le visiteur ne voit que les cas AMLworld, plus les
dossiers ouverts par les essais Chaos enregistrés (docs/demo/capturer_chaos.js écrit chaos.json avant).

Usage (plateforme lancée) : python scripts/instantane_web.py
"""

import json
import os
import shutil
import subprocess
import sys
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

API = os.environ.get("API", "http://localhost:18100")
SORTIE = Path(__file__).resolve().parents[1] / "web" / "public" / "donnees"
REGLES = [None, "fan_in", "fan_out", "cycle", "especes_fractionnees"]
LIMITE = 200  # comme l'interface


def lire(chemin: str):
    with urllib.request.urlopen(API + chemin, timeout=60) as r:
        return json.load(r)


def ecrire(nom: str, donnees) -> None:
    f = SORTIE / nom
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_text(json.dumps(donnees, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")


def compte_de_test(iban: str) -> bool:
    # AMLworld : code banque = longueur de l'identifiant puis zéros (« 30000 », « 50000 »…), jamais 30001/30002.
    return iban[4:9] in ("30001", "30002")


def operations_amlworld() -> int:
    """Opérations hors comptes de test (l'API compte tout le grand livre, banc de charge compris)."""
    sql = ("SELECT count(*) FROM transactions WHERE substr(coalesce(debtor_account, creditor_account), 5, 5) NOT IN ('30001', '30002') "
           "AND substr(coalesce(creditor_account, debtor_account), 5, 5) NOT IN ('30001', '30002')")
    r = subprocess.run(["docker", "compose", "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "grand_livre", "-tAc", sql],
                       capture_output=True, text=True, check=True, cwd=SORTIE.parents[2])
    return int(r.stdout)


def main() -> None:
    chaos = SORTIE / "chaos.json"
    garde = chaos.read_bytes() if chaos.exists() else None
    shutil.rmtree(SORTIE / "alertes", ignore_errors=True)  # dossiers gardés : un arrêt en cours de route se reprend

    ids: set[str] = set()
    for statut in ("a_traiter", "traitees"):
        for tri in ("score", "recentes"):
            for regle in REGLES:
                q = f"/alertes?statut={statut}&tri={tri}&limite=10000" + (f"&regle={regle}" if regle else "")
                liste = [a for a in lire(q) if not compte_de_test(a["entite"])][:LIMITE]
                ecrire(f"alertes/{statut}-{tri}-{regle or 'toutes'}.json", liste)
                ids |= {a["alert_id"] for a in liste}

    # Dossiers ouverts depuis la console Chaos rejouée (liens « Fan-in sur … »).
    if garde:
        for e in json.loads(garde)["essais"]:
            ids |= {v["alerte"] for v in e["verifs"] if v.get("alerte")}
    for alert_id in sorted(ids):
        if not (SORTIE / f"dossiers/{alert_id}.json").exists():
            ecrire(f"dossiers/{alert_id}.json", lire(f"/alertes/{alert_id}"))
    for f in (SORTIE / "dossiers").glob("*.json"):  # dossiers d'un instantané précédent, plus listés
        if f.stem not in ids:
            f.unlink()

    toutes = [a for a in lire("/alertes?statut=toutes&limite=10000") if not compte_de_test(a["entite"])]
    sante = lire("/sante")
    n = operations_amlworld()
    sante |= {"operations": n, "graphe_operations": n if sante["graphe_en_retard"] == 0 else None, "alertes": len(toutes), "a_traiter": sum(a["statut"] == "a_traiter" for a in toutes), "debit": 0,
              "instantane": datetime.now(timezone.utc).isoformat(timespec="seconds")}
    ecrire("sante.json", sante)

    taille = sum(f.stat().st_size for f in SORTIE.rglob("*.json"))
    print(f"{len(ids)} dossiers, {len(toutes)} alertes AMLworld, {taille / 1e6:.1f} Mo dans {SORTIE}")
    if len(toutes) == 0 or len(ids) == 0:
        sys.exit("instantané vide : la plateforme a-t-elle rejoué AMLworld ?")


if __name__ == "__main__":
    main()
