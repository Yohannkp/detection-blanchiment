"""API du poste de l'analyste (Filature) : file d'alertes, dossier complet, santé de la plateforme,
décisions de l'analyste (ajout seul) et mises à jour en direct par WebSocket.

Lit Postgres avec le rôle `api` : il voit ce qui sert à instruire un dossier, n'écrit que des décisions,
et n'a aucun accès à la vérité terrain.

Lancer : uvicorn app:app --port 18100   (DATABASE_URL, METRIQUES_INGESTION)
"""

from __future__ import annotations

import asyncio
import os
import re
import time
from contextlib import asynccontextmanager
from datetime import datetime
from typing import Literal

import httpx
from fastapi import Depends, FastAPI, HTTPException, Query, Request, WebSocket, WebSocketDisconnect
from fastapi.middleware.cors import CORSMiddleware
from psycopg.rows import dict_row
from psycopg_pool import AsyncConnectionPool
from pydantic import BaseModel, Field, field_validator

DATABASE_URL = os.environ.get("DATABASE_URL", "postgresql://api:api@localhost:15433/grand_livre")
METRIQUES_INGESTION = os.environ.get("METRIQUES_INGESTION", "http://localhost:18090/metrics")
MODELE_SERVICE = os.environ.get("MODELE_SERVICE", "detection-blanchiment-lgbm-service")
ORIGINES = os.environ.get("ORIGINES", "http://localhost:3100").split(",")

pool = AsyncConnectionPool(DATABASE_URL, open=False, min_size=1, max_size=8, kwargs={"row_factory": dict_row})


@asynccontextmanager
async def cycle_de_vie(_: FastAPI):
    await pool.open()
    yield
    await pool.close()


app = FastAPI(title="Filature — API de l'analyste", lifespan=cycle_de_vie)
app.add_middleware(CORSMiddleware, allow_origins=ORIGINES, allow_methods=["GET", "POST"], allow_headers=["content-type"])


async def requete(sql: str, *params) -> list[dict]:
    async with pool.connection() as con:
        cur = await con.execute(sql, params)
        return await cur.fetchall()


# Dernier score de chaque alerte par le modèle de service (preuves les plus nombreuses, puis le plus récent).
_DERNIER_SCORE = """
  SELECT DISTINCT ON (alert_id) alert_id, score, transaction_id AS tx_suspecte, raisons, modele, calcule_le
  FROM alertes_scores WHERE modele LIKE %s || ' %%'
  ORDER BY alert_id, preuves DESC, calcule_le DESC"""

_STATUT = """
  SELECT DISTINCT ON (alert_id) alert_id, decision, decidee_le FROM decisions ORDER BY alert_id, decidee_le DESC, decision_id DESC"""


_cache_sante: tuple[float, dict] | None = None
_verrou_sante = asyncio.Lock()


@app.get("/sante")
async def sante() -> dict:
    """Calculée au plus une fois toutes les 2 s, quel que soit le nombre d'onglets ouverts."""
    global _cache_sante
    async with _verrou_sante:
        if _cache_sante is None or time.monotonic() - _cache_sante[0] > 2:
            _cache_sante = (time.monotonic(), await _calculer_sante())
        return _cache_sante[1]


async def _calculer_sante() -> dict:
    r = (await requete(f"""
        -- Réconciliation : devises déséquilibrées et écritures manquantes (deux compteurs, sans unité :
        -- on n'additionne pas des centimes, des yens et des satoshis).
        SELECT (SELECT count(*) FILTER (WHERE ecart_minor <> 0) FROM v_reconciliation) AS devises_en_ecart,
               (SELECT coalesce(sum(abs(ecritures_manquantes)), 0)::bigint FROM v_reconciliation) AS ecritures_manquantes,
               (SELECT count(*) FROM transactions) AS operations,
               (SELECT count(*) FROM transactions WHERE ingested_at > now() - interval '60 seconds')::float / 60 AS debit,
               (SELECT count(*) FROM alertes) AS alertes,
               (SELECT count(*) FROM alertes a WHERE NOT EXISTS (SELECT 1 FROM decisions d WHERE d.alert_id = a.alert_id)) AS a_traiter,
               (SELECT count(*) FROM outbox WHERE published_at IS NULL) AS outbox_en_attente,
               (SELECT round(1000 * percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM a.creee_le - t.ingested_at)))::float
                  FROM (SELECT * FROM alertes WHERE creee_le > now() - interval '10 minutes') a  -- direct seulement : un rejeu accéléré mesure la file d'attente
                  JOIN transactions t ON t.transaction_id = a.declencheur) AS latence_p50_ms
    """))[0]
    r["dlq"] = await _dlq()
    return r


async def _dlq() -> int | None:
    """Messages envoyés en DLQ depuis le démarrage de l'ingestion (compteurs Prometheus) ; None si injoignable."""
    try:
        async with httpx.AsyncClient(timeout=1.5) as c:
            texte = (await c.get(METRIQUES_INGESTION)).text
        return sum(int(m) for m in re.findall(r"^ingestion_dlq_total\{[^}]*\} (\d+)$", texte, re.M))
    except (httpx.HTTPError, ValueError):
        return None


@app.get("/alertes")
async def alertes(
    regle: Literal["fan_in", "fan_out", "cycle", "especes_fractionnees"] | None = None,
    statut: Literal["a_traiter", "traitees", "toutes"] = "a_traiter",
    tri: Literal["score", "recentes"] = "score",
    limite: int = Query(60, ge=1, le=500),
) -> list[dict]:
    return await requete(f"""
        WITH s AS ({_DERNIER_SCORE}), st AS ({_STATUT}),
        -- Un total n'a de sens que dans une seule devise : sinon montant et devise sont NULL, et devises > 1.
        p AS (SELECT ao.alert_id, count(*) AS n, count(DISTINCT t.currency) AS devises,
                     CASE WHEN count(DISTINCT t.currency) = 1 THEN sum(t.amount_minor)::bigint END AS montant,
                     CASE WHEN count(DISTINCT t.currency) = 1 THEN max(t.currency) END AS devise,
                     min(t.booked_at) AS debut, max(t.booked_at) AS fin
              FROM (SELECT DISTINCT alert_id, transaction_id FROM alertes_operations) ao
              JOIN transactions t ON t.transaction_id = ao.transaction_id GROUP BY 1)
        SELECT a.alert_id, a.regle, a.entite, a.ouverte_le, a.creee_le, s.score, p.n, p.devises, p.montant, p.devise, p.debut, p.fin,
               coalesce(st.decision, 'a_traiter') AS statut
        FROM alertes a JOIN p USING (alert_id) LEFT JOIN s USING (alert_id) LEFT JOIN st USING (alert_id)
        WHERE (%s::text IS NULL OR a.regle = %s)
          AND (%s = 'toutes' OR (%s = 'a_traiter') = (st.decision IS NULL))
        ORDER BY CASE WHEN %s = 'score' THEN s.score END DESC NULLS LAST, a.creee_le DESC
        LIMIT %s""", MODELE_SERVICE, regle, regle, statut, statut, tri, limite)


@app.get("/alertes/{alert_id}")
async def dossier(alert_id: str) -> dict:
    a = await requete("SELECT * FROM alertes WHERE alert_id = %s", alert_id)
    if not a:
        raise HTTPException(404, "alerte inconnue")
    preuves = await requete("""
        SELECT t.transaction_id, t.booked_at, t.debtor_account AS src, t.creditor_account AS dst, t.amount_minor,
               t.currency, t.payment_format, min(ao.signalee_le) AS signalee_le, min(ao.ajoutee_le) AS ajoutee_le
        FROM alertes_operations ao JOIN transactions t ON t.transaction_id = ao.transaction_id
        WHERE ao.alert_id = %s GROUP BY 1, 2, 3, 4, 5, 6, 7 ORDER BY t.booked_at""", alert_id)
    # Voisinage : autres opérations des mêmes comptes autour de la période (contexte pour l'analyste).
    voisinage = await requete("""
        -- La caisse interne est commune à tous les clients : elle n'apporte aucun voisinage. Les opérations les
        -- plus proches de la période du dossier passent en premier.
        WITH p AS (SELECT t.* FROM alertes_operations ao JOIN transactions t ON t.transaction_id = ao.transaction_id
                   WHERE ao.alert_id = %s),
             c AS (SELECT debtor_account AS compte FROM p UNION SELECT creditor_account FROM p),
             b AS (SELECT min(booked_at) AS debut, max(booked_at) AS fin FROM p)
        SELECT t.transaction_id, t.booked_at, t.debtor_account AS src, t.creditor_account AS dst, t.amount_minor, t.currency
        FROM transactions t, b
        WHERE (t.debtor_account IN (SELECT compte FROM c WHERE compte NOT LIKE 'INTERNE:%%')
               OR t.creditor_account IN (SELECT compte FROM c WHERE compte NOT LIKE 'INTERNE:%%'))
          AND t.booked_at BETWEEN b.debut - interval '3 days' AND b.fin + interval '1 day'
          AND t.transaction_id NOT IN (SELECT transaction_id FROM p)
        ORDER BY CASE WHEN t.booked_at < b.debut THEN b.debut - t.booked_at WHEN t.booked_at > b.fin THEN t.booked_at - b.fin
                      ELSE interval '0' END, t.booked_at
        LIMIT 40""", alert_id)
    score = await requete(f"SELECT * FROM ({_DERNIER_SCORE}) s WHERE alert_id = %s", MODELE_SERVICE, alert_id)
    decisions = await requete("SELECT * FROM decisions WHERE alert_id = %s ORDER BY decidee_le, decision_id", alert_id)
    scores = await requete("""SELECT preuves, score, modele, calcule_le FROM alertes_scores
                              WHERE alert_id = %s AND modele LIKE %s || ' %%' ORDER BY calcule_le""", alert_id, MODELE_SERVICE)
    return {"alerte": a[0], "score": score[0] if score else None, "preuves": preuves, "voisinage": voisinage,
            "decisions": decisions, "audit": _audit(a[0], preuves, scores, decisions)}


def _audit(a: dict, preuves: list[dict], scores: list[dict], decisions: list[dict]) -> list[dict]:
    """Piste d'audit lisible : ouverture, preuves ajoutées, scores, décisions, dans l'ordre réel des faits."""
    ev = [{"quand": a["creee_le"], "quoi": "ouverture", "detail": f"règle {a['regle']}"}]
    ev += [{"quand": p["ajoutee_le"], "quoi": "preuve", "detail": p["transaction_id"]} for p in preuves]
    ev += [{"quand": s["calcule_le"], "quoi": "score", "detail": f"{s['score']:.2f} · {s['modele']} · {s['preuves']} preuves"} for s in scores]
    ev += [{"quand": d["decidee_le"], "quoi": "decision", "detail": f"{d['decision']} · {d['analyste']}"} for d in decisions]
    return sorted(ev, key=lambda e: e["quand"])


class Decision(BaseModel):
    decision: Literal["sans_suite", "escalade", "soupcon"]
    justification: str = Field(min_length=20, max_length=2000)
    analyste: str = Field(min_length=1, max_length=80)

    @field_validator("justification")
    @classmethod
    def justification_reelle(cls, v: str) -> str:
        if len(v.strip()) < 20:
            raise ValueError("une justification d'au moins 20 caractères est obligatoire")
        return v.strip()

    @field_validator("analyste")
    @classmethod
    def analyste_nomme(cls, v: str) -> str:
        if not v.strip():
            raise ValueError("l'analyste doit être nommé")
        return v.strip()


def origine_autorisee(origine: str | None) -> bool:
    """Navigateur : l'origine doit être l'interface. Outil sans navigateur (curl, tests) : pas d'en-tête Origin."""
    return origine is None or origine in ORIGINES


async def contre_csrf(request: Request) -> None:
    """Une page tierce peut envoyer un POST « simple » (sans requête préalable CORS, sans Content-Type JSON) :
    FastAPI le lirait quand même. On exige donc un vrai JSON ET une origine autorisée ; le CORS seul ne protège
    que la lecture de la réponse, pas l'écriture."""
    if request.headers.get("content-type", "").split(";")[0].strip() != "application/json":
        raise HTTPException(415, "Content-Type application/json obligatoire")
    if not origine_autorisee(request.headers.get("origin")):
        raise HTTPException(403, "origine non autorisée")


@app.post("/alertes/{alert_id}/decisions", status_code=201, dependencies=[Depends(contre_csrf)])
async def decider(alert_id: str, d: Decision) -> dict:
    if not await requete("SELECT 1 FROM alertes WHERE alert_id = %s", alert_id):
        raise HTTPException(404, "alerte inconnue")
    async with pool.connection() as con:
        cur = await con.execute(
            "INSERT INTO decisions (alert_id, decision, justification, analyste) VALUES (%s, %s, %s, %s) RETURNING *",
            (alert_id, d.decision, d.justification, d.analyste))
        return await cur.fetchone()


@app.websocket("/flux")
async def flux(ws: WebSocket) -> None:
    """Pousse la santé toutes les 3 s, et « maj » dès qu'une alerte, un score ou une décision change.
    Le CORS ne s'applique pas aux WebSockets : on vérifie l'origine nous-mêmes."""
    if not origine_autorisee(ws.headers.get("origin")):
        await ws.close(code=1008)
        return
    await ws.accept()
    version = None
    try:
        while True:
            v = (await requete("""SELECT (SELECT max(creee_le) FROM alertes) AS a, (SELECT max(calcule_le) FROM alertes_scores) AS s,
                                         (SELECT max(decision_id) FROM decisions) AS d,
                                         (SELECT max(ajoutee_le) FROM alertes_operations) AS p"""))[0]
            message = {"type": "sante", "sante": await sante()}
            if version is not None and v != version:
                message["maj"] = True
            version = v
            await ws.send_json(message, mode="text")
            await asyncio.sleep(3)
    except (WebSocketDisconnect, RuntimeError):
        return


@app.get("/")
async def accueil() -> dict:
    return {"service": "Filature", "maintenant": datetime.now().isoformat(timespec="seconds")}
