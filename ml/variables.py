"""Variables du modèle, calculées « au moment de l'opération » (point-in-time).

Une variable de l'opération t (u → v à l'instant T) n'utilise que t elle-même et des opérations
STRICTEMENT antérieures à T (les horodatages AMLworld sont à la minute : les opérations de la même
minute ne se voient pas entre elles). Ajouter des opérations futures ne change donc aucune variable
passée : c'est ce que vérifie tests/test_variables.py.

Le même SQL sert à l'entraînement (CSV AMLworld complet) et au score en direct (opérations du grand
livre, lues dans Postgres par DuckDB) : pas d'écart entre les deux chemins.

Méthode : chaque opération devient deux événements de compte (une sortie pour u, une entrée pour v).
Toutes les statistiques sont des fenêtres glissantes sur ce flux, par compte : pas de jointure qui
exploserait sur les comptes « hubs » (jusqu'à 168 000 opérations dans HI-Small).

Entrée : une table `ops` (transaction_id, ts, src, dst, montant, devise, devise_recue, format).
Sortie : une table `variables` (transaction_id + variables).
"""

from __future__ import annotations

import duckdb

CATEGORIELLES = ["devise", "format"]

# Uniquement des fenêtres glissantes : un compteur « depuis le début » grandit mécaniquement avec le temps,
# et le modèle apprendrait l'époque (début ou fin du jeu) au lieu du comportement (constaté : arrêt
# anticipé dès la 1re itération, F1 instable selon la graine).
_FENETRES = {"24h": "INTERVAL 24 HOUR PRECEDING", "96h": "INTERVAL 96 HOUR PRECEDING", "7j": "INTERVAL 7 DAY PRECEDING"}
_FIN = "INTERVAL 1 SECOND PRECEDING"  # strictement avant la minute de l'opération (ts est tronqué à la minute)


def _stats_compte() -> str:
    cols = []
    for nom, debut in _FENETRES.items():
        w = f"OVER (PARTITION BY compte ORDER BY ts RANGE BETWEEN {debut} AND {_FIN})"
        for sens in ("out", "in"):
            f = f"FILTER (WHERE sens = '{sens}')"
            cols += [
                f"count(*) {f} {w} AS n_{sens}_{nom}",
                f"count(DISTINCT autre) {f} {w} AS d_{sens}_{nom}",
            ]
            cols.append(f"coalesce(sum(montant) {f} {w}, 0) AS s_{sens}_{nom}")
    w7 = f"OVER (PARTITION BY compte ORDER BY ts RANGE BETWEEN INTERVAL 7 DAY PRECEDING AND {_FIN})"
    cols += [
        f"max(ts) FILTER (WHERE sens = 'out') {w7} AS derniere_sortie",
        f"max(ts) FILTER (WHERE sens = 'in') {w7} AS derniere_entree",
        f"avg(montant) FILTER (WHERE sens = 'out') {w7} AS montant_moyen_sorti",
    ]
    return ",\n           ".join(cols)


def _noms_stats() -> list[str]:
    noms = []
    for nom in _FENETRES:
        for sens in ("out", "in"):
            noms += [f"n_{sens}_{nom}", f"d_{sens}_{nom}", f"s_{sens}_{nom}"]
    return noms


def _sql() -> str:
    stats = _noms_stats()
    u = ", ".join(f"cu.{s} AS u_{s}" for s in stats)
    v = ", ".join(f"cv.{s} AS v_{s}" for s in stats)
    return f"""
    -- Horodatages tronqués à la minute (résolution d'AMLworld) : deux opérations de la même minute ne se
    -- voient jamais, même horodatées à la microseconde en direct.
    CREATE OR REPLACE TEMP TABLE _o AS SELECT * REPLACE (date_trunc('minute', ts) AS ts) FROM ops;

    CREATE OR REPLACE TEMP TABLE _ev AS
    SELECT transaction_id, src AS compte, 'out' AS sens, dst AS autre, ts, montant FROM _o
    UNION ALL
    SELECT transaction_id, dst, 'in', src, ts, montant FROM _o;

    CREATE OR REPLACE TEMP TABLE _st AS
    SELECT transaction_id, sens,
           {_stats_compte()}
    FROM _ev;

    -- u connaît-il déjà v ? v a-t-il envoyé de l'argent à u dans les 7 jours (aller-retour) ?
    -- Fenêtre sur la paire NON orientée : on compte chaque sens, puis on garde le sens opposé.
    CREATE OR REPLACE TEMP TABLE _paires AS
    SELECT transaction_id,
           count(*) OVER (PARTITION BY src, dst ORDER BY ts RANGE BETWEEN INTERVAL 7 DAY PRECEDING AND {_FIN}) AS paire_7j,
           CASE WHEN src < dst
                THEN count(*) FILTER (WHERE src > dst) OVER w7
                ELSE count(*) FILTER (WHERE src < dst) OVER w7 END AS aller_retour_7j
    FROM _o
    WINDOW w7 AS (PARTITION BY least(src, dst), greatest(src, dst) ORDER BY ts
                  RANGE BETWEEN INTERVAL 7 DAY PRECEDING AND {_FIN});

    CREATE OR REPLACE TABLE variables AS
    SELECT o.transaction_id,
           ln(1 + o.montant)                                     AS log_montant,
           o.montant = round(o.montant, -2)                      AS montant_rond_100,
           o.devise, o.format,
           o.devise <> o.devise_recue                            AS change,
           o.src = o.dst                                         AS autovirement,
           split_part(o.src, '/', 1) = split_part(o.dst, '/', 1) AS meme_banque,
           hour(o.ts)                                            AS heure,
           {u},
           {v},
           coalesce(date_diff('minute', cu.derniere_sortie, o.ts), -1) AS u_minutes_depuis_sortie,
           coalesce(date_diff('minute', cv.derniere_entree, o.ts), -1) AS v_minutes_depuis_entree,
           o.montant / nullif(cu.montant_moyen_sorti, 0)               AS ratio_montant_habituel_u,
           p.paire_7j,
           CASE WHEN o.src = o.dst THEN 0 ELSE p.aller_retour_7j END   AS aller_retour_7j
    FROM _o o
    JOIN _st cu ON cu.transaction_id = o.transaction_id AND cu.sens = 'out'
    JOIN _st cv ON cv.transaction_id = o.transaction_id AND cv.sens = 'in'
    JOIN _paires p ON p.transaction_id = o.transaction_id;
    """


def construire(con: duckdb.DuckDBPyConnection) -> list[str]:
    """Crée la table `variables` à partir de la table `ops` ; renvoie les noms des variables."""
    con.execute(_sql())
    return [c for c in con.sql("SELECT * FROM variables LIMIT 0").columns if c != "transaction_id"]
