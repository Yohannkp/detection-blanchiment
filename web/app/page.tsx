"use client";
// Poste de l'analyste : file d'alertes, dossier, décision. Lien partageable : ?alerte=<identifiant>.
import { useCallback, useEffect, useRef, useState } from "react";
import Chaos from "@/components/Chaos";
import Cote from "@/components/Cote";
import Dossier from "@/components/Dossier";
import FileAlertes, { type FiltreStatut } from "@/components/FileAlertes";
import Sante from "@/components/Sante";
import { STATIQUE, lireAlertes, lireDossier, lireSante, urlFlux, type AlerteResume, type Dossier as D, type Regle, type Sante as S, type Tri } from "@/lib/api";

export default function Poste() {
  const [sante, setSante] = useState<S | null>(null);
  const [direct, setDirect] = useState(false);
  const [alertes, setAlertes] = useState<AlerteResume[] | null>(null);
  const [regle, setRegle] = useState<Regle | null>(null);
  const [statut, setStatut] = useState<FiltreStatut>("a_traiter");
  const [tri, setTri] = useState<Tri>("score");
  const [choisie, setChoisie] = useState<string | null>(null);
  const [dossier, setDossier] = useState<D | null>(null);
  const [erreur, setErreur] = useState<string | null>(null);
  const [chaos, setChaos] = useState(false);
  const fermerChaos = useCallback(() => setChaos(false), []);
  const choisieRef = useRef<string | null>(null);
  choisieRef.current = choisie;

  // Un dossier n'est affiché que s'il est encore celui choisi : une réponse en retard (j/k enchaînés, réseau
  // lent) ne doit jamais remplacer le dossier courant, sinon la décision partirait sur la mauvaise alerte.
  const afficherDossier = useCallback(async (id: string) => {
    try {
      const d = await lireDossier(id);
      if (choisieRef.current === id) setDossier(d);
    } catch (e) {
      if (choisieRef.current === id) setErreur(`Dossier ${id} illisible : ${(e as Error).message}`);
    }
  }, []);

  const rafraichir = useCallback(async () => {
    try {
      const liste = await lireAlertes(regle, statut, tri);
      setAlertes(liste);
      setErreur(null);
      if (!choisieRef.current && liste[0]) setChoisie(liste[0].alert_id);
      if (choisieRef.current) await afficherDossier(choisieRef.current);
    } catch (e) {
      setErreur(`API injoignable (${(e as Error).message}). La plateforme est-elle lancée ?`);
    }
  }, [regle, statut, tri, afficherDossier]);
  const rafraichirRef = useRef(rafraichir);
  rafraichirRef.current = rafraichir;

  // Alerte demandée dans l'adresse, puis chargement de la file.
  useEffect(() => {
    const id = new URLSearchParams(window.location.search).get("alerte");
    if (id) setChoisie(id);
    lireSante().then(setSante).catch(() => {});
  }, []);
  useEffect(() => { rafraichir(); }, [rafraichir]);
  useEffect(() => {
    if (!choisie) return;
    setDossier(null);  // jamais l'ancien dossier (ni son formulaire) sous une autre alerte surlignée
    window.history.replaceState(null, "", `?alerte=${encodeURIComponent(choisie)}`);
    afficherDossier(choisie);
  }, [choisie, afficherDossier]);

  // Direct : santé toutes les 3 s, file et dossier rechargés quand une alerte, un score ou une décision change.
  useEffect(() => {
    if (STATIQUE) return;  // version en ligne : un instantané, pas de flux
    let ws: WebSocket | null = null, arret = false, minuterie: ReturnType<typeof setTimeout>;
    const connecter = () => {
      ws = new WebSocket(urlFlux());
      // À chaque (re)connexion on recharge : ce qui a changé pendant une coupure ne doit pas être perdu.
      ws.onopen = () => { setDirect(true); rafraichirRef.current(); };
      ws.onmessage = (m) => {
        const msg = JSON.parse(m.data);
        setSante(msg.sante);
        if (msg.maj) rafraichirRef.current();
      };
      ws.onclose = () => { setDirect(false); if (!arret) minuterie = setTimeout(connecter, 3000); };
    };
    connecter();
    return () => { arret = true; clearTimeout(minuterie); ws?.close(); };
  }, []);  // une seule connexion, même quand on change de filtre

  // Clavier : j / k pour passer d'une alerte à l'autre (hors saisie).
  useEffect(() => {
    const touche = (e: KeyboardEvent) => {
      if (!alertes?.length || (e.target as HTMLElement).closest("input, textarea")) return;
      const i = alertes.findIndex((a) => a.alert_id === choisie);
      if (e.key === "j") setChoisie(alertes[Math.min(alertes.length - 1, i + 1)].alert_id);
      if (e.key === "k") setChoisie(alertes[Math.max(0, i - 1)].alert_id);
    };
    window.addEventListener("keydown", touche);
    return () => window.removeEventListener("keydown", touche);
  }, [alertes, choisie]);

  return (
    <>
      <header className="bandeau">
        <div className="marque"><h1>Filature</h1><span>Poste de l'analyste conformité · lutte contre le blanchiment</span></div>
        <div className="droite">
          <Sante s={sante} direct={direct} />
          <button className="puce chaos-bouton" aria-pressed={chaos} onClick={() => setChaos((c) => !c)}>Chaos</button>
        </div>
      </header>
      {chaos && <Chaos sante={sante} fermer={fermerChaos} ouvrir={(id) => { setStatut("a_traiter"); setChoisie(id); }} />}
      <main className="poste">
        <section className="file">
          <FileAlertes alertes={alertes} choisie={choisie} choisir={setChoisie} regle={regle} setRegle={setRegle} statut={statut} setStatut={setStatut} tri={tri} setTri={setTri} />
        </section>
        <section className="dossier">
          {erreur && <p className="erreur" role="alert">{erreur}</p>}
          {dossier ? <Dossier key={dossier.alerte.alert_id} d={dossier} />
            : !erreur && <p className="chargement" role="status">{choisie ? "Chargement du dossier…" : "Choisissez une alerte."}</p>}
        </section>
        <section className="cote">
          {dossier && <Cote key={dossier.alerte.alert_id} d={dossier} apres={rafraichir} />}
        </section>
      </main>
    </>
  );
}
