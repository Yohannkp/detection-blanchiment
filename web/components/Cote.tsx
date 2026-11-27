"use client";
// Colonne de droite : pourquoi ce score, décision de l'analyste, piste d'audit.
import { useState } from "react";
import { STATIQUE, decider, type Decision, type Dossier } from "@/lib/api";
import { DECISIONS, STATUTS, libelleVariable, momentLocal, score, valeurRaison } from "@/lib/format";

const QUOI = { ouverture: "Alerte ouverte", preuve: "Preuve ajoutée", score: "Score", decision: "Décision" } as const;

// « escalade · y.a » → « Escaladée · y.a » ; nom de modèle abrégé.
function detail(quoi: string, d: string) {
  if (quoi === "decision") { const [code, ...qui] = d.split(" · "); return [STATUTS[code as keyof typeof STATUTS] ?? code, ...qui].join(" · "); }
  return d.replace("detection-blanchiment-lgbm-", "");
}

export default function Cote({ d, apres }: { d: Dossier; apres: () => void }) {
  const [choix, setChoix] = useState<Decision | null>(null);
  const [texte, setTexte] = useState("");
  const [analyste, setAnalyste] = useState("analyste.demo");
  const [etat, setEtat] = useState<{ envoi?: boolean; erreur?: string; ok?: string }>({});
  const raisons = d.score?.raisons ?? [];
  const max = Math.max(...raisons.map((r) => r.contribution), 1e-9);
  const pret = choix != null && texte.trim().length >= 20 && analyste.trim().length > 0 && !etat.envoi;

  async function envoyer(e: React.FormEvent) {
    e.preventDefault();
    if (!pret || !choix) return;
    setEtat({ envoi: true });
    try {
      await decider(d.alerte.alert_id, choix, texte, analyste);
      setEtat({ ok: STATIQUE ? "Décision inscrite, dans ce navigateur seulement (version en ligne : rien n'est envoyé)." : "Décision inscrite dans la piste d'audit." });
      setTexte(""); setChoix(null);
      apres();
    } catch (err) {
      setEtat({ erreur: (err as Error).message });
    }
  }

  // Les preuves sont nombreuses dans certains dossiers : on les regroupe dans la piste d'audit.
  const evenements = d.audit.reduce<{ quand: string; quoi: keyof typeof QUOI; detail: string; n?: number }[]>((acc, e) => {
    const prec = acc.at(-1);
    if (e.quoi === "preuve" && prec?.quoi === "preuve") { prec.n = (prec.n ?? 1) + 1; return acc; }
    return [...acc, { ...e }];
  }, []);

  return (
    <>
      <div>
        <div className="titre"><span>Pourquoi ce score</span>{d.score && <span>{score(d.score.score)}</span>}</div>
        {raisons.length === 0 && <p className="explication">{d.alerte.alert_id.includes("-sim")
          ? "Dossier simulé dans votre navigateur : les règles ont tourné ici, mais le modèle LightGBM, lui, ne tourne que sur la plateforme complète."
          : "Le modèle n'a pas encore noté ce dossier."}</p>}
        {raisons.map((r) => (
          <div className="raison" key={r.variable}>
            <div className="l"><span>{libelleVariable(r.variable)}</span><b>{valeurRaison(r)}</b></div>
            <div className="barre"><i style={{ width: `${Math.max(0, (100 * r.contribution) / max)}%` }} /></div>
          </div>
        ))}
        {raisons.length > 0 && <p className="explication">Ce qui pousse le plus le score vers « blanchiment » pour l'opération la plus suspecte du dossier (contributions SHAP du modèle).</p>}
      </div>

      <form onSubmit={envoyer}>
        <div className="titre"><span>Décision</span></div>
        <fieldset className="choix">
          <legend className="titre" style={{ position: "absolute", left: -9999 }}>Décision</legend>
          {(Object.keys(DECISIONS) as Decision[]).map((k) => (
            <label key={k}><input type="radio" name="decision" checked={choix === k} onChange={() => setChoix(k)} />{DECISIONS[k]}</label>
          ))}
        </fieldset>
        <div style={{ marginTop: 12 }}>
          <textarea aria-label="Justification" placeholder="Justification (obligatoire, 20 caractères au moins)" value={texte} onChange={(e) => setTexte(e.target.value)} />
        </div>
        <div style={{ marginTop: 8 }}>
          <input type="text" aria-label="Analyste" value={analyste} onChange={(e) => setAnalyste(e.target.value)} />
        </div>
        <div className="valider">
          <small>Inscrite dans la piste d'audit : qui, quand, pourquoi. Ne peut être ni modifiée ni effacée.</small>
          <button className="bouton" disabled={!pret}>Enregistrer</button>
        </div>
        {etat.erreur && <p className="erreur" role="alert">{etat.erreur}</p>}
        {etat.ok && <p className="succes" role="status">{etat.ok}</p>}
      </form>

      <div className="audit">
        <div className="titre"><span>Piste d'audit</span></div>
        <ul>
          {evenements.map((e, i) => (
            <li key={i} className={e.quoi === "decision" ? "decision" : ""}>
              <span>{momentLocal(e.quand)}</span>
              {QUOI[e.quoi]}{e.n ? ` ×${e.n}` : ""}{e.quoi !== "preuve" && ` · ${detail(e.quoi, e.detail)}`}
            </li>
          ))}
        </ul>
      </div>
    </>
  );
}
