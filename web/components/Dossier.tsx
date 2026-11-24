"use client";
import { useState } from "react";
import type { Dossier as D } from "@/lib/api";
import { REGLES, REGLES_LONG, STATUTS, codeDossier, date, heure, iban, ibanCourt, montant, score } from "@/lib/format";
import Frise from "./Frise";
import Graphe from "./Graphe";

export default function Dossier({ d }: { d: D }) {
  const [actif, survol] = useState<string | null>(null);
  const a = d.alerte, p = d.preuves, suspecte = d.score?.tx_suspecte;
  // Au-delà de 60 opérations, un graphe n'est plus lisible (et coûte cher à disposer) : on dessine les 60
  // plus récentes, en le disant ; la frise et la liste restent complètes.
  const MAX = 60, dessinees = p.length > MAX ? p.slice(-MAX) : p;
  const devises = [...new Set(p.map((x) => x.currency))];
  const total = devises.length === 1 ? montant(p.reduce((s, x) => s + x.amount_minor, 0), devises[0]) : `${devises.length} devises`;
  const statut = d.decisions.at(-1)?.decision ?? "a_traiter";
  const b = (c: string) => (c === a.entite ? <b>{ibanCourt(c)}</b> : ibanCourt(c));

  return (
    <>
      <div className="entete">
        <div>
          <div className="code">
            DOSSIER {codeDossier(a.alert_id)} · RÈGLE {REGLES[a.regle].toUpperCase()} · PÉRIODE DU {date(a.ouverte_le)}
            <span className="etiquette-statut">{STATUTS[statut]}</span>
          </div>
          <h2>{REGLES_LONG[a.regle]}</h2>
          <p>Compte au centre <span className="mono">{iban(a.entite)}</span></p>
          <p>{p.length} opérations · <span className="mono">{total}</span> · du {date(p[0].booked_at)} au {date(p[p.length - 1].booked_at)}</p>
        </div>
        <div className="score-dossier">
          <b>{score(d.score?.score ?? null)}</b>
          <small>{d.score ? `probabilité estimée · ${d.score.modele.replace("detection-blanchiment-lgbm-", "modèle de ")}` : "score en cours de calcul"}</small>
        </div>
      </div>

      <div className="graphe">
        <Graphe preuves={dessinees} premier={p.length - dessinees.length + 1} voisinage={d.voisinage} origine={a.entite} suspecte={suspecte} actif={actif} survol={survol} />
        {p.length > MAX && <p className="explication">Graphe limité aux {MAX} opérations les plus récentes sur {p.length} ; la frise et la liste sont complètes.</p>}
      </div>
      <div className="bas">
        <div className="frise">
          <div className="titre"><span>Frise du temps</span><span>{date(p[0].booked_at)} → {date(p[p.length - 1].booked_at)}</span></div>
          <Frise preuves={p} suspecte={suspecte} actif={actif} survol={survol} />
        </div>
        <div>
          <div className="titre"><span>Flux de l'argent</span><span>en rouge : l'opération la plus suspecte selon le modèle</span></div>
          <table className="flux">
            <thead><tr><th>N°</th><th>Date</th><th>De → vers</th><th>Moyen</th><th style={{ textAlign: "right" }}>Montant</th></tr></thead>
            <tbody>
              {p.map((x, i) => (
                <tr key={x.transaction_id} data-actif={actif === x.transaction_id} onMouseEnter={() => survol(x.transaction_id)} onMouseLeave={() => survol(null)}>
                  <td><span className={`num ${x.transaction_id === suspecte ? "suspecte" : ""}`}>{i + 1}</span></td>
                  <td className="mono">{date(x.booked_at)} {heure(x.booked_at)}</td>
                  <td className="c">{b(x.src)} → {b(x.dst)}</td>
                  <td>{(x.payment_format ?? "—").toUpperCase()}</td>
                  <td className="m">{montant(x.amount_minor, x.currency)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}
