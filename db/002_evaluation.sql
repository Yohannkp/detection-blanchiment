-- Vérité terrain du jeu AMLworld (étiquette « blanchiment » de chaque opération rejouée, et son
-- motif quand le fichier des motifs le donne). Schéma séparé, SANS droit pour les rôles applicatifs :
-- la détection ne doit jamais pouvoir lire les réponses. Seule l'évaluation y accède.
CREATE SCHEMA evaluation;
REVOKE ALL ON SCHEMA evaluation FROM PUBLIC;

CREATE TABLE evaluation.verite_terrain (
  transaction_id text    PRIMARY KEY,
  is_laundering  boolean NOT NULL,
  motif          text,     -- fan-in, cycle, scatter-gather… ; NULL si licite ou hors motif
  tentative      integer   -- n° de la tentative de blanchiment dans le fichier des motifs
);
