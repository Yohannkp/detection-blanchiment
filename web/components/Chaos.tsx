"use client";
// Console Chaos : on provoque une attaque ou une panne, et la plateforme doit réagir comme prévu.
// Chaque essai dit ce qu'il attend ; la console le vérifie elle-même sur l'API, sans faire confiance au service Chaos.
import { useEffect, useRef, useState } from "react";
import { alertesDuCompte, lancerScenario, operationsDuScenario, panneGraphe, type Regle, type ResultatChaos, type Sante } from "@/lib/api";
import { REGLES, ibanCourt } from "@/lib/format";

const SCENARIOS = [
  { nom: "schtroumpfage", titre: "Schtroumpfage", detail: "6 mules déposent des espèces sous le seuil COSI, puis virent tout à un même bénéficiaire." },
  { nom: "cycle", titre: "Cycle", detail: "4 sociétés écrans : l'argent fait le tour et revient à son point de départ." },
  { nom: "paie", titre: "Piège : une paie", detail: "Une entreprise établie verse 80 salaires le même jour. Aucune alerte ne doit sortir." },
  { nom: "doublons", titre: "100 doublons", detail: "La même opération renvoyée 100 fois, comme après une coupure réseau." },
  { nom: "corrompus", titre: "20 messages corrompus", detail: "IBAN faux, devise inconnue, montant négatif, JSON tronqué, schéma inconnu." },
];
const PANNE_S = 20;
const DELAI_S = 90;        // au-delà, une attente non tenue est un échec
const SILENCE_S = 30;      // piège : aucune alerte pendant 30 s APRÈS que tout le scénario est passé dans le graphe

type Etat = "attente" | "ok" | "ko";
// Chaque vérification porte sur l'essai lui-même : ses opérations sont reconnues à leur préfixe d'identifiant.
// Seuls les compteurs de doublons et de DLQ sont globaux (les messages refusés n'atteignent pas le grand livre).
type Verif =
  | { type: "alerte" | "absente"; regle: Regle; entite: string; etat: Etat; apres?: number; alerte?: string }
  | { type: "grand_livre" | "graphe"; libelle: string; cible: number; etat: Etat; apres?: number; vu?: number }
  | { type: "global"; libelle: string; cle: "doublons_absorbes" | "dlq"; cible: number; etat: Etat; apres?: number; vu?: number }
  | { type: "retard"; etat: Etat; apres?: number; manque?: number };
interface Essai {
  id: number; titre: string; resume: string; messages: number; debut: number; base: Sante | null; verifs: Verif[]; erreur?: string;
  prefixe?: string; attendues?: number;  // opérations du scénario attendues au grand livre
  complet?: number;                      // instant où toutes ses opérations sont dans le graphe (donc transmises à la détection)
}

const secondes = (ms: number) => `${(ms / 1000).toLocaleString("fr-FR", { maximumFractionDigits: 1 })} s`;
const fr = (n: number) => n.toLocaleString("fr-FR");

function libelle(v: Verif): string {
  switch (v.type) {
    case "alerte": return `${REGLES[v.regle]} sur ${ibanCourt(v.entite)}`;
    case "absente": return `aucun ${REGLES[v.regle].toLowerCase()} sur ${ibanCourt(v.entite)} (${SILENCE_S} s après la détection)`;
    case "grand_livre": case "graphe": case "global": return v.vu == null ? v.libelle : `${v.libelle} (${fr(v.vu)})`;
    case "retard": return `pendant la panne, le graphe en manque${v.manque ? ` ${fr(v.manque)}` : ""}`;
  }
}

function verifsDe(nom: string, r: ResultatChaos): [Verif[], number] {
  const n = r.messages ?? 0;
  if (nom === "doublons") return [[
    { type: "grand_livre", libelle: "une seule écriture au grand livre", cible: 1, etat: "attente" },
    { type: "global", libelle: `${n - 1} doublons absorbés`, cle: "doublons_absorbes", cible: n - 1, etat: "attente" },
  ], 1];
  if (nom === "corrompus") return [[
    { type: "global", libelle: `${n} messages en DLQ`, cle: "dlq", cible: n, etat: "attente" },
    { type: "grand_livre", libelle: "aucun au grand livre", cible: 0, etat: "attente" },
  ], 0];
  return [[
    ...(r.attendus ?? []).map((a): Verif => ({ type: "alerte", ...a, etat: "attente" })),
    ...(r.absents ?? []).map((a): Verif => ({ type: "absente", ...a, etat: "attente" })),
  ], n];
}

export default function Chaos({ sante, fermer, ouvrir }: { sante: Sante | null; fermer: () => void; ouvrir: (alerte: string) => void }) {
  const [essais, setEssais] = useState<Essai[]>([]);
  const [occupe, setOccupe] = useState<string | null>(null);
  const santeRef = useRef(sante);
  santeRef.current = sante;
  const compteur = useRef(0);

  const ajouter = (e: Omit<Essai, "id" | "debut">) =>
    setEssais((x) => [{ ...e, id: ++compteur.current, debut: Date.now() }, ...x].slice(0, 8));
  const echec = (titre: string, err: unknown) =>
    ajouter({ titre, resume: "", messages: 0, base: null, verifs: [], erreur: (err as Error).message });

  async function jouer(nom: string, titre: string) {
    setOccupe(nom);
    const base = santeRef.current;
    try {
      const r = await lancerScenario(nom);
      const [verifs, attendues] = verifsDe(nom, r);
      ajouter({ titre, resume: r.resume, messages: r.messages ?? 0, base, verifs, prefixe: r.prefixe, attendues });
    } catch (e) { echec(titre, e); } finally { setOccupe(null); }
  }

  async function panne() {
    const titre = `Panne du graph-sink · ${PANNE_S} s`;
    setOccupe("panne");
    try {
      await panneGraphe(PANNE_S);
      const r = await lancerScenario("normal");  // du trafic pendant la panne : le grand livre doit l'encaisser
      const n = r.messages ?? 0;
      ajouter({ titre, resume: `Le graph-sink cesse de lire pendant ${PANNE_S} s ; ${n} opérations arrivent pendant ce temps.`,
        messages: n, base: santeRef.current, prefixe: r.prefixe, attendues: n, verifs: [
          { type: "grand_livre", libelle: `le grand livre écrit les ${n} opérations`, cible: n, etat: "attente" },
          { type: "retard", etat: "attente" },
          { type: "graphe", libelle: "puis le graphe les rattrape toutes", cible: n, etat: "attente" },
        ] });
    } catch (e) { echec(titre, e); } finally { setOccupe(null); }
  }

  // Vérification en direct, toutes les 1,5 s. Une erreur de l'API ne valide jamais rien : la vérification reste en attente.
  const essaisRef = useRef(essais);
  essaisRef.current = essais;
  useEffect(() => {
    let enCours = false;
    const t = setInterval(async () => {
      if (enCours || !essaisRef.current.some((e) => e.verifs.some((v) => v.etat === "attente"))) return;
      enCours = true;
      try {
        const s = santeRef.current;
        const maj = await Promise.all(essaisRef.current.map(async (e): Promise<Essai> => {
          if (!e.verifs.some((v) => v.etat === "attente")) return e;
          const maintenant = Date.now(), ecoule = maintenant - e.debut;
          const ops = e.prefixe ? await operationsDuScenario(e.prefixe).catch(() => null) : null;
          let complet = e.complet;
          if (complet == null && ops && e.attendues != null && ops.grand_livre === e.attendues && ops.graphe === e.attendues) complet = maintenant;
          const verifs = await Promise.all(e.verifs.map(async (v): Promise<Verif> => {
            if (v.etat !== "attente") return v;
            if (v.type === "alerte" || v.type === "absente") {
              const regle = v.regle;
              const liste = await alertesDuCompte(v.entite).catch(() => null);
              const trouvee = liste?.find((a) => a.regle === regle);
              if (trouvee) return { ...v, etat: v.type === "alerte" ? "ok" : "ko", apres: ecoule, alerte: trouvee.alert_id };
              if (v.type === "absente" && liste && complet != null && maintenant - complet > SILENCE_S * 1000) return { ...v, etat: "ok", apres: ecoule };
            } else if ((v.type === "grand_livre" || v.type === "graphe") && ops) {
              const vu = v.type === "grand_livre" ? ops.grand_livre : ops.graphe;
              if (vu != null) {
                // Pour des refus (cible 0), « 0 » ne prouve rien tant que la DLQ n'a pas tout reçu.
                const pret = v.cible > 0 || e.verifs.every((w) => w.type !== "global" || w.etat === "ok");
                if (vu === v.cible && pret) return { ...v, etat: "ok", apres: ecoule, vu };
                if (vu > v.cible) return { ...v, etat: "ko", vu };
                v = { ...v, vu };
              }
            } else if (v.type === "global") {
              const b = e.base?.[v.cle], c = s?.[v.cle];
              if (b != null && c != null) {
                const vu = c - b;
                if (vu >= v.cible) return { ...v, etat: "ok", apres: ecoule, vu };
                v = { ...v, vu };
              }
            } else if (v.type === "retard" && ops && ops.graphe != null && e.attendues != null) {
              // Preuve sur les opérations de l'essai : toutes au grand livre, pas toutes dans le graphe.
              if (ops.grand_livre === e.attendues && ops.graphe < e.attendues) return { ...v, etat: "ok", apres: ecoule, manque: e.attendues - ops.graphe };
            }
            return ecoule > DELAI_S * 1000 ? { ...v, etat: "ko" } : v;
          }));
          return { ...e, verifs, complet };
        }));
        // On ne remplace que les essais vérifiés : un essai lancé pendant la vérification est gardé.
        setEssais((actuels) => actuels.map((e) => maj.find((m) => m.id === e.id) ?? e));
      } finally { enCours = false; }
    }, 1500);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    const touche = (e: KeyboardEvent) => { if (e.key === "Escape") fermer(); };
    window.addEventListener("keydown", touche);
    return () => window.removeEventListener("keydown", touche);
  }, [fermer]);

  const nb = (v: number | null | undefined) => (v == null ? "—" : v.toLocaleString("fr-FR"));
  return (
    <aside className="chaos" aria-label="Console Chaos">
      <div className="chaos-tete">
        <div><h2>Console Chaos</h2><p>On provoque une attaque ou une panne ; la console vérifie que la plateforme réagit comme prévu.</p></div>
        <button className="fermer" onClick={fermer} aria-label="Fermer la console Chaos">×</button>
      </div>
      <dl className="compteurs">
        <div><dt>Doublons absorbés</dt><dd>{nb(sante?.doublons_absorbes)}</dd></div>
        <div><dt>En DLQ</dt><dd>{nb(sante?.dlq)}</dd></div>
        <div className={sante?.graphe_en_retard ? "retard" : ""}><dt>Graphe en retard</dt><dd>{nb(sante?.graphe_en_retard)}</dd></div>
      </dl>
      <div className="titre"><span>Scénarios</span></div>
      <ul className="scenarios">
        {SCENARIOS.map((s) => (
          <li key={s.nom}>
            <div><b>{s.titre}</b><small>{s.detail}</small></div>
            <button className="bouton petit" disabled={occupe != null} onClick={() => jouer(s.nom, s.titre)}>{occupe === s.nom ? "…" : "Jouer"}</button>
          </li>
        ))}
        <li className="panne">
          <div><b>Panne du graph-sink · {PANNE_S} s</b><small>300 opérations arrivent pendant que le graphe est coupé. Le grand livre continue ; la détection, qui lit après le graphe, attend : alertes retardées, jamais perdues.</small></div>
          <button className="bouton petit" disabled={occupe != null} onClick={panne}>{occupe === "panne" ? "…" : "Couper"}</button>
        </li>
      </ul>
      <div className="titre"><span>Journal</span><span>{essais.length ? "vérifié sur l'API" : ""}</span></div>
      {essais.length === 0 && <p className="vide">Aucun essai pour l'instant.</p>}
      {essais.map((e) => {
        const ko = e.erreur || e.verifs.some((v) => v.etat === "ko");
        const fini = !e.verifs.some((v) => v.etat === "attente");
        return (
          <article key={e.id} className="essai" aria-live="polite">
            <header><b>{e.titre}</b><span className={`verdict ${e.erreur ? "ko" : fini ? (ko ? "ko" : "ok") : ""}`}>
              {e.erreur ? "échec" : !fini ? "en cours" : ko ? "écart" : "conforme"}</span></header>
            {e.erreur ? <p className="erreur">{e.erreur}</p> : <p>{e.resume} <span className="mono">{e.messages} msg</span></p>}
            <ul>
              {e.verifs.map((v, i) => (
                <li key={i} className={v.etat}>
                  <i aria-hidden>{v.etat === "ok" ? "✓" : v.etat === "ko" ? "✗" : "·"}</i>
                  {"alerte" in v && v.alerte
                    ? <button className="lien" onClick={() => ouvrir(v.alerte!)}>{libelle(v)}</button>
                    : <span>{libelle(v)}</span>}
                  <span className="mono">{v.apres != null ? secondes(v.apres) : v.etat === "ko" && v.type !== "absente" ? `pas vu en ${DELAI_S} s` : ""}</span>
                </li>
              ))}
            </ul>
          </article>
        );
      })}
    </aside>
  );
}
