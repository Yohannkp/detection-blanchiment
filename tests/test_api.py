"""API de l'analyste, sur un vrai Postgres (schéma db/*.sql) avec un petit jeu de données.

Tourne si TEST_DATABASE_URL pointe vers une base VIDE où l'on est superutilisateur (la CI fournit un
conteneur neuf) ; l'API, elle, se connecte avec son rôle restreint `api`. Sous Linux (psycopg asynchrone
n'accepte pas la boucle d'événements par défaut de Windows) : la CI, ou un conteneur python:3.13.
"""

import os
import sys
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

import uuid

import psycopg
import pytest

RACINE = Path(__file__).resolve().parents[1]
ALERTE = f"cycle-test-{uuid.uuid4().hex[:8]}"  # propre à chaque exécution : les tests se rejouent sur la même base
URL = os.environ.get("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not URL, reason="TEST_DATABASE_URL absent : tests de l'API ignorés")

def url_role(url: str, role: str) -> str:
    u = urlsplit(url)
    return urlunsplit(u._replace(netloc=f"{role}:{role}@{u.hostname}:{u.port}"))


@pytest.fixture(scope="module")
def client():
    with psycopg.connect(URL, autocommit=True) as c:
        if not c.execute("SELECT to_regclass('decisions')").fetchone()[0]:
            for f in sorted((RACINE / "db").glob("*.sql")):
                c.execute(f.read_text(encoding="utf-8"))
        # Une alerte de cycle sur trois opérations, et son score. Opérations et écritures dans UNE transaction :
        # le grand livre vérifie l'équilibre au COMMIT. Jeu créé une seule fois (rejouable sur la même base).
        deja = c.execute("SELECT 1 FROM transactions WHERE transaction_id = 't1'").fetchone()
        with c.transaction():
          if deja:
            raise psycopg.Rollback()
          c.execute("""
          INSERT INTO transactions VALUES
            ('t1', '2022-09-10 08:00+00', 'transfer', 'A', 'B', 100000, 'EUR', 'ach', '\x01', 0, 1),
            ('t2', '2022-09-10 09:00+00', 'transfer', 'B', 'C',  98000, 'EUR', 'ach', '\x02', 0, 2),
            ('t3', '2022-09-10 10:00+00', 'transfer', 'C', 'A',  96000, 'EUR', 'ach', '\x03', 0, 3)
          ON CONFLICT DO NOTHING""")
          c.execute("""INSERT INTO ledger_entries (transaction_id, account, currency, amount_minor)
                     SELECT transaction_id, debtor_account, currency, -amount_minor FROM transactions WHERE transaction_id IN ('t1','t2','t3')
                     UNION ALL SELECT transaction_id, creditor_account, currency, amount_minor FROM transactions WHERE transaction_id IN ('t1','t2','t3')
                     ON CONFLICT DO NOTHING""")
        c.execute("INSERT INTO alertes VALUES (%s, 'cycle', 'A', '2022-09-08', 't3', now())", (ALERTE,))
        c.execute("""INSERT INTO alertes_operations (alert_id, transaction_id, signalee_le)
                     SELECT %s, t, '2022-09-10 10:00' FROM unnest(ARRAY['t1', 't2', 't3']) t""", (ALERTE,))
        c.execute("""INSERT INTO alertes_scores VALUES (%s, 3, 0.91, 't3', '[{"variable": "format", "valeur": "ach", "contribution": 2.1}]',
                                                     'detection-blanchiment-lgbm-service v1', now())""", (ALERTE,))
    os.environ["DATABASE_URL"] = url_role(URL, "api")
    os.environ["METRIQUES_INGESTION"] = "http://127.0.0.1:1/metrics"  # injoignable : dlq vaut alors None
    sys.path.insert(0, str(RACINE / "api"))
    from fastapi.testclient import TestClient
    import app as module
    with TestClient(module.app) as c:
        yield c


def test_sante(client):
    s = client.get("/sante").json()
    assert (s["devises_en_ecart"], s["ecritures_manquantes"]) == (0, 0) and s["operations"] >= 3 and s["dlq"] is None


def test_file_et_dossier(client):
    file = client.get("/alertes").json()
    a = next(x for x in file if x["alert_id"] == ALERTE)
    assert (a["n"], a["devises"], a["montant"], a["statut"]) == (3, 1, 294000, "a_traiter") and a["score"] == pytest.approx(0.91)
    d = client.get(f"/alertes/{ALERTE}").json()
    assert [p["transaction_id"] for p in d["preuves"]] == ["t1", "t2", "t3"]
    assert [e["quoi"] for e in d["audit"]][0] == "ouverture"
    assert client.get("/alertes/inconnue").status_code == 404


def test_decision_ajout_seul_et_statut(client):
    assert client.post(f"/alertes/{ALERTE}/decisions", json={"decision": "escalade", "justification": "trop court", "analyste": "y.a"}).status_code == 422
    assert client.post(f"/alertes/{ALERTE}/decisions", json={"decision": "classer", "justification": "x" * 30, "analyste": "y.a"}).status_code == 422
    r = client.post(f"/alertes/{ALERTE}/decisions",
                    json={"decision": "escalade", "justification": "Montage circulaire A → B → C → A en deux heures.", "analyste": "y.a"})
    assert r.status_code == 201 and r.json()["decision"] == "escalade"
    assert all(x["alert_id"] != ALERTE for x in client.get("/alertes").json())  # n'est plus « à traiter »
    assert any(x["alert_id"] == ALERTE and x["statut"] == "escalade" for x in client.get("/alertes?statut=traitees").json())
    # Le rôle api n'a pas le droit de modifier une décision ; même un superutilisateur bute sur le déclencheur.
    with psycopg.connect(url_role(URL, "api")) as c, pytest.raises(psycopg.errors.InsufficientPrivilege):
        c.execute("UPDATE decisions SET decision = 'sans_suite'")
    with psycopg.connect(URL) as c, pytest.raises(psycopg.errors.InsufficientPrivilege, match="ajout seul"):
        c.execute("UPDATE decisions SET decision = 'sans_suite'")


def test_contre_csrf(client):
    corps = '{"decision": "sans_suite", "justification": "Tentative depuis un site tiers, à refuser.", "analyste": "x"}'
    # POST « simple » sans Content-Type JSON (ce qu'une page tierce peut envoyer sans requête préalable CORS)
    assert client.post(f"/alertes/{ALERTE}/decisions", content=corps, headers={"content-type": "text/plain"}).status_code == 415
    # JSON depuis une origine non autorisée
    assert client.post(f"/alertes/{ALERTE}/decisions", content=corps,
                       headers={"content-type": "application/json", "origin": "https://site-tiers.example"}).status_code == 403


def test_le_role_api_ne_voit_pas_la_verite(client):
    with psycopg.connect(url_role(URL, "api")) as c, pytest.raises(psycopg.errors.InsufficientPrivilege):
        c.execute("SELECT count(*) FROM evaluation.verite_terrain")
