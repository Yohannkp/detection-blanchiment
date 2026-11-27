"use client";
// Simuler un montage (version en ligne) : ses opérations passent une à une dans les règles de la plateforme,
// exécutées dans le navigateur ; les alertes levées entrent dans la file, avec leur dossier, à instruire.
import { useEffect, useRef, useState } from "react";
import { MONTAGES, CAISSE, ajouterSimulees, detecter, versApi, type Evenement, type Montage } from "@/lib/simulation";
import { REGLES, ibanCourt, jourMois, heure, montantCourt } from "@/lib/format";

const pause = (ms: number) => new Promise((r) => setTimeout(r, ms));

interface Deroule {
  m: Montage; hasard: boolean; vues: number; evenements: Evenement[]; fini: boolean;
  alertes: { id: string; regle: keyof typeof REGLES; entite: string; n: number }[];
}

export default function Simulation({ fermer, ouvrir, apres }: { fermer: () => void; ouvrir: (id: string) => void; apres: () => void }) {
  const [d, setD] = useState<Deroule | null>(null);
  const enCours = useRef(false);
  const vivant = useRef(true);
  useEffect(() => () => { vivant.current = false; }, []);
  useEffect(() => {
    const touche = (e: KeyboardEvent) => { if (e.key === "Escape") fermer(); };
    window.addEventListener("keydown", touche);
    return () => window.removeEventListener("keydown", touche);
  }, [fermer]);

  async function lancer(nom: string) {
    if (enCours.current) return;
    enCours.current = true;
    const cle = nom === "hasard" ? Object.keys(MONTAGES)[Math.floor(Math.random() * Object.keys(MONTAGES).length)] : nom;
    const m = MONTAGES[cle].fabrique(Math.random, Date.now());
    const { alertes, evenements } = detecter(m.ops);
    setD({ m, hasard: nom === "hasard", vues: 0, evenements: [], fini: false, alertes: [] });
    // Les opérations arrivent une à une ; chaque événement de détection s'affiche à l'opération qui l'a causé.
    const delai = Math.min(450, Math.max(25, 4500 / m.ops.length));
    for (let i = 0; i < m.ops.length && vivant.current; i++) {
      await pause(delai);
      setD((x) => x && { ...x, vues: i + 1, evenements: evenements.filter((e) => e.op <= i) });
    }
    const creeLe = new Date().toISOString();
    const pourApi = alertes.map((a) => versApi(a, m.ops, creeLe));
    ajouterSimulees(pourApi);
    setD((x) => x && { ...x, fini: true, alertes: pourApi.map(({ resume: r }) => ({ id: r.alert_id, regle: r.regle, entite: r.entite, n: r.n })) });
    enCours.current = false;
    apres();
  }

  const dernieres = d ? d.m.ops.slice(Math.max(0, d.vues - 5), d.vues).reverse() : [];
  return (
    <aside className="chaos simulation" aria-label="Simuler un montage">
      <div className="chaos-tete">
        <div><h2>Simuler un montage</h2>
          <p>Lancez un montage de blanchiment : ses opérations passent une à une dans les règles de détection de la plateforme
            (mêmes seuils, mêmes fenêtres de temps), exécutées ici, dans votre navigateur.</p></div>
        <button className="fermer" onClick={fermer} aria-label="Fermer la simulation">×</button>
      </div>

      <ul className="scenarios">
        {Object.entries(MONTAGES).map(([nom, x]) => (
          <li key={nom} className={nom === "paie" ? "panne" : ""}>
            <div><b>{x.titre}</b><small>{x.detail}</small></div>
            <button className="bouton petit" disabled={d != null && !d.fini} onClick={() => lancer(nom)}>Lancer</button>
          </li>
        ))}
        <li><div><b>Au hasard</b><small>Un des quatre, sans savoir lequel : à vous de deviner ce que la plateforme va repérer.</small></div>
          <button className="bouton petit" disabled={d != null && !d.fini} onClick={() => lancer("hasard")}>Lancer</button></li>
      </ul>

      {d && (
        <article className="essai deroule" aria-live="polite">
          <header><b>{d.fini || !d.hasard ? d.m.titre : "Montage tiré au hasard"}</b>
            <span className={`verdict ${d.fini ? "ok" : ""}`}>{d.fini ? "terminé" : "en cours"}</span></header>
          <p>{d.fini || !d.hasard ? d.m.resume : "Regardez les opérations arriver : que va repérer la plateforme ?"}</p>

          <div className="titre"><span>Opérations reçues</span><span className="mono">{d.vues} / {d.m.ops.length}</span></div>
          <div className="barre"><i style={{ width: `${(100 * d.vues) / d.m.ops.length}%`, background: "var(--encre)" }} /></div>
          <ul className="flux-sim">
            {dernieres.map((o) => (
              <li key={o.id} className="mono">
                <span>{jourMois(new Date(o.t * 1000))} {heure(new Date(o.t * 1000).toISOString())}</span>
                <span>{o.src === CAISSE ? "Espèces" : ibanCourt(o.src)} → {ibanCourt(o.dst)}</span>
                <span>{montantCourt(o.montant, o.devise)}</span>
              </li>
            ))}
          </ul>

          <div className="titre" style={{ marginTop: 14 }}><span>Détection</span></div>
          <ul>
            {d.evenements.length === 0 && <li className="attente"><i>·</i><span>aucune règle déclenchée pour l'instant</span><span /></li>}
            {d.evenements.map((e, i) => (
              <li key={i} className={e.ecart ? "attente" : "ok"}>
                <i aria-hidden>{e.ecart ? "–" : "✓"}</i>
                <span>{e.ecart
                  ? <>{REGLES[e.regle]} sur {ibanCourt(e.entite)} : {e.ecart}</>
                  : <>{REGLES[e.regle]} sur {ibanCourt(e.entite)} : {e.nouvelle ? "alerte levée" : "preuve ajoutée"}</>}</span>
                <span className="mono">op. {e.op + 1}</span>
              </li>
            ))}
          </ul>

          {d.fini && (
            <div className="bilan">
              {d.alertes.length === 0
                ? <p><b>Aucune alerte</b> : c'est le bon résultat. Une règle trop naïve aurait dérangé un analyste pour une simple paie.</p>
                : <>
                    <p><b>{d.alertes.length} alerte{d.alertes.length > 1 ? "s" : ""} en tête de file</b> (attendu : {d.m.attendu}). Ouvrez le dossier et décidez :</p>
                    <ul>
                      {d.alertes.map((a) => (
                        <li key={a.id}><button className="lien" onClick={() => ouvrir(a.id)}>{REGLES[a.regle]} sur {ibanCourt(a.entite)} · {a.n} op.</button></li>
                      ))}
                    </ul>
                  </>}
              <p className="explication">Le score du modèle LightGBM n'est pas calculé dans le navigateur : sur la plateforme complète, il note chaque alerte.</p>
            </div>
          )}
        </article>
      )}
    </aside>
  );
}
