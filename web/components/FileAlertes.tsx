"use client";
import type { AlerteResume, Regle, Tri } from "@/lib/api";
import { REGLES, ibanCourt, jourMois, montantCourt, score } from "@/lib/format";

export type FiltreStatut = "a_traiter" | "traitees";
export const LIMITE = 200;  // l'API renvoie au plus 200 alertes, les mieux notées : on le dit

export default function FileAlertes({ alertes, choisie, choisir, regle, setRegle, statut, setStatut, tri, setTri }: {
  alertes: AlerteResume[] | null; choisie: string | null; choisir: (id: string) => void;
  regle: Regle | null; setRegle: (r: Regle | null) => void; statut: FiltreStatut; setStatut: (s: FiltreStatut) => void;
  tri: Tri; setTri: (t: Tri) => void;
}) {
  const regles: (Regle | null)[] = [null, "fan_in", "fan_out", "cycle", "especes_fractionnees"];
  return (
    <>
      <div className="titre"><span>File d'alertes</span>
        <span>{alertes ? `${alertes.length >= LIMITE ? `${LIMITE} premières` : alertes.length} · ${tri === "score" ? "par score" : "plus récentes d'abord"}` : ""}</span></div>
      <div className="filtres" role="group" aria-label="Statut">
        <button className="puce" aria-pressed={statut === "a_traiter"} onClick={() => setStatut("a_traiter")}>À traiter</button>
        <button className="puce" aria-pressed={statut === "traitees"} onClick={() => setStatut("traitees")}>Traitées</button>
        <span style={{ flex: 1 }} />
        <button className="puce" aria-pressed={tri === "score"} onClick={() => setTri("score")}>Score</button>
        <button className="puce" aria-pressed={tri === "recentes"} onClick={() => setTri("recentes")}>Récentes</button>
      </div>
      <div className="filtres" role="group" aria-label="Règle" style={{ marginBottom: 14 }}>
        {regles.map((r) => (
          <button key={r ?? "toutes"} className="puce" aria-pressed={regle === r} onClick={() => setRegle(r)}>{r ? REGLES[r] : "Toutes"}</button>
        ))}
      </div>
      {alertes == null && <p className="vide">Chargement…</p>}
      {alertes?.length === 0 && <p className="vide">Aucune alerte dans cette vue.</p>}
      <nav aria-label="Alertes">
        {alertes?.map((f) => (
          <button key={f.alert_id} className="alerte" aria-current={f.alert_id === choisie} onClick={() => choisir(f.alert_id)}>
            <span className={`note ${f.score == null ? "bas" : f.score > 0.8 ? "haut" : f.score < 0.2 ? "bas" : ""}`}>{score(f.score)}</span>
            <span className="regle">{REGLES[f.regle]}</span>
            <span className="montant">{f.montant != null && f.devise ? montantCourt(f.montant, f.devise) : `${f.devises} devises`}</span>
            <span className="compte">{ibanCourt(f.entite)} · {jourMois(f.debut)}</span>
            <span className="n">{f.n} op.</span>
          </button>
        ))}
      </nav>
    </>
  );
}
