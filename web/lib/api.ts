// Accès à l'API de l'analyste (api/app.py).
export const API = process.env.NEXT_PUBLIC_API ?? "http://localhost:18100";
/** Version en ligne (GitHub Pages) : pas d'API, un instantané des vraies données en JSON (scripts/instantane_web.py). */
export const STATIQUE = process.env.NEXT_PUBLIC_STATIQUE === "1";
export const BASE = process.env.NEXT_PUBLIC_BASE_PATH ?? "";

export type Regle = "fan_in" | "fan_out" | "cycle" | "especes_fractionnees";
export type Statut = "a_traiter" | "sans_suite" | "escalade" | "soupcon";
export type Decision = Exclude<Statut, "a_traiter">;

export interface Sante {
  graphe_operations: number | null;
  graphe_en_retard: number | null;
  doublons_absorbes: number | null;
  devises_en_ecart: number;
  ecritures_manquantes: number;
  operations: number;
  debit: number;
  alertes: number;
  a_traiter: number;
  outbox_en_attente: number;
  latence_p50_ms: number | null;
  dlq: number | null;
  instantane?: string;  // version en ligne : date de l'instantané
}

export interface AlerteResume {
  alert_id: string;
  regle: Regle;
  entite: string;
  ouverte_le: string;
  creee_le: string;
  score: number | null;
  n: number;
  devises: number;
  montant: number | null;  // null si plusieurs devises
  devise: string | null;
  debut: string;
  fin: string;
  statut: Statut;
}

export interface Operation {
  transaction_id: string;
  booked_at: string;
  src: string;
  dst: string;
  amount_minor: number;
  currency: string;
  payment_format?: string | null;
}

export interface Raison { variable: string; valeur: string | number | boolean | null; contribution: number }

export interface Dossier {
  alerte: { alert_id: string; regle: Regle; entite: string; ouverte_le: string; declencheur: string; creee_le: string };
  score: { score: number; tx_suspecte: string; raisons: Raison[]; modele: string; calcule_le: string } | null;
  preuves: Operation[];
  voisinage: Operation[];
  decisions: { decision: Decision; justification: string; analyste: string; decidee_le: string }[];
  audit: { quand: string; quoi: "ouverture" | "preuve" | "score" | "decision"; detail: string }[];
}

/** Fichier de l'instantané qui contient la réponse d'un appel de l'API. */
function fichierStatique(chemin: string): string {
  const u = new URL(chemin, "http://x");
  const q = (n: string) => u.searchParams.get(n);
  if (u.pathname === "/sante") return "sante.json";
  if (u.pathname === "/alertes") return `alertes/${q("statut")}-${q("tri")}-${q("regle") ?? "toutes"}.json`;
  if (u.pathname.startsWith("/alertes/")) return `dossiers/${u.pathname.slice(9)}.json`;
  throw new Error(`${u.pathname} n'existe pas dans la version en ligne`);
}

// Version en ligne : les décisions d'un visiteur restent dans son navigateur (rien n'est envoyé ni gardé).
const decisionsLocales = new Map<string, Dossier["decisions"]>();

async function lire<T>(chemin: string): Promise<T> {
  if (STATIQUE) {
    const fichier = fichierStatique(chemin);
    const r = await fetch(`${BASE}/donnees/${fichier}`, { cache: "no-store" });
    if (!r.ok) throw new Error(`${fichier} : absent de l'instantané en ligne`);
    return r.json();
  }
  const r = await fetch(API + chemin, { cache: "no-store" });
  if (!r.ok) throw new Error(`${chemin} : HTTP ${r.status}`);
  return r.json();
}

export const lireSante = () => lire<Sante>("/sante");
export type Tri = "score" | "recentes";
export async function lireAlertes(regle: Regle | null, statut: "a_traiter" | "traitees" | "toutes", tri: Tri) {
  const liste = await lire<AlerteResume[]>(`/alertes?statut=${statut}&tri=${tri}${regle ? `&regle=${regle}` : ""}&limite=200`);
  if (!STATIQUE) return liste;
  const statutLocal = (a: AlerteResume): AlerteResume => ({ ...a, statut: decisionsLocales.get(a.alert_id)?.at(-1)?.decision ?? a.statut });
  return statut === "a_traiter" ? liste.filter((a) => !decisionsLocales.has(a.alert_id)) : liste.map(statutLocal);
}
export async function lireDossier(id: string) {
  const d = await lire<Dossier>(`/alertes/${encodeURIComponent(id)}`);
  const locales = STATIQUE ? decisionsLocales.get(id) ?? [] : [];
  return { ...d, decisions: [...d.decisions, ...locales],
    audit: [...d.audit, ...locales.map((x) => ({ quand: x.decidee_le, quoi: "decision" as const, detail: `${x.decision} · ${x.analyste}` }))] };
}

export async function decider(id: string, decision: Decision, justification: string, analyste: string) {
  if (STATIQUE) {
    decisionsLocales.set(id, [...(decisionsLocales.get(id) ?? []), { decision, justification, analyste, decidee_le: new Date().toISOString() }]);
    return;
  }
  const r = await fetch(`${API}/alertes/${encodeURIComponent(id)}/decisions`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ decision, justification, analyste }),
  });
  if (!r.ok) {
    const corps = await r.json().catch(() => null);
    throw new Error(corps?.detail?.[0]?.msg ?? corps?.detail ?? `HTTP ${r.status}`);
  }
}

export const urlFlux = () => API.replace(/^http/, "ws") + "/flux";

// Console Chaos (service ingestion/cmd/chaos).
export const CHAOS = process.env.NEXT_PUBLIC_CHAOS ?? "http://localhost:18110";
export interface Attendu { regle: Regle; entite: string }
export interface ResultatChaos { nom: string; resume: string; messages?: number; prefixe?: string; attendus?: Attendu[]; absents?: Attendu[]; secondes?: number }

async function envoyerChaos(chemin: string, corps: object = {}): Promise<ResultatChaos> {
  if (STATIQUE) throw new Error("console Chaos en rejeu dans la version en ligne");
  const r = await fetch(CHAOS + chemin, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(corps) });
  if (!r.ok) throw new Error(`${chemin} : ${(await r.text()).trim() || r.status}`);
  return r.json();
}
export const lancerScenario = (nom: string) => envoyerChaos(`/scenarios/${encodeURIComponent(nom)}`);
export const panneGraphe = (secondes: number) => envoyerChaos("/pannes/graphe", { secondes });
export const alertesDuCompte = (entite: string) =>
  lire<AlerteResume[]>(`/alertes?statut=toutes&entite=${encodeURIComponent(entite)}&limite=20`);
export const operationsDuScenario = (prefixe: string) =>
  lire<{ grand_livre: number; graphe: number | null }>(`/operations?prefixe=${encodeURIComponent(prefixe)}`);
