"use client";
// Réseau du dossier : comptes des preuves (+ quelques voisins), flèches numérotées dans l'ordre
// chronologique (mêmes numéros que la liste des flux et la frise). Disposition d3-force calculée une
// fois, déterministe (positions initiales fixes, 400 pas).
import { forceCollide, forceLink, forceManyBody, forceSimulation, forceX, forceY, type SimulationNodeDatum } from "d3";
import { useMemo } from "react";
import type { Operation } from "@/lib/api";
import { ibanCourt } from "@/lib/format";

interface Noeud extends SimulationNodeDatum { id: string; role: "dossier" | "voisin"; origine: boolean }
interface Lien { source: Noeud | string; target: Noeud | string; preuve: boolean; numero: number; tx: string; rang: number; sens: number }

const L = 820, H = 420, MARGE = 52;

function disposer(preuves: Operation[], voisinage: Operation[], origine: string, premier: number) {
  const comptes = new Map<string, Noeud>();
  const ajouter = (id: string, role: Noeud["role"]) => { if (!comptes.has(id)) comptes.set(id, { id, role, origine: id === origine }); };
  preuves.forEach((p) => { ajouter(p.src, "dossier"); ajouter(p.dst, "dossier"); });
  // Voisins : seulement les opérations qui relient un compte du dossier à un compte extérieur.
  const voisins = voisinage.filter((v) => comptes.has(v.src) !== comptes.has(v.dst)).slice(0, preuves.length > 12 ? 4 : 10);
  voisins.forEach((v) => { ajouter(v.src, "voisin"); ajouter(v.dst, "voisin"); });
  const noeuds = [...comptes.values()];
  const liens: Lien[] = [
    ...preuves.map((p, i) => ({ source: p.src, target: p.dst, preuve: true, numero: premier + i, tx: p.transaction_id, rang: 0, sens: 1 })),
    ...voisins.map((v) => ({ source: v.src, target: v.dst, preuve: false, numero: 0, tx: v.transaction_id, rang: 0, sens: 1 })),
  ];
  const paires: Record<string, number> = {};
  liens.forEach((l) => {
    const a = l.source as string, b = l.target as string, k = [a, b].sort().join("|");
    l.rang = (paires[k] = (paires[k] ?? 0) + 1) - 1;
    l.sens = a < b ? 1 : -1;
  });
  const dossier = noeuds.filter((n) => n.role === "dossier").length;
  const distance = Math.min(L, H) * (dossier > 12 ? 0.32 : 0.55);
  noeuds.forEach((n, i) => { const a = (i / noeuds.length) * 2 * Math.PI; n.x = L / 2 + 140 * Math.cos(a); n.y = H / 2 + 140 * Math.sin(a); });
  const sim = forceSimulation(noeuds)
    .force("lien", forceLink<Noeud, Lien>(liens).id((n) => n.id).distance((l) => (l.preuve ? distance : 80)).strength((l) => (l.preuve ? 0.7 : 0.5)))
    .force("charge", forceManyBody<Noeud>().strength((n) => (n.role === "dossier" ? (dossier > 12 ? -900 : -2600) : -300)))
    .force("collision", forceCollide<Noeud>((n) => (n.role === "dossier" ? 46 : 20)))
    .force("x", forceX(L / 2).strength(0.03))
    .force("y", forceY(H / 2).strength(0.05))
    .stop();
  for (let i = 0; i < 400; i++) sim.tick();
  // Recentrer le dessin (la simulation peut dériver d'un côté), puis le garder dans le cadre.
  const xs = noeuds.map((n) => n.x!), ys = noeuds.map((n) => n.y!);
  const dx = L / 2 - (Math.min(...xs) + Math.max(...xs)) / 2, dy = H / 2 - (Math.min(...ys) + Math.max(...ys)) / 2;
  noeuds.forEach((n) => {
    n.x = Math.max(MARGE, Math.min(L - MARGE, n.x! + dx));
    n.y = Math.max(MARGE, Math.min(H - MARGE - 12, n.y! + dy));
  });
  return { noeuds, liens: liens as (Lien & { source: Noeud; target: Noeud })[] };
}

// Arc courbe entre deux nœuds, raccourci pour s'arrêter au bord du nœud d'arrivée.
function arc(l: { source: Noeud; target: Noeud; rang: number; sens: number }, r: number) {
  const s = l.source, t = l.target;
  const dx = t.x! - s.x!, dy = t.y! - s.y!;
  const courbe = (l.rang * 0.35 + 0.18) * l.sens;
  const mx = (s.x! + t.x!) / 2 - dy * courbe, my = (s.y! + t.y!) / 2 + dx * courbe;
  const bord = (ax: number, ay: number, bx: number, by: number) => {
    const d = Math.hypot(bx - ax, by - ay) || 1;
    return [bx - ((bx - ax) / d) * (r + 4), by - ((by - ay) / d) * (r + 4)];
  };
  const [ex, ey] = bord(mx, my, t.x!, t.y!), [sx, sy] = bord(mx, my, s.x!, s.y!);
  return { d: `M${sx},${sy} Q${mx},${my} ${ex},${ey}`, x: (s.x! + 2 * mx + t.x!) / 4, y: (s.y! + 2 * my + t.y!) / 4 };
}

// Les pastilles numérotées s'écartent les unes des autres et des nœuds (relaxation simple).
function ecarter(points: { x: number; y: number; ax: number; ay: number }[], obstacles: { x: number; y: number }[]) {
  for (let it = 0; it < 200; it++) {
    for (const a of points) {
      let fx = (a.ax - a.x) * 0.08, fy = (a.ay - a.y) * 0.08;
      for (const b of [...points, ...obstacles]) {
        if (a === b) continue;
        const dx = a.x - b.x, dy = a.y - b.y, d = Math.hypot(dx, dy) || 0.01, min = "ax" in b ? 24 : 34;
        if (d < min) { fx += (dx / d) * (min - d) * 0.5; fy += (dy / d) * (min - d) * 0.5; }
      }
      a.x += fx; a.y += fy;
    }
  }
}

export default function Graphe({ preuves, voisinage, origine, suspecte, actif, survol, premier = 1 }: {
  preuves: Operation[]; voisinage: Operation[]; origine: string; suspecte?: string;
  actif: string | null; survol: (tx: string | null) => void; premier?: number;
}) {
  // Disposition recalculée seulement si le dossier change vraiment (pas à chaque rafraîchissement).
  const cle = `${origine}|${premier}|${preuves.map((p) => p.transaction_id).join(",")}`;
  // Dossier chargé : nœuds plus petits, seul le compte d'origine nommé, numéros réservés à la liste.
  const compact = preuves.length > 25;
  const { noeuds, liens, pastilles } = useMemo(() => {
    const g = disposer(preuves, voisinage, origine, premier);
    const ps = g.liens.filter((l) => l.preuve).map((l) => { const a = arc(l, 16); return { l, d: a.d, x: a.x, y: a.y, ax: a.x, ay: a.y }; });
    ecarter(ps, g.noeuds.filter((n) => n.role === "dossier").map((n) => ({ x: n.x!, y: n.y! })));
    return { ...g, pastilles: ps };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cle]);

  return (
    <svg viewBox={`0 0 ${L} ${H}`} role="img" aria-label="Réseau des comptes du dossier">
      <defs>
        <marker id="fl" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,1 L9,5 L0,9 z" fill="#1F1D1A" /></marker>
        <marker id="flr" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,1 L9,5 L0,9 z" fill="#A8321E" /></marker>
        <marker id="flp" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0,1 L9,5 L0,9 z" fill="#B9B0A0" /></marker>
      </defs>
      {liens.filter((l) => !l.preuve).map((l) => (
        <path key={l.tx} d={arc(l, 6).d} fill="none" stroke="#C9C0AF" strokeWidth={1} strokeDasharray="3 4" markerEnd="url(#flp)" />
      ))}
      {pastilles.map(({ l, d }) => {
        const rouge = l.tx === suspecte, eteint = actif != null && actif !== l.tx;
        return <path key={l.tx} d={d} fill="none" stroke={rouge ? "#A8321E" : "#1F1D1A"} strokeWidth={actif === l.tx ? 2.6 : 1.6}
          opacity={eteint ? 0.25 : 1} markerEnd={`url(#${rouge ? "flr" : "fl"})`} />;
      })}
      {noeuds.filter((n) => n.role === "voisin").map((n) => (
        <circle key={n.id} cx={n.x} cy={n.y} r={5} fill="#F5F1E8" stroke="#B9B0A0" strokeWidth={1.2} />
      ))}
      {!compact && pastilles.map((p) => {
        const rouge = p.l.tx === suspecte;
        return (
          <g key={p.l.tx} transform={`translate(${p.x},${p.y})`} onMouseEnter={() => survol(p.l.tx)} onMouseLeave={() => survol(null)}
             style={{ cursor: "default" }} opacity={actif != null && actif !== p.l.tx ? 0.35 : 1}>
            {Math.hypot(p.x - p.ax, p.y - p.ay) > 10 && <line x1={0} y1={0} x2={p.ax - p.x} y2={p.ay - p.y} stroke="#B9B0A0" strokeWidth={0.8} />}
            <circle r={10} fill={rouge ? "#A8321E" : "#FBF9F4"} stroke={rouge ? "#A8321E" : "#1F1D1A"} />
            <text textAnchor="middle" dy={3.6} fontFamily="var(--mono)" fontSize={10.5} fontWeight={500} fill={rouge ? "#FBF9F4" : "#1F1D1A"}>{p.l.numero}</text>
          </g>
        );
      })}
      {noeuds.filter((n) => n.role === "dossier").map((n) => (
        <g key={n.id} transform={`translate(${n.x},${n.y})`}>
          <circle r={compact && !n.origine ? 7 : 16} fill={n.origine ? "#A8321E" : "#FBF9F4"} stroke={n.origine ? "#A8321E" : "#1F1D1A"} strokeWidth={1.6} />
          {(!compact || n.origine) && <text y={32} textAnchor="middle" fontFamily="var(--mono)" fontSize={11.5} fill="#1F1D1A">{ibanCourt(n.id)}</text>}
          {n.origine && <text y={-24} textAnchor="middle" fontSize={10} letterSpacing="0.08em" fill="#A8321E">ORIGINE</text>}
        </g>
      ))}
    </svg>
  );
}
