// Formats français et libellés métier.
import type { Decision, Raison, Regle, Statut } from "./api";

export const REGLES: Record<Regle, string> = {
  fan_in: "Fan-in", fan_out: "Fan-out", cycle: "Cycle", especes_fractionnees: "Espèces fractionnées",
};
export const REGLES_LONG: Record<Regle, string> = {
  fan_in: "Afflux de nombreux émetteurs vers un compte",
  fan_out: "Dispersion d'un compte vers de nombreux bénéficiaires",
  cycle: "L'argent revient à son point de départ",
  especes_fractionnees: "Dépôts d'espèces juste sous le seuil COSI",
};
export const DECISIONS: Record<Decision, string> = {
  sans_suite: "Classer sans suite", escalade: "Escalader au niveau 2", soupcon: "Préparer une déclaration de soupçon",
};
export const STATUTS: Record<Statut, string> = {
  a_traiter: "À traiter", sans_suite: "Classée sans suite", escalade: "Escaladée", soupcon: "Déclaration de soupçon",
};

// Libellés des variables du modèle (ml/variables.py) ; les autres gardent leur nom technique.
const VARIABLES: Record<string, string> = {
  format: "Moyen de paiement",
  devise: "Devise",
  log_montant: "Montant",
  montant_rond_100: "Montant rond",
  change: "Opération avec change",
  meme_banque: "Même banque",
  heure: "Heure",
  paire_7j: "Paire de comptes déjà vue (7 j)",
  aller_retour_7j: "Aller-retour entre ces comptes (7 j)",
  ratio_montant_habituel_u: "Montant / habitude de l'émetteur",
  u_minutes_depuis_sortie: "Minutes depuis la sortie précédente",
  v_minutes_depuis_entree: "Minutes depuis l'entrée précédente",
};
const FENETRE = /^([uv])_([nds])_(out|in)_(24h|96h|7j)$/;
export function libelleVariable(v: string): string {
  if (VARIABLES[v]) return VARIABLES[v];
  const m = FENETRE.exec(v);
  if (!m) return v;
  const [, qui, quoi, sens, duree] = m;
  const compte = qui === "u" ? "de l'émetteur" : "du bénéficiaire";
  const mesure = { n: "Opérations", d: "Contreparties distinctes", s: "Montants" }[quoi as "n" | "d" | "s"];
  return `${mesure} ${sens === "out" ? "sortantes" : "entrantes"} ${compte} (${duree.replace("h", " h").replace("j", " j")})`;
}
export function valeurRaison(r: Raison): string {
  if (r.variable === "log_montant" && typeof r.valeur === "number") return Math.round(Math.expm1(r.valeur)).toLocaleString("fr-FR");
  if (typeof r.valeur === "number") return r.valeur.toLocaleString("fr-FR", { maximumFractionDigits: 2 });
  if (typeof r.valeur === "boolean") return r.valeur ? "oui" : "non";
  return r.valeur == null ? "—" : String(r.valeur).toUpperCase();
}

const DECIMALES: Record<string, number> = { XBT: 8 };
const unites = (minor: number, devise: string) => minor / 10 ** (DECIMALES[devise] ?? 2);
export const montant = (minor: number, devise: string) =>
  unites(minor, devise).toLocaleString("fr-FR", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + " " + devise;
export function montantCourt(minor: number, devise: string): string {
  const v = unites(minor, devise);
  const s = v >= 1e6 ? (v / 1e6).toLocaleString("fr-FR", { maximumFractionDigits: 1 }) + " M"
    : v >= 1e3 ? (v / 1e3).toLocaleString("fr-FR", { maximumFractionDigits: 1 }) + " k"
    : v.toLocaleString("fr-FR", { maximumFractionDigits: 0 });
  return `${s} ${devise}`;
}

const interne = (c: string) => c.startsWith("INTERNE:");
export const iban = (c: string) => (interne(c) ? "Caisse (espèces)" : c.replace(/(.{4})/g, "$1 ").trim());
export const ibanCourt = (c: string) => (interne(c) ? "Caisse" : "…" + c.slice(-6, -2) + " " + c.slice(-2));

const enUTC = { timeZone: "UTC" } as const;
export const date = (s: string) => new Date(s).toLocaleDateString("fr-FR", { day: "2-digit", month: "2-digit", year: "numeric", ...enUTC });
export const jourMois = (s: string | Date) => new Date(s).toLocaleDateString("fr-FR", { day: "2-digit", month: "2-digit", ...enUTC });
export const heure = (s: string) => new Date(s).toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit", ...enUTC });
// Horodatages de la plateforme (création, décision) : heure locale de l'analyste.
export const momentLocal = (s: string) =>
  new Date(s).toLocaleString("fr-FR", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });

export const score = (s: number | null) => (s == null ? "—" : s.toFixed(2).replace(".", ","));
export const codeDossier = (id: string) => {
  const [regle, ...reste] = id.split("-");
  return regle.slice(0, 2).toUpperCase() + "-" + reste.join("").slice(0, 6).toUpperCase();
};
