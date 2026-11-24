"""Prépare le jeu IBM AMLworld (HI-Small_Trans.csv) pour le rejeu en flux.

Entrée : le CSV de Kaggle (ealtman2019/ibm-transactions-for-anti-money-laundering-aml, licence CDLA-Sharing-1.0).
Sorties (dans --sortie) :
  rejeu.csv    opérations à rejouer, triées par horodatage, au format du contrat transaction.v2
  verite.csv   étiquette de chaque opération rejouée, et son motif si --motifs est donné
               (n'entre JAMAIS dans le flux)
  comptes.csv  correspondance IBAN fictif ↔ banque/compte AMLworld
  bilan.json   ce qui a été gardé, écarté, et pourquoi

Choix (voir docs/decisions/002-donnees-amlworld.md) :
  - identifiant = « AMLW-<n° de ligne> » : déterministe, donc rejouer deux fois ne crée rien de nouveau ;
  - écartés : les autovirements (le grand livre refuse un virement d'un compte vers lui-même) et les
    opérations avec change (devise payée ≠ devise reçue), que le grand livre mono-devise ne sait pas encore porter ;
  - sous-graphe : tous les comptes qui touchent une opération illicite + une part (--voisins %) de leurs
    voisins directs ; on garde les opérations dont LES DEUX comptes sont retenus ;
  - montants en unités mineures exactes : centièmes pour les devises fiduciaires (le jeu exprime aussi le yen
    au centième), satoshi (10⁻⁸) pour le bitcoin. Une conversion inexacte arrête le script.

Usage : python outils/preparer_amlworld.py data/HI-Small_Trans.csv --sortie data --voisins 25             --motifs data/HI-Small_Patterns.txt
"""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path

import duckdb

DEVISES = {  # nom AMLworld → (code, nombre de décimales de l'unité mineure)
    "US Dollar": ("USD", 2), "Euro": ("EUR", 2), "Swiss Franc": ("CHF", 2), "Yuan": ("CNY", 2),
    "Shekel": ("ILS", 2), "Rupee": ("INR", 2), "UK Pound": ("GBP", 2), "Yen": ("JPY", 2),
    "Ruble": ("RUB", 2), "Canadian Dollar": ("CAD", 2), "Australian Dollar": ("AUD", 2),
    "Mexican Peso": ("MXN", 2), "Saudi Riyal": ("SAR", 2), "Brazil Real": ("BRL", 2), "Bitcoin": ("XBT", 8),
}
FORMATS = {"ACH": "ach", "Cheque": "cheque", "Credit Card": "credit_card", "Cash": "cash",
           "Wire": "wire", "Bitcoin": "bitcoin"}


def mod97(chiffres: str) -> int:
    return int(chiffres) % 97


def iban_fictif(banque: str, compte_hex: str) -> str:
    """IBAN français fictif, valide (clé RIB et clé IBAN calculées), bijectif avec (banque, compte).

    Banque AMLworld : 3 à 7 chiffres → 10 chiffres « longueur + identifiant complété » (code banque + guichet),
    pour que « 010 » et « 10 » restent distincts. Compte : 9 caractères hexadécimaux → 11 chiffres décimaux.
    """
    if not banque.isdigit() or not 1 <= len(banque) <= 9:
        raise ValueError(f"banque inattendue {banque!r}")
    bg = f"{len(banque)}{banque:0>9}"
    code_banque, guichet = bg[:5], bg[5:]
    compte = f"{int(compte_hex, 16):011d}"
    if len(compte) != 11:
        raise ValueError(f"compte trop long {compte_hex!r}")
    cle_rib = 97 - (89 * int(code_banque) + 15 * int(guichet) + 3 * int(compte)) % 97
    bban = f"{code_banque}{guichet}{compte}{cle_rib:02d}"
    cle_iban = 98 - mod97(bban + "1527" + "00")  # F=15, R=27
    return f"FR{cle_iban:02d}{bban}"


COLONNES = ["ts", "banque_src", "compte_src", "banque_dst", "compte_dst",
            "montant_recu", "devise_recue", "montant_paye", "devise_payee", "format", "blanchiment"]


def lire_motifs(chemin: Path) -> list[tuple]:
    """HI-Small_Patterns.txt : blocs « BEGIN LAUNDERING ATTEMPT - <MOTIF>: … » / lignes CSV / « END … ».

    Renvoie (n° de tentative, motif, 11 champs bruts) pour chaque opération d'un motif.
    """
    lignes, tentative, motif = [], 0, None
    for brute in chemin.read_text(encoding="utf-8").splitlines():
        brute = brute.strip()
        if brute.startswith("BEGIN LAUNDERING ATTEMPT"):
            m = re.match(r"BEGIN LAUNDERING ATTEMPT - ([A-Z-]+)", brute)
            if not m:
                raise ValueError(f"en-tête de motif illisible : {brute!r}")
            tentative, motif = tentative + 1, m.group(1).lower()
        elif brute.startswith("END LAUNDERING ATTEMPT"):
            motif = None
        elif brute:
            champs = brute.split(",")
            if motif is None or len(champs) != len(COLONNES):
                raise ValueError(f"ligne de motif hors bloc ou mal formée : {brute!r}")
            lignes.append((tentative, motif, *champs))
    return lignes


def preparer(source: Path, sortie: Path, voisins_pct: int, motifs: Path | None = None) -> dict:
    sortie.mkdir(parents=True, exist_ok=True)
    c = duckdb.connect()
    c.execute("SET preserve_insertion_order = true")
    # Lecture en texte : les montants sont convertis en décimal exact, jamais en flottant.
    c.execute(
        """
        CREATE TABLE brut AS
        SELECT row_number() OVER () AS ligne, *
        FROM read_csv(?, header = true, all_varchar = true, names = ?)
        """,
        [str(source), COLONNES],
    )

    # Motifs : chaque opération d'un motif doit correspondre à exactement UNE ligne du CSV (tous champs égaux).
    c.execute(f"CREATE TABLE motifs (tentative INT, motif VARCHAR, {', '.join(f'{k} VARCHAR' for k in COLONNES)})")
    if motifs is not None:
        c.executemany(f"INSERT INTO motifs VALUES ({', '.join(['?'] * (len(COLONNES) + 2))})", lire_motifs(motifs))
    c.execute(f"CREATE TABLE motif_lignes AS SELECT m.tentative, m.motif, b.ligne FROM motifs m JOIN brut b USING ({', '.join(COLONNES)})")
    n_motifs, n_lies, n_lignes = c.execute(
        "SELECT (SELECT count(*) FROM motifs), count(*), count(DISTINCT ligne) FROM motif_lignes").fetchone()
    if not n_motifs == n_lies == n_lignes:
        raise ValueError(f"motifs : {n_motifs} opérations, {n_lies} correspondances, {n_lignes} lignes distinctes")
    total, illicites_total = c.execute("SELECT count(*), sum(blanchiment::INT) FROM brut").fetchone()

    c.execute(
        """
        CREATE TABLE ops AS
        SELECT ligne, strptime(ts, '%Y/%m/%d %H:%M') AS ts,
               banque_src, compte_src, banque_dst, compte_dst,
               banque_src || '/' || compte_src AS src, banque_dst || '/' || compte_dst AS dst,
               montant_paye::DECIMAL(24, 8) AS montant, devise_payee AS devise, format,
               blanchiment = '1' AS blanchiment
        FROM brut
        WHERE devise_recue = devise_payee
          AND NOT (banque_src = banque_dst AND compte_src = compte_dst)
        """
    )
    ecartes = c.execute(
        """
        SELECT count(*) FILTER (WHERE banque_src = banque_dst AND compte_src = compte_dst),
               sum(blanchiment::INT) FILTER (WHERE banque_src = banque_dst AND compte_src = compte_dst),
               count(*) FILTER (WHERE devise_recue <> devise_payee AND NOT (banque_src = banque_dst AND compte_src = compte_dst)),
               sum(blanchiment::INT) FILTER (WHERE devise_recue <> devise_payee AND NOT (banque_src = banque_dst AND compte_src = compte_dst))
        FROM brut
        """
    ).fetchone()

    # Sous-graphe : comptes illicites + une part stable (empreinte md5) de leurs voisins directs.
    c.execute(
        """
        CREATE TABLE illicites AS
        SELECT src AS compte FROM ops WHERE blanchiment UNION SELECT dst FROM ops WHERE blanchiment;
        CREATE TABLE voisins AS
        (SELECT dst AS compte FROM ops WHERE src IN (SELECT compte FROM illicites)
         UNION SELECT src FROM ops WHERE dst IN (SELECT compte FROM illicites))
        EXCEPT SELECT compte FROM illicites;
        """
    )
    c.execute(
        """
        CREATE TABLE retenus AS
        SELECT compte FROM illicites
        UNION SELECT compte FROM voisins
              WHERE CAST(('0x' || substr(md5(compte), 1, 8)) AS UBIGINT) % 100 < ?
        """,
        [voisins_pct],
    )
    c.execute(
        """
        CREATE TABLE rejeu AS
        SELECT * FROM ops
        WHERE src IN (SELECT compte FROM retenus) AND dst IN (SELECT compte FROM retenus)
        """
    )

    # Devises et formats : tout nom inconnu arrête le script plutôt que de produire un message faux.
    inconnues = c.execute("SELECT DISTINCT devise FROM rejeu").fetchall()
    for (d,) in inconnues:
        if d not in DEVISES:
            raise ValueError(f"devise inconnue {d!r}")
    for (f,) in c.execute("SELECT DISTINCT format FROM rejeu").fetchall():
        if f not in FORMATS:
            raise ValueError(f"format de paiement inattendu {f!r}")
    c.execute("CREATE TABLE devises (nom VARCHAR, code VARCHAR, decimales INT)")
    c.executemany("INSERT INTO devises VALUES (?, ?, ?)", [(n, cd, dec) for n, (cd, dec) in DEVISES.items()])
    c.execute("CREATE TABLE formats (nom VARCHAR, code VARCHAR)")
    c.executemany("INSERT INTO formats VALUES (?, ?)", list(FORMATS.items()))

    inexacts = c.execute(
        """
        SELECT count(*) FROM rejeu r JOIN devises d ON d.nom = r.devise
        WHERE r.montant * (10 ** d.decimales)::DECIMAL(24, 0) <> round(r.montant * (10 ** d.decimales)::DECIMAL(24, 0))
           OR r.montant <= 0
        """
    ).fetchone()[0]
    if inexacts:
        raise ValueError(f"{inexacts} montant(s) non convertibles exactement en unités mineures")

    # IBAN fictifs, calculés en Python (une fois par compte).
    comptes = c.execute(
        """
        SELECT DISTINCT banque, compte FROM (
          SELECT banque_src AS banque, compte_src AS compte FROM rejeu
          UNION SELECT banque_dst, compte_dst FROM rejeu)
        """
    ).fetchall()
    c.execute("CREATE TABLE ibans (banque VARCHAR, compte VARCHAR, iban VARCHAR)")
    c.executemany("INSERT INTO ibans VALUES (?, ?, ?)", [(b, a, iban_fictif(b, a)) for b, a in comptes])
    if c.execute("SELECT count(DISTINCT iban) = count(*) FROM ibans").fetchone()[0] is not True:
        raise ValueError("deux comptes ont reçu le même IBAN")

    c.execute(
        f"""
        COPY (
          SELECT 'AMLW-' || r.ligne AS transaction_id,
                 strftime(r.ts, '%Y-%m-%dT%H:%M:%SZ') AS booked_at,
                 'transfer' AS payment_type,
                 s.iban AS debtor_iban, d.iban AS creditor_iban,
                 CAST(r.montant * (10 ** dv.decimales)::DECIMAL(24, 0) AS BIGINT) AS amount_minor,
                 dv.code AS currency, f.code AS payment_format
          FROM rejeu r
          JOIN ibans s ON s.banque = r.banque_src AND s.compte = r.compte_src
          JOIN ibans d ON d.banque = r.banque_dst AND d.compte = r.compte_dst
          JOIN devises dv ON dv.nom = r.devise
          JOIN formats f ON f.nom = r.format
          ORDER BY r.ts, r.ligne
        ) TO '{(sortie / "rejeu.csv").as_posix()}' (HEADER, DELIMITER ',')
        """
    )
    c.execute(
        f"""COPY (SELECT 'AMLW-' || r.ligne AS transaction_id, r.blanchiment AS is_laundering, m.motif, m.tentative
                  FROM rejeu r LEFT JOIN motif_lignes m USING (ligne) ORDER BY r.ligne)
            TO '{(sortie / "verite.csv").as_posix()}' (HEADER, DELIMITER ',')"""
    )
    c.execute(
        f"""COPY (SELECT iban, banque AS banque_amlworld, compte AS compte_amlworld FROM ibans ORDER BY iban)
            TO '{(sortie / "comptes.csv").as_posix()}' (HEADER, DELIMITER ',')"""
    )

    n, ill, debut, fin = c.execute(
        "SELECT count(*), sum(blanchiment::INT), min(ts), max(ts) FROM rejeu"
    ).fetchone()
    bilan = {
        "source": source.name,
        "lignes_source": total,
        "illicites_source": int(illicites_total or 0),
        "ecartees_autovirement": ecartes[0],
        "ecartees_autovirement_illicites": int(ecartes[1] or 0),
        "ecartees_change": ecartes[2],
        "ecartees_change_illicites": int(ecartes[3] or 0),
        "voisins_retenus_pct": voisins_pct,
        "comptes_rejeu": len(comptes),
        "operations_rejeu": n,
        "illicites_rejeu": int(ill or 0),
        "illicites_rejeu_dans_un_motif": c.execute(
            "SELECT count(*) FROM rejeu JOIN motif_lignes USING (ligne)").fetchone()[0],
        "debut": str(debut),
        "fin": str(fin),
    }
    (sortie / "bilan.json").write_text(json.dumps(bilan, ensure_ascii=False, indent=2), encoding="utf-8")
    return bilan


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("source", type=Path)
    p.add_argument("--sortie", type=Path, default=Path("data"))
    p.add_argument("--voisins", type=int, default=25, help="part des voisins directs gardés, en %%")
    p.add_argument("--motifs", type=Path, help="HI-Small_Patterns.txt (facultatif)")
    a = p.parse_args()
    if not 0 <= a.voisins <= 100:
        p.error("--voisins doit être entre 0 et 100")
    print(json.dumps(preparer(a.source, a.sortie, a.voisins, a.motifs), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
