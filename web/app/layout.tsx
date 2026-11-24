import type { Metadata } from "next";
import { Fraunces, IBM_Plex_Mono, Inter } from "next/font/google";
import "./globals.css";

const serif = Fraunces({ subsets: ["latin"], weight: ["400", "600"], variable: "--police-serif" });
const sans = Inter({ subsets: ["latin"], weight: ["400", "500", "600"], variable: "--police-sans" });
const mono = IBM_Plex_Mono({ subsets: ["latin"], weight: ["400", "500"], variable: "--police-mono" });

export const metadata: Metadata = {
  title: "Filature — poste de l'analyste",
  description: "Détection du blanchiment en temps réel : file d'alertes, dossiers, décisions.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="fr" className={`${serif.variable} ${sans.variable} ${mono.variable}`}>
      <body>{children}</body>
    </html>
  );
}
