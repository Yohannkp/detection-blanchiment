"use client";
import type { Sante as S } from "@/lib/api";

// Bandeau toujours visible : ce qu'un responsable veut savoir d'un coup d'œil sur la plateforme.
export default function Sante({ s, direct }: { s: S | null; direct: boolean }) {
  const nb = (v: number | null | undefined) => (v == null ? "—" : v.toLocaleString("fr-FR"));
  const items: [string, string, string?][] = s
    ? [
        s.devises_en_ecart === 0 && s.ecritures_manquantes === 0
          ? ["Grand livre", "équilibré", "ok"]
          : ["Grand livre", `${s.devises_en_ecart} devise(s) en écart · ${s.ecritures_manquantes} écriture(s) manquante(s)`, "ko"],
        ["Opérations", nb(s.operations)],
        s.graphe_en_retard == null ? ["Graphe", "injoignable", "ko"]
          : s.graphe_en_retard <= 0 ? ["Graphe", "à jour", "ok"] : ["Graphe", `${nb(s.graphe_en_retard)} en retard`, "attention"],
        ["Débit", s.debit ? `${s.debit.toLocaleString("fr-FR", { maximumFractionDigits: 1 })} op/s` : "—"],
        ["À traiter", `${nb(s.a_traiter)} / ${nb(s.alertes)}`],
        ["Latence p50", s.latence_p50_ms == null ? "—" : `${Math.round(s.latence_p50_ms)} ms`],
        ["En DLQ", nb(s.dlq)],
      ]
    : [];
  return (
    <>
      <dl className="sante" aria-label="Santé de la plateforme">
        {items.map(([t, v, c]) => (
          <div key={t} className={c ?? ""}><dt>{t}</dt><dd>{v}</dd></div>
        ))}
      </dl>
      {s?.instantane ? (
        <span className="direct" title="Version en ligne : instantané des vraies données de la plateforme">
          <i />instantané du {new Date(s.instantane).toLocaleDateString("fr-FR")}
        </span>
      ) : <span className={`direct ${direct ? "actif" : ""}`} title={direct ? "Mises à jour en direct" : "Connexion en direct interrompue"}>
        <i />{direct ? "en direct" : "hors ligne"}
      </span>}
    </>
  );
}
