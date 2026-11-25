// Capture de la console Chaos : joue tous les scénarios et la panne du graph-sink depuis l'interface,
// attend que chaque vérification soit tranchée, puis photographie l'écran (docs/captures/filature-chaos.png).
// Prérequis : plateforme lancée (docker compose up -d --build). Usage : node capturer_chaos.js
const path = require("path");
const puppeteer = require("puppeteer-core");

const CHROME = process.env.CHROME ?? "C:/Program Files/Google/Chrome/Application/chrome.exe";
const URL = process.env.URL ?? "http://localhost:3100";
const SORTIE = path.resolve(__dirname, "../captures/filature-chaos.png");
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  const navigateur = await puppeteer.launch({
    executablePath: CHROME, headless: "new", defaultViewport: { width: 1600, height: 1000, deviceScaleFactor: 2 },
    args: ["--hide-scrollbars", "--lang=fr-FR", "--font-render-hinting=none"],
  });
  try {
    const page = await navigateur.newPage();
    await page.goto(URL, { waitUntil: "networkidle0" });
    await page.click(".chaos-bouton");
    await page.waitForSelector(".scenarios");
    // La panne en dernier : pendant la panne, la détection attend le graphe et fausserait les délais des autres.
    for (const i of [0, 1, 2, 3, 4, 5]) {
      await page.waitForFunction(() => [...document.querySelectorAll(".scenarios button")].every((b) => !b.disabled));
      const boutons = await page.$$(".scenarios button");
      await boutons[i].click();
      await pause(1200);
    }
    // Le piège (paie) attend 30 s de silence, la panne 20 s de pause : tout doit être tranché avant la photo.
    await page.waitForFunction(() => !document.querySelector(".essai .verdict:not(.ok):not(.ko)"), { timeout: 150_000 });
    const verdicts = await page.$$eval(".essai", (es) => es.map((e) => e.querySelector("header").innerText.replace(/\n/g, " ")));
    console.log(verdicts.join("\n"));
    await page.screenshot({ path: SORTIE });
    console.log("capture :", SORTIE);
  } finally {
    await navigateur.close();
  }
})();
