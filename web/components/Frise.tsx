"use client";
// Frise du temps : chaque preuve est un point numéroté sur l'axe (taille = montant). Elle montre d'un
// coup d'œil les phases d'un montage (l'argent entre, puis repart quelques jours plus tard).
import { max, scaleSqrt, scaleUtc, utcDay, utcHour } from "d3";
import { useMemo } from "react";
import type { Operation } from "@/lib/api";
import { heure, jourMois } from "@/lib/format";

const L = 820, H = 92, Y = 48;

export default function Frise({ preuves, suspecte, actif, survol }: {
  preuves: Operation[]; suspecte?: string; actif: string | null; survol: (tx: string | null) => void;
}) {
  const { x, r, graduations, unJour } = useMemo(() => {
    const t = preuves.map((p) => new Date(p.booked_at).getTime());
    const marge = Math.max((Math.max(...t) - Math.min(...t)) * 0.04, 3 * 3600e3);
    const x = scaleUtc().domain([Math.min(...t) - marge, Math.max(...t) + marge]).range([16, L - 16]);
    const jours = utcDay.count(new Date(x.domain()[0]), new Date(x.domain()[1]));
    const graduations = jours > 1 ? x.ticks(utcDay.every(jours > 20 ? 7 : jours > 10 ? 2 : 1)!) : x.ticks(utcHour.every(6)!);
    const r = scaleSqrt().domain([0, max(preuves, (p) => p.amount_minor) ?? 1]).range([4, 13]);
    return { x, r, graduations, unJour: jours <= 1 };
  }, [preuves]);

  // Points proches : numéros alternés au-dessus / en dessous pour rester lisibles.
  let dernier = -Infinity, rangee = 0;
  return (
    <svg viewBox={`0 0 ${L} ${H}`} role="img" aria-label="Chronologie des opérations du dossier">
      <line x1={16} x2={L - 16} y1={Y} y2={Y} stroke="#DCD4C4" />
      {graduations.map((g) => (
        <g key={+g}>
          <line x1={x(g)} x2={x(g)} y1={Y - 4} y2={Y + 4} stroke="#B9B0A0" />
          <text x={x(g)} y={H - 4} textAnchor="middle" fontFamily="var(--mono)" fontSize={10.5} fill="#6B6558">{unJour ? heure(g.toISOString()) : jourMois(g)}</text>
        </g>
      ))}
      {preuves.map((p, i) => {
        const cx = x(new Date(p.booked_at)), rouge = p.transaction_id === suspecte;
        rangee = cx - dernier < 22 ? 1 - rangee : 0;
        dernier = cx;
        const eteint = actif != null && actif !== p.transaction_id;
        return (
          <g key={p.transaction_id} opacity={eteint ? 0.3 : 1} onMouseEnter={() => survol(p.transaction_id)} onMouseLeave={() => survol(null)}>
            <circle cx={cx} cy={Y} r={r(p.amount_minor)} fill={rouge ? "#A8321E" : "#1F1D1A"} fillOpacity={rouge ? 0.9 : 0.78} />
            <text x={cx} y={rangee ? Y + r(p.amount_minor) + 13 : Y - r(p.amount_minor) - 5} textAnchor="middle" fontFamily="var(--mono)"
                  fontSize={10.5} fontWeight={500} fill={rouge ? "#A8321E" : "#1F1D1A"}>{i + 1}</text>
          </g>
        );
      })}
    </svg>
  );
}
