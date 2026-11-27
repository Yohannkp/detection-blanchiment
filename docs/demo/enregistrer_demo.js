// Enregistre la démonstration de Filature : un parcours piloté, capturé image par image, avec curseur et
// sous-titres dans la charte de l'interface. Le passage « en direct » injecte un vrai montage dans la
// plateforme et attend qu'il apparaisse dans la file.
//
// Prérequis : plateforme lancée (docker compose up -d --build ; scoreur : --profile modele), rejeu AMLworld
// fait, injecteur compilé (cd ingestion && go build -o ../.context/injecter.exe ./cmd/injecter).
// Usage : node enregistrer_demo.js   puis   bash assembler.sh
const fs = require("fs");
const path = require("path");
const { execFile } = require("child_process");
const puppeteer = require("puppeteer-core");

const CHROME = process.env.CHROME ?? "C:/Program Files/Google/Chrome/Application/chrome.exe";
const URL = process.env.URL ?? "http://localhost:3100";
const BOUT = process.env.BOUT ?? "4128 43";  // fin d'IBAN du compte au centre d'un vrai cycle AMLworld à traiter (cycle-38221be6)
const INJECTEUR = path.resolve(__dirname, "../../.context/injecter.exe");
const IMAGES = path.join(__dirname, "images");
const L = 1600, H = 1000;

const pause = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  fs.rmSync(IMAGES, { recursive: true, force: true });
  fs.mkdirSync(IMAGES);
  const navigateur = await puppeteer.launch({
    executablePath: CHROME, headless: "new", defaultViewport: { width: L, height: H, deviceScaleFactor: 1 },
    args: ["--hide-scrollbars", "--lang=fr-FR", "--font-render-hinting=none"],
  });
  const page = await navigateur.newPage();
  await page.goto(URL, { waitUntil: "networkidle0" });
  await page.waitForSelector(".alerte");
  await pause(1500);

  // Sous-titre, curseur et surlignage, dans la charte (papier, encre, rouge brique).
  await page.evaluate(() => {
    const css = document.createElement("style");
    css.textContent = `
      #st-demo { position: fixed; left: 50%; bottom: 34px; top: auto; transform: translateX(-50%); z-index: 10000; max-width: 1100px;
        padding: 14px 26px; border-radius: 12px; background: rgba(31,29,26,.94); color: #F5F1E8; text-align: center;
        font: 500 21px/1.35 var(--police-sans), Inter, sans-serif; box-shadow: 0 14px 40px rgba(31,29,26,.28);
        transition: opacity .35s ease; pointer-events: none; }
      #st-demo.haut { top: 82px; bottom: auto; }
      #st-demo.gauche { left: 574px; max-width: 1000px; }  /* à gauche du tiroir Chaos (452 px) */
      #st-demo b { font: 600 23px var(--police-serif), Georgia, serif; color: #F2DDD6; }
      #curseur-demo { position: fixed; z-index: 10001; width: 22px; height: 22px; pointer-events: none; transform: translate(-3px, -2px);
        transition: left .45s cubic-bezier(.3,.7,.2,1), top .45s cubic-bezier(.3,.7,.2,1); }
      .surligne-demo { outline: 3px solid #A8321E !important; outline-offset: 4px; border-radius: 10px; transition: outline-color .3s; }`;
    document.head.appendChild(css);
    const st = document.createElement("div"); st.id = "st-demo"; st.style.opacity = "0"; document.body.appendChild(st);
    const c = document.createElement("div"); c.id = "curseur-demo";
    c.innerHTML = '<svg width="22" height="22" viewBox="0 0 22 22"><path d="M2 2 L2 18 L7 13.5 L10.5 20.5 L13.5 19 L10 12 L17 12 Z" fill="#1F1D1A" stroke="#FBF9F4" stroke-width="1.5" stroke-linejoin="round"/></svg>';
    c.style.left = "800px"; c.style.top = "520px"; document.body.appendChild(c);
  });
  // Sous-titre en bas, ou en haut (sous le bandeau) quand l'action se passe dans le bas de l'écran.
  const sousTitre = (html, haut = false, gauche = false) => page.evaluate((html, haut, gauche) => {
    const st = document.getElementById("st-demo"); st.innerHTML = html; st.style.opacity = html ? "1" : "0";
    st.classList.toggle("haut", haut); st.classList.toggle("gauche", gauche);
  }, html, haut, gauche);
  const surligner = (sel) => page.evaluate((sel) => {
    document.querySelectorAll(".surligne-demo").forEach((e) => e.classList.remove("surligne-demo"));
    if (sel) document.querySelector(sel)?.classList.add("surligne-demo");
  }, sel);
  // Le curseur visible glisse vers l'élément, puis la vraie souris s'y pose (survols réels de l'interface).
  async function aller(el, { cliquer = false, attente = 500 } = {}) {
    // Élément hors de l'écran (bas de la file) : on le fait d'abord défiler jusqu'au centre, sinon le clic tombe dans le vide.
    await el.evaluate((e) => e.scrollIntoView({ block: "center", behavior: "smooth" }));
    await pause(600);
    const b = await el.boundingBox();
    if (!b || b.y > H || b.y + b.height < 0) throw new Error("élément hors de l'écran");
    const x = b.x + Math.min(b.width / 2, 120), y = Math.min(Math.max(b.y + b.height / 2, 90), H - 90);
    await page.evaluate((x, y) => { const c = document.getElementById("curseur-demo"); c.style.left = x + "px"; c.style.top = y + "px"; }, x, y);
    await pause(480);
    await page.mouse.move(x, y);
    if (cliquer) await page.mouse.click(x, y);
    await pause(attente);
  }
  async function boutonTexte(texte) {
    for (const b of await page.$$("button, label")) if ((await b.evaluate((e) => e.textContent.trim())) === texte) return b;
    throw new Error(`bouton « ${texte} » introuvable`);
  }

  // Capture continue par le « screencast » de Chrome : une image à chaque rendu, horodatée par le
  // navigateur (bien plus fluide que des captures d'écran successives). L'assemblage respecte ces durées.
  const cdp = await page.createCDPSession();
  let n = 0, dernier = null;
  const durees = [];
  cdp.on("Page.screencastFrame", async ({ data, metadata, sessionId }) => {
    fs.writeFileSync(path.join(IMAGES, `${String(n).padStart(5, "0")}.jpg`), Buffer.from(data, "base64"));
    if (dernier != null) durees.push(Math.max(metadata.timestamp - dernier, 0.001));
    dernier = metadata.timestamp; n++;
    await cdp.send("Page.screencastFrameAck", { sessionId }).catch(() => {});
  });
  await cdp.send("Page.startScreencast", { format: "jpeg", quality: 90, maxWidth: L, maxHeight: H, everyNthFrame: 1 });

  // 1. Présentation
  await sousTitre("<b>Filature</b> · le poste de l'analyste anti-blanchiment");
  await pause(3200);
  await surligner(".sante");
  await sousTitre("En haut, la santé de la plateforme, en direct : grand livre équilibré, opérations, alertes à traiter");
  await pause(4200);
  await surligner(null);

  // 2. La file et le choix d'un dossier
  await sousTitre("La file : les alertes levées par les règles, triées par le score du modèle");
  await aller(await page.$(".file nav"), { attente: 1600 });
  await aller(await boutonTexte("Cycle"), { cliquer: true, attente: 1400 });
  const cible = await page.waitForFunction(
    (bout) => [...document.querySelectorAll(".alerte")].find((b) => b.querySelector(".regle")?.textContent === "Cycle" && b.textContent.includes(bout)), {}, BOUT);
  await aller(cible.asElement(), { cliquer: true, attente: 1800 });
  // Garde-fou : la suite (et la décision) ne doit porter que sur le dossier visé.
  await page.waitForFunction((bout) => document.querySelector(".entete")?.textContent.replace(/\s/g, "").includes(bout.replace(" ", "")), { timeout: 8000 }, BOUT);

  // 3. Le dossier : graphe, liste, frise
  await sousTitre("Le dossier : qui a envoyé combien à qui. Les flèches sont numérotées dans l'ordre du temps");
  await pause(2200);
  await sousTitre("Le dossier : qui a envoyé combien à qui. Les flèches sont numérotées dans l'ordre du temps", true);
  for (const ligne of (await page.$$(".flux tbody tr")).slice(0, 7)) await aller(ligne, { attente: 900 });
  await page.mouse.move(10, 990);
  await surligner(".frise");
  await sousTitre("La frise montre le rythme du montage : l'argent fait le tour de trois comptes en quelques jours", true);
  await pause(4200);
  await surligner(".cote > div:first-child");
  await sousTitre("Pourquoi ce score : les raisons du modèle, traduites en mots pour l'analyste");
  await pause(4600);
  await surligner(null);

  // 4. La décision
  await sousTitre("L'analyste décide : la justification est obligatoire, la décision ne peut plus être effacée");
  await aller(await boutonTexte("Escalader au niveau 2"), { cliquer: true, attente: 700 });
  const zone = await page.$("textarea");
  await aller(zone, { cliquer: true, attente: 300 });
  await page.keyboard.type("Boucle de trois comptes en quelques jours : l'argent revient à son point de départ. À approfondir.", { delay: 24 });
  await pause(600);
  await aller(await boutonTexte("Enregistrer"), { cliquer: true, attente: 1200 });
  await page.waitForFunction(() => document.querySelector(".etiquette-statut")?.textContent.includes("Escalad"), { timeout: 10000 });
  if (!(await page.evaluate((bout) => document.querySelector(".entete")?.textContent.includes("RÈGLE CYCLE"), BOUT))) throw new Error("le dossier affiché n'est pas le cycle visé");
  await surligner(".audit");
  await sousTitre("La décision entre dans la piste d'audit : qui, quand, pourquoi", true);
  await pause(3800);
  await surligner(null);

  // 5. Le direct
  await sousTitre("En direct : on injecte un nouveau montage dans la plateforme…");
  await aller(await boutonTexte("Récentes"), { cliquer: true, attente: 900 });
  const entite = await new Promise((ok, ko) => execFile(INJECTEUR, ["-mode", "cycle"], (e, sortie) =>
    e ? ko(e) : ok(/entite: (\S+)/.exec(sortie)?.[1])));
  const bout = entite.slice(-6, -2) + " " + entite.slice(-2);
  await page.waitForFunction((bout) => document.querySelector(".alerte")?.textContent.includes(bout), { timeout: 20000, polling: 200 }, bout);
  await sousTitre("…il traverse ingestion, graphe et détection, et apparaît en tête de file en quelques secondes");
  await surligner(".alerte");
  await pause(2600);
  await surligner(null);
  await aller(await page.$(".alerte"), { cliquer: true, attente: 2400 });
  await page.waitForFunction(() => !document.querySelector(".score-dossier small")?.textContent.includes("en cours"), { timeout: 15000 }).catch(() => {});
  await sousTitre("A → B → C → A en trois minutes : la règle de cycle l'a vu. Le modèle, appris sur AMLworld, le juge peu suspect : les deux se complètent");
  await pause(5200);

  // 6. La console Chaos : quatre essais, vérifiés par la console elle-même sur l'API.
  const st = (html) => sousTitre(html, false, true);
  await st("Et si on essayait de la piéger ? La console <b>Chaos</b> provoque attaques et pannes");
  await aller(await page.$(".chaos-bouton"));  // pas de clic par aller() : il borne y à 90 px, le bouton est au-dessus
  await page.click(".chaos-bouton");
  await pause(1400);
  const jouer = async (titre) => {
    const li = await page.waitForFunction((t) => [...document.querySelectorAll(".scenarios li")].find((l) => l.querySelector("b")?.textContent.startsWith(t)), {}, titre);
    await page.waitForFunction(() => [...document.querySelectorAll(".scenarios button")].every((b) => !b.disabled));
    await aller(await li.asElement().$("button"), { cliquer: true, attente: 400 });
  };
  const essai = (titre) => page.evaluate((t) => [...document.querySelectorAll(".essai")].find((e) => e.querySelector("header b").textContent.startsWith(t))?.querySelector(".verdict").textContent, titre);
  const voirJournal = () => page.evaluate(() => {
    const c = document.querySelector(".chaos"), j = document.querySelector(".essai");
    if (j) c.scrollTo({ top: j.offsetTop - 90, behavior: "smooth" });
  });

  await st("Schtroumpfage : six mules déposent des espèces sous le seuil de 10 000 €, puis virent tout au même compte");
  await jouer("Schtroumpfage");
  await page.waitForFunction(() => document.querySelector(".essai .verdict")?.textContent === "conforme", { timeout: 30000 });
  await voirJournal();
  await surligner(".essai");
  await st("Les sept alertes attendues sont levées : la console les vérifie une à une sur l'API");
  await pause(4200);
  await surligner(null);
  await page.evaluate(() => document.querySelector(".chaos").scrollTo({ top: 0, behavior: "smooth" }));
  await pause(700);

  await st("Le piège : une entreprise établie verse 80 salaires le même jour. Aucune alerte ne doit sortir");
  await jouer("Piège");
  await pause(2400);
  await st("100 fois la même opération, comme après une coupure réseau : elle ne doit compter qu'une fois");
  await jouer("100 doublons");
  await pause(2600);
  await st("Et une panne : le graphe s'arrête 20 secondes pendant que 300 opérations arrivent");
  await jouer("Panne");
  await pause(1500);
  await voirJournal();
  await page.waitForFunction(() => document.querySelector(".essai li.ok"), { timeout: 15000 });
  await surligner(".essai");
  await st("Le grand livre écrit tout. Le graphe, lui, prend du retard : 300 opérations lui manquent");
  await page.waitForFunction(() => [...document.querySelectorAll(".essai")][0]?.querySelectorAll("li.ok").length >= 2, { timeout: 20000 });
  await pause(3500);
  await st("Pendant la panne, la détection attend le graphe : les alertes seront retardées, jamais perdues");
  // « Fin de la panne » seulement quand le graphe a vraiment tout rattrapé (3e vérification de l'essai).
  await page.waitForFunction(() => [...document.querySelectorAll(".essai")][0]?.querySelectorAll("li.ok").length >= 3, { timeout: 60000, polling: 300 });
  await st("Fin de la panne : le graphe rattrape tout, sans rien perdre ni compter deux fois");
  await pause(3500);
  await page.waitForFunction(() => [...document.querySelectorAll(".essai .verdict")].slice(0, 4).every((v) => v.textContent === "conforme"), { timeout: 90000, polling: 300 });
  await surligner(null);
  await page.evaluate(() => document.querySelector(".chaos").scrollTo({ top: 1e5, behavior: "smooth" }));
  await pause(800);
  for (const t of ["Schtroumpfage", "Piège", "100 doublons", "Panne"]) if ((await essai(t)) !== "conforme") throw new Error(`essai ${t} : ${await essai(t)}`);
  await st("Quatre essais, quatre verdicts <b>conformes</b>, vérifiés opération par opération");
  await pause(4800);

  // 7. Fin
  await page.mouse.move(10, 990);
  await page.evaluate(() => document.querySelector(".fermer")?.click());
  await pause(400);
  await sousTitre("<b>Filature</b> · Go · Kafka · Postgres · Neo4j · Redis · LightGBM · FastAPI · Next.js<br><span style='font-size:17px;opacity:.85'>github.com/Yohannkp/detection-blanchiment</span>");
  await pause(4500);

  await cdp.send("Page.stopScreencast");
  durees.push(Math.max(Date.now() / 1000 - dernier, 1 / 30));  // la dernière image dure jusqu'à l'arrêt (pauses finales)
  fs.writeFileSync(path.join(IMAGES, "durees.json"), JSON.stringify(durees));
  console.log(`${n} images, ${durees.reduce((a, b) => a + b, 0).toFixed(1)} s`);
  await navigateur.close();
})().catch((e) => { console.error(e); process.exit(1); });
