// Accès à l'API de l'analyste (api/app.py).
export const API = process.env.NEXT_PUBLIC_API ?? "http://localhost:18100";

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

async function lire<T>(chemin: string): Promise<T> {
  const r = await fetch(API + chemin, { cache: "no-store" });
  if (!r.ok) throw new Error(`${chemin} : HTTP ${r.status}`);
  return r.json();
}

export const lireSante = () => lire<Sante>("/sante");
export type Tri = "score" | "recentes";
export const lireAlertes = (regle: Regle | null, statut: "a_traiter" | "traitees" | "toutes", tri: Tri) =>
  lire<AlerteResume[]>(`/alertes?statut=${statut}&tri=${tri}${regle ? `&regle=${regle}` : ""}&limite=200`);
export const lireDossier = (id: string) => lire<Dossier>(`/alertes/${encodeURIComponent(id)}`);

export async function decider(id: string, decision: Decision, justification: string, analyste: string) {
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
