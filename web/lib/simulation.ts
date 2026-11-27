// Simulation dans le navigateur (version en ligne) : montages de blanchiment, et les règles de détection de la
// plateforme portées telles quelles depuis ingestion/internal/detect (mêmes seuils, mêmes fenêtres, même
// plafond d'historique), appliquées opération par opération comme le détecteur Go. Le score du modèle n'est pas
// calculé ici. Fichier sans import de valeur : il est testé directement par Node (simulation.test.mjs).
import type { AlerteResume, Dossier, Operation, Regle } from "./api";

export const CAISSE = "INTERNE:CAISSE";

// Copie de regles.go : à garder identique.
export const REGLAGES = {
  fenetreDegre: 96 * 3600, seuilFanIn: 5, seuilFanOut: 6, plafondHistorique: 20,
  fenetreCycle: 7 * 24 * 3600, sautsMaxCycle: 5, plafondHubCycle: 200,
  depotsMinEspeces: 3, cumulMinEspeces: 800_000, cumulMaxEspeces: 1_000_000,
};

export interface Op { id: string; t: number; type: "transfer" | "cash_deposit"; src: string; dst: string; montant: number; devise: string }
export interface AlerteSim { id: string; regle: Regle; entite: string; ouverteLe: number; quand: number; declencheur: string; preuves: string[] }
export interface Evenement { op: number; regle: Regle; entite: string; nouvelle: boolean; alerte?: string; ecart?: string }

// ---------- identifiants et IBAN fictifs ----------
function hache(s: string): string {
  let a = 0x811c9dc5, b = 0x01000193 ^ 0x5bd1e995;
  for (let i = 0; i < s.length; i++) {
    a = Math.imul(a ^ s.charCodeAt(i), 0x01000193) >>> 0;
    b = Math.imul(b ^ s.charCodeAt(i), 0x5bd1e995) >>> 0;
  }
  return a.toString(16).padStart(8, "0") + b.toString(16).padStart(8, "0");
}
const idAlerte = (regle: Regle, entite: string, debut: number) => `${regle}-sim${hache(`${regle}|${entite}|${debut}`)}`;

function mod97(chiffres: string): number {
  let r = 0;
  for (const c of chiffres) r = (r * 10 + Number(c)) % 97;
  return r;
}
/** IBAN français fictif mais valide (clé RIB et clé IBAN calculées), banque « 30009 » réservée à la simulation. */
export function ibanSimule(alea: () => number): string {
  const banque = "30009", guichet = "00001";
  const compte = String(Math.floor(alea() * 1e11)).padStart(11, "0");
  const cleRib = 97 - ((89 * Number(banque) + 15 * Number(guichet) + 3 * Number(compte)) % 97);
  const bban = `${banque}${guichet}${compte}${String(cleRib).padStart(2, "0")}`;
  return `FR${String(98 - mod97(bban + "152700")).padStart(2, "0")}${bban}`;
}

// ---------- montages ----------
export interface Montage { nom: string; titre: string; detail: string; resume: string; ops: Op[]; attendu: string }
type Fabrique = (alea: () => number, maintenant: number) => Montage;

const H = 3600, J = 24 * H;

/** Fin des montages (en secondes). La plateforme range les alertes fan-in/fan-out par périodes fixes de 96 h :
 *  une rafale à cheval sur deux périodes donne deux alertes pour un seul réseau (regles.go). Pour la
 *  démonstration, on termine donc les montages assez loin du début d'une période (60 h, plus que leurs rafales). */
export function ancre(maintenant: number): number {
  const t = Math.floor(maintenant / 1000), W = REGLAGES.fenetreDegre;
  return t % W >= 60 * H ? t : Math.floor(t / W) * W - 60;
}

let compteur = 0;
function outils(alea: () => number, maintenant: number) {
  const run = Math.floor(alea() * 1e9).toString(36);
  const ops: Op[] = [];
  const t0 = ancre(maintenant);
  return {
    ops,
    neuf: () => ibanSimule(alea),
    entre: (a: number, b: number) => a + Math.floor(alea() * (b - a + 1)),
    virement(src: string, dst: string, montant: number, il: number) {
      ops.push({ id: `sim-${run}-${++compteur}`, t: t0 - il, type: "transfer", src, dst, montant, devise: "EUR" });
    },
    depot(dst: string, montant: number, il: number) {
      ops.push({ id: `sim-${run}-${++compteur}`, t: t0 - il, type: "cash_deposit", src: CAISSE, dst, montant, devise: "EUR" });
    },
  };
}
export const MONTAGES: Record<string, { titre: string; detail: string; fabrique: Fabrique }> = {
  cycle: {
    titre: "Cycle par des sociétés écrans", detail: "80 000 € font le tour de 4 sociétés et reviennent à l'expéditeur.",
    fabrique(alea, maintenant) {
      const o = outils(alea, maintenant);
      const c = [o.neuf(), o.neuf(), o.neuf(), o.neuf()];
      for (let i = 0; i < 4; i++) o.virement(c[i], c[(i + 1) % 4], Math.round(8_000_000 * 0.98 ** i), (4 - i) * 19 * H);
      return { nom: "cycle", titre: this.titre, detail: this.detail, ops: o.ops, attendu: "une alerte de cycle",
        resume: "4 sociétés écrans se renvoient 80 000 € en deux jours et demi, 2 % de commission à chaque étape." };
    },
  },
  fanin: {
    titre: "Afflux vers un compte collecteur", detail: "6 comptes neufs versent chacun 3 500 à 4 800 € au même compte.",
    fabrique(alea, maintenant) {
      const o = outils(alea, maintenant);
      const cible = o.neuf();
      for (let i = 0; i < 6; i++) o.virement(o.neuf(), cible, o.entre(350_000, 480_000), (6 - i) * o.entre(3, 9) * H);
      return { nom: "fanin", titre: this.titre, detail: this.detail, ops: o.ops, attendu: "une alerte fan-in (seuil : 5 émetteurs en 96 h)",
        resume: "6 comptes ouverts pour l'occasion versent chacun quelques milliers d'euros au même compte, en moins de deux jours." };
    },
  },
  schtroumpfage: {
    titre: "Schtroumpfage sous le seuil COSI", detail: "6 mules déposent des espèces sous 10 000 €, puis virent tout au même compte.",
    fabrique(alea, maintenant) {
      const o = outils(alea, maintenant);
      // Les dépôts d'une mule doivent tomber dans le même mois civil (UTC), comme dans scenarios.go.
      const fin = ancre(maintenant), d = new Date(fin * 1000);
      const recul = Math.min(2 * J, fin - Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1) / 1000);
      const il = (part: number) => Math.floor(recul * part);
      const beneficiaire = o.neuf();
      for (let m = 0; m < 6; m++) {
        const mule = o.neuf();
        let total = 0;
        for (let j = 0; j < 3; j++) {
          const montant = o.entre(290_000, 330_000);
          total += montant;
          o.depot(mule, montant, il(1 - (m * 3 + j) / 24));
        }
        o.virement(mule, beneficiaire, total, il(0.2 - m * 0.03));
      }
      return { nom: "schtroumpfage", titre: this.titre, detail: this.detail, ops: o.ops, attendu: "6 alertes « espèces fractionnées » et un fan-in",
        resume: "6 mules déposent chacune 3 fois environ 3 000 € en espèces (moins de 10 000 € par mois, donc pas de déclaration automatique), puis virent tout à un même bénéficiaire." };
    },
  },
  paie: {
    titre: "Piège : la paie d'une entreprise", detail: "80 salaires le même jour, versés par une entreprise établie. Rien ne doit sortir.",
    fabrique(alea, maintenant) {
      const o = outils(alea, maintenant);
      const employeur = o.neuf();
      for (let i = 0; i < 30; i++) o.virement(employeur, o.neuf(), o.entre(50_000, 550_000), (40 - i) * J);
      for (let i = 0; i < 80; i++) o.virement(employeur, o.neuf(), o.entre(180_000, 400_000), (80 - i) * 60);
      return { nom: "paie", titre: this.titre, detail: this.detail, ops: o.ops, attendu: "aucune alerte (faux positif évité)",
        resume: "Une entreprise qui a déjà payé 30 fournisseurs ce mois-ci verse 80 salaires le même matin : un afflux de virements sortants, mais légitime." };
    },
  },
};

// ---------- détection (port de etat.go, regles.go, cycles.go) ----------
interface Point { t: number; cp: string; id: string }

/** Applique les règles à chaque opération, dans l'ordre d'arrivée. Renvoie les alertes et ce qui s'est passé à chaque étape. */
export function detecter(ops: Op[]): { alertes: AlerteSim[]; evenements: Evenement[] } {
  const R = REGLAGES, W = R.fenetreDegre;
  const fenetres = new Map<string, Point[]>(), histos = new Map<string, Set<string>>();
  const especes = new Map<string, { t: number; id: string; montant: number }[]>();
  const alertes = new Map<string, AlerteSim>();
  const evenements: Evenement[] = [];
  const ecartsVus = new Set<string>();
  const graphe: Op[] = [];

  const lever = (i: number, regle: Regle, entite: string, debut: number, quand: number, preuves: string[]) => {
    const id = idAlerte(regle, entite, debut);
    const a = alertes.get(id);
    if (a) a.preuves = [...new Set([...a.preuves, ...preuves])];
    else alertes.set(id, { id, regle, entite, ouverteLe: debut, quand, declencheur: ops[i].id, preuves: [...new Set(preuves)] });
    evenements.push({ op: i, regle, entite, nouvelle: !a, alerte: id });
  };

  ops.forEach((op, i) => {
    const T = op.t;
    // Fan-in et fan-out : fenêtres (fin − 96 h, fin] qui contiennent T.
    const cotes: [Regle, string, string, number][] = [];
    if (op.dst !== CAISSE) cotes.push(["fan_in", op.dst, op.src, R.seuilFanIn]);
    if (op.src !== CAISSE) cotes.push(["fan_out", op.src, op.dst, R.seuilFanOut]);
    for (const [regle, compte, cp, seuil] of cotes) {
      const cle = `${regle}:${compte}`;
      const pts = fenetres.get(cle) ?? [];
      pts.push({ t: T, cp, id: op.id });
      pts.sort((a, b) => a.t - b.t);
      fenetres.set(cle, pts);
      const histo = histos.get(cle) ?? new Set<string>();
      histo.add(cp);
      histos.set(cle, histo);
      const parPeriode = new Map<number, { fin: number; preuves: string[] }>();
      const fins = [...new Set(pts.map((p) => p.t).filter((f) => f >= T && f < T + W))];
      for (const fin of fins) {
        const dans = pts.filter((p) => p.t > fin - W && p.t <= fin);
        if (new Set(dans.map((p) => p.cp)).size < seuil) continue;
        const periode = Math.floor(fin / W);
        const f = parPeriode.get(periode) ?? { fin, preuves: [] };
        f.preuves.push(...dans.map((p) => p.id));
        parPeriode.set(periode, f);
      }
      if (parPeriode.size === 0) continue;
      if (histo.size > R.plafondHistorique) {  // compte « hub » : la règle se tait (et on dit pourquoi)
        if (!ecartsVus.has(cle)) {
          ecartsVus.add(cle);
          evenements.push({ op: i, regle, entite: compte, nouvelle: false,
            ecart: `${histo.size} contreparties déjà vues : au-delà du plafond de ${R.plafondHistorique}, la règle reconnaît un compte qui paie ou encaisse beaucoup de monde, et ne lève rien` });
        }
        continue;
      }
      for (const [periode, f] of parPeriode) lever(i, regle, compte, periode * W, f.fin, f.preuves);
    }

    // Espèces fractionnées : dépôts d'espèces en euros du mois civil.
    if (op.type === "cash_deposit" && op.devise === "EUR") {
      const d = new Date(T * 1000);
      const mois = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1) / 1000;
      const cle = `${op.dst}|${mois}`;
      const depots = especes.get(cle) ?? [];
      depots.push({ t: T, id: op.id, montant: op.montant });
      especes.set(cle, depots);
      const cumul = depots.reduce((s, x) => s + x.montant, 0);
      if (depots.length >= R.depotsMinEspeces && cumul >= R.cumulMinEspeces && cumul <= R.cumulMaxEspeces)
        lever(i, "especes_fractionnees", op.dst, mois, T, depots.map((x) => x.id));
    }

    // Cycles : un chemin dst → … → src (1 à 5 opérations) qui, avec celle-ci, tient en 7 jours sans passer par un hub.
    graphe.push(op);
    if (op.src !== CAISSE && op.dst !== CAISSE) {
      const degre = (c: string) => graphe.filter((x) => x.src === c || x.dst === c).length;
      const C = R.fenetreCycle;
      if (degre(op.dst) <= R.plafondHubCycle) {
        const proches = graphe.filter((x) => x.id !== op.id && x.src !== CAISSE && x.dst !== CAISSE && Math.abs(x.t - T) <= C);
        const chercher = (noeud: string, chemin: Op[]): Op[] | null => {
          if (chemin.length > 0 && noeud === op.src) {
            const rs = [...chemin, op];
            const premiere = rs.reduce((m, r) => (r.t < m.t || (r.t === m.t && r.id < m.id) ? r : m));
            const fin = Math.max(...rs.map((r) => r.t));
            return fin <= premiere.t + C ? rs : null;
          }
          if (chemin.length === R.sautsMaxCycle) return null;
          for (const r of proches) {
            if (r.src !== noeud || chemin.includes(r) || degre(r.dst) > R.plafondHubCycle) continue;
            const trouve = chercher(r.dst, [...chemin, r]);
            if (trouve) return trouve;
          }
          return null;
        };
        const rs = chercher(op.dst, []);
        if (rs) {
          const premiere = rs.reduce((m, r) => (r.t < m.t || (r.t === m.t && r.id < m.id) ? r : m));
          const fin = Math.max(...rs.map((r) => r.t));
          lever(i, "cycle", premiere.src, Math.floor(fin / C) * C, fin, rs.map((r) => r.id));
        }
      }
    }
  });
  return { alertes: [...alertes.values()], evenements };
}

// ---------- alerte et dossier au format de l'API ----------
const iso = (t: number) => new Date(t * 1000).toISOString();
const operation = (o: Op): Operation => ({ transaction_id: o.id, booked_at: iso(o.t), src: o.src, dst: o.dst,
  amount_minor: o.montant, currency: o.devise, payment_format: o.type === "cash_deposit" ? "cash" : null });

export function versApi(a: AlerteSim, ops: Op[], creeLe: string): { resume: AlerteResume; dossier: Dossier } {
  const preuves = ops.filter((o) => a.preuves.includes(o.id)).sort((x, y) => x.t - y.t);
  const devises = new Set(preuves.map((o) => o.devise));
  const comptes = new Set(preuves.flatMap((o) => [o.src, o.dst]).filter((c) => c !== CAISSE));
  const debut = preuves[0].t, fin = preuves[preuves.length - 1].t;
  const voisinage = ops.filter((o) => !a.preuves.includes(o.id) && (comptes.has(o.src) || comptes.has(o.dst))
    && o.t >= debut - 3 * 24 * 3600 && o.t <= fin + 24 * 3600).slice(0, 40);
  const resume: AlerteResume = {
    alert_id: a.id, regle: a.regle, entite: a.entite, ouverte_le: iso(a.ouverteLe), creee_le: creeLe, score: null,
    n: preuves.length, devises: devises.size,
    montant: devises.size === 1 ? preuves.reduce((s, o) => s + o.montant, 0) : null,
    devise: devises.size === 1 ? preuves[0].devise : null, debut: iso(debut), fin: iso(fin), statut: "a_traiter",
  };
  const dossier: Dossier = {
    alerte: { alert_id: a.id, regle: a.regle, entite: a.entite, ouverte_le: iso(a.ouverteLe), declencheur: a.declencheur, creee_le: creeLe },
    score: null, preuves: preuves.map(operation), voisinage: voisinage.map(operation), decisions: [],
    audit: [{ quand: creeLe, quoi: "ouverture", detail: `règle ${a.regle} (simulation dans le navigateur)` },
      ...preuves.map((o) => ({ quand: creeLe, quoi: "preuve" as const, detail: o.id }))],
  };
  return { resume, dossier };
}

// ---------- alertes simulées de la session (gardées en mémoire, jamais envoyées) ----------
const simulees = new Map<string, { resume: AlerteResume; dossier: Dossier }>();
export const ajouterSimulees = (l: { resume: AlerteResume; dossier: Dossier }[]) => l.forEach((x) => simulees.set(x.resume.alert_id, x));
export const alertesSimulees = () => [...simulees.values()].map((x) => x.resume);
export const dossierSimule = (id: string) => simulees.get(id)?.dossier;
export const estSimulee = (id: string) => id.includes("-sim");
