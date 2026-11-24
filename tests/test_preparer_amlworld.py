"""Préparation AMLworld sur un extrait écrit à la main (10 lignes au format Kaggle, aucune donnée réelle)."""

import csv
import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "outils"))
from preparer_amlworld import iban_fictif, preparer  # noqa: E402

EXTRAIT = Path(__file__).parent / "fixtures" / "amlworld_extrait.csv"
MOTIFS = Path(__file__).parent / "fixtures" / "amlworld_motifs.txt"


def iban_valide(iban: str) -> bool:
    """Contrôle indépendant de l'implémentation : ISO 13616 (mod 97) et clé RIB."""
    chiffres = "".join(str(int(ch, 36)) for ch in iban[4:] + iban[:4])
    b, g, c, k = iban[4:9], iban[9:14], iban[14:25], iban[25:27]
    rib = (89 * int(b) + 15 * int(g) + 3 * int(c) + int(k)) % 97 == 0
    return len(iban) == 27 and int(chiffres) % 97 == 1 and rib


def lire(p: Path) -> list[dict]:
    with p.open(encoding="utf-8", newline="") as f:
        return list(csv.DictReader(f))


@pytest.fixture
def sortie(tmp_path):
    bilan = preparer(EXTRAIT, tmp_path, voisins_pct=100)
    return tmp_path, bilan


def test_selection_et_ecarts(sortie):
    d, bilan = sortie
    assert bilan["lignes_source"] == 10
    assert bilan["ecartees_autovirement"] == 2  # reinvestment et ACH illicite (ligne 10 : banques « 10 » ≠ « 010 », gardée)
    assert bilan["ecartees_autovirement_illicites"] == 1
    assert bilan["ecartees_change"] == 1
    ops = lire(d / "rejeu.csv")
    # gardées : lignes 4, 5 (illicites), 6, 7, 8 (voisins), 10 (banque « 10 » ≠ « 010 ») ; pas la 9 (sans lien)
    assert {o["transaction_id"] for o in ops} == {"AMLW-4", "AMLW-5", "AMLW-6", "AMLW-7", "AMLW-8", "AMLW-10"}
    assert [o["booked_at"] for o in ops] == sorted(o["booked_at"] for o in ops)


def test_montants_exacts_en_unites_mineures(sortie):
    ops = {o["transaction_id"]: o for o in lire(sortie[0] / "rejeu.csv")}
    assert (ops["AMLW-4"]["amount_minor"], ops["AMLW-4"]["currency"]) == ("350050", "EUR")
    assert (ops["AMLW-6"]["amount_minor"], ops["AMLW-6"]["currency"]) == ("150025", "JPY")
    assert (ops["AMLW-8"]["amount_minor"], ops["AMLW-8"]["currency"]) == ("12300", "XBT")  # satoshis
    assert ops["AMLW-7"]["payment_format"] == "credit_card"


def test_ibans_valides_et_distincts(sortie):
    ops = lire(sortie[0] / "rejeu.csv")
    ibans = {o["debtor_iban"] for o in ops} | {o["creditor_iban"] for o in ops}
    assert all(iban_valide(i) for i in ibans), ibans
    # même compte hexadécimal, banques « 10 » et « 010 » : deux IBAN différents
    assert iban_fictif("10", "8000AAAA1") != iban_fictif("010", "8000AAAA1")


def test_verite_a_part(sortie):
    d, bilan = sortie
    verite = {v["transaction_id"]: v["is_laundering"] for v in lire(d / "verite.csv")}
    assert verite["AMLW-4"] == "true" and verite["AMLW-6"] == "false"
    assert bilan["illicites_rejeu"] == 2
    assert "is_laundering" not in lire(d / "rejeu.csv")[0]  # l'étiquette n'entre jamais dans le flux
    assert json.loads((d / "bilan.json").read_text(encoding="utf-8"))["operations_rejeu"] == 6


def test_voisins_zero_garde_seulement_les_comptes_illicites(tmp_path):
    bilan = preparer(EXTRAIT, tmp_path, voisins_pct=0)
    assert {o["transaction_id"] for o in lire(tmp_path / "rejeu.csv")} == {"AMLW-4", "AMLW-5"}
    assert bilan["comptes_rejeu"] == 3


def test_motifs_relies_aux_operations(tmp_path):
    bilan = preparer(EXTRAIT, tmp_path, voisins_pct=100, motifs=MOTIFS)
    verite = {v["transaction_id"]: v for v in lire(tmp_path / "verite.csv")}
    assert (verite["AMLW-4"]["motif"], verite["AMLW-4"]["tentative"]) == ("cycle", "1")
    assert verite["AMLW-6"]["motif"] == ""  # opération licite : pas de motif
    assert bilan["illicites_rejeu_dans_un_motif"] == 2


def test_motif_sans_operation_correspondante_refuse(tmp_path):
    faux = tmp_path / "motifs.txt"
    faux.write_text(MOTIFS.read_text(encoding="utf-8").replace("3500.50,Euro,3500.50", "3500.51,Euro,3500.51"),
                    encoding="utf-8")
    with pytest.raises(ValueError, match="correspondances"):
        preparer(EXTRAIT, tmp_path / "s", voisins_pct=100, motifs=faux)
