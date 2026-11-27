import type { NextConfig } from "next";

// Version en ligne (GitHub Pages) : site entièrement statique, servi sous /detection-blanchiment.
const statique = process.env.NEXT_PUBLIC_STATIQUE === "1";
const config: NextConfig = statique
  ? { output: "export", basePath: process.env.NEXT_PUBLIC_BASE_PATH ?? "", trailingSlash: true }
  : { output: "standalone", poweredByHeader: false };
export default config;
