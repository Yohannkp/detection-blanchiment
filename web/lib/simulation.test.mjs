// Règles portées dans le navigateur : chaque montage doit donner exactement ce que donne la plateforme.
// Lancement : node --test lib/   (Node 24 lit le TypeScript sans compilation)
import { test } from "node:test";
import assert from "node:assert/strict";
import { CAISSE, MONTAGES, detecter, ibanSimule, versApi } from "./simulation.ts";

function alea(graine) {  // pseudo-aléatoire reproductible
  let s = graine;
  return () => ((s = (Math.imul(s, 1103515245) + 12345) >>> 0) / 2 ** 32);
}
const MAINTENANT = Date.UTC(2026, 10, 27, 15, 0);
const regles = (r) => r.alertes.map((a) => a.regle).sort();

test("IBAN simulés valides (clé mod 97)", () => {
  const a = alea(1);
  for (let i = 0; i < 50; i++) {
    const iban = ibanSimule(a);
    const r = (iban.slice(4) + "1527" + iban.slice(2, 4)).split("").reduce((m, c) => (m * 10 + Number(c)) % 97, 0);
    assert.equal(r, 1, iban);
    assert.equal(iban.length, 27);
  }
});

for (const graine of [1, 2, 3, 42, 2026, 7, 99, 123, 555, 9001]) {
  test(`montages, graine ${graine}`, () => {
    const cycle = MONTAGES.cycle.fabrique(alea(graine), MAINTENANT);
    const r1 = detecter(cycle.ops);
    assert.deepEqual(regles(r1), ["cycle"]);
    assert.equal(r1.alertes[0].preuves.length, 4);
    assert.equal(r1.alertes[0].entite, cycle.ops[0].src);  // origine = émetteur de la plus ancienne opération

    const fanin = MONTAGES.fanin.fabrique(alea(graine), MAINTENANT);
    const r2 = detecter(fanin.ops);
    assert.deepEqual(regles(r2), ["fan_in"]);
    assert.equal(r2.alertes[0].entite, fanin.ops[0].dst);

    const s = MONTAGES.schtroumpfage.fabrique(alea(graine), MAINTENANT);
    const r3 = detecter(s.ops);
    assert.deepEqual(regles(r3), ["especes_fractionnees", "especes_fractionnees", "especes_fractionnees",
      "especes_fractionnees", "especes_fractionnees", "especes_fractionnees", "fan_in"]);

    const paie = MONTAGES.paie.fabrique(alea(graine), MAINTENANT);
    const r4 = detecter(paie.ops);
    assert.deepEqual(r4.alertes, []);
    assert.ok(r4.evenements.some((e) => e.regle === "fan_out" && e.ecart), "le plafond d'historique doit être expliqué");
  });
}

test("schtroumpfage le 1er du mois à 0 h 30 : tous les dépôts dans le même mois", () => {
  const debutMois = Date.UTC(2026, 11, 1, 0, 30);
  const r = detecter(MONTAGES.schtroumpfage.fabrique(alea(7), debutMois).ops);
  assert.equal(r.alertes.filter((a) => a.regle === "especes_fractionnees").length, 6);
});

test("à toute heure : jamais deux alertes pour un même montage", () => {
  for (let h = 0; h < 96 * 2; h += 1) {  // deux périodes de 96 h, heure par heure
    const quand = MAINTENANT + h * 3600_000;
    for (const nom of ["cycle", "fanin", "schtroumpfage"]) {
      const r = detecter(MONTAGES[nom].fabrique(alea(h + 1), quand).ops);
      const attendu = { cycle: 1, fanin: 1, schtroumpfage: 7 }[nom];
      assert.equal(r.alertes.length, attendu, `${nom} à +${h} h : ${r.alertes.map((a) => a.regle)}`);
    }
  }
});

test("seuils : 4 émetteurs ne suffisent pas, 10 500 € d'espèces non plus", () => {
  const a = alea(9), t = 1_800_000_000, cible = ibanSimule(a);
  const ops = [0, 1, 2, 3].map((i) => ({ id: `x${i}`, t: t + i * 3600, type: "transfer", src: ibanSimule(a), dst: cible, montant: 400_000, devise: "EUR" }));
  assert.deepEqual(detecter(ops).alertes, []);
  ops.push({ id: "x4", t: t + 5 * 3600, type: "transfer", src: ibanSimule(a), dst: cible, montant: 400_000, devise: "EUR" });
  assert.deepEqual(regles(detecter(ops)), ["fan_in"]);

  const dep = [0, 1, 2].map((i) => ({ id: `d${i}`, t: t + i * 3600, type: "cash_deposit", src: CAISSE, dst: cible, montant: 350_000, devise: "EUR" }));
  assert.deepEqual(detecter(dep).alertes, []);  // 10 500 € : au-delà, la COSI part d'office
});

test("cycle trop lent (plus de 7 jours) : pas d'alerte", () => {
  const a = alea(11), c = [ibanSimule(a), ibanSimule(a), ibanSimule(a)], t = 1_800_000_000;
  const ops = [0, 1, 2].map((i) => ({ id: `c${i}`, t: t + i * 4 * 86400, type: "transfer", src: c[i], dst: c[(i + 1) % 3], montant: 100_000, devise: "EUR" }));
  assert.deepEqual(detecter(ops).alertes, []);
});

test("alerte et dossier au format de l'API", () => {
  const m = MONTAGES.cycle.fabrique(alea(5), MAINTENANT);
  const { resume, dossier } = versApi(detecter(m.ops).alertes[0], m.ops, new Date(MAINTENANT).toISOString());
  assert.equal(resume.n, 4);
  assert.equal(resume.devise, "EUR");
  assert.equal(resume.montant, m.ops.reduce((s, o) => s + o.montant, 0));
  assert.equal(dossier.preuves.length, 4);
  assert.equal(dossier.score, null);
  assert.equal(dossier.audit[0].quoi, "ouverture");
});
