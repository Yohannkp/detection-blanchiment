-- Vérité terrain du jeu AMLworld (étiquette « blanchiment » de chaque opération rejouée).
-- Schéma séparé, SANS droit pour le rôle applicatif : la détection ne doit jamais pouvoir lire
-- les réponses. Seule l'évaluation (lot 4) et les contrôles y accèdent.
CREATE SCHEMA evaluation;
REVOKE ALL ON SCHEMA evaluation FROM PUBLIC;

CREATE TABLE evaluation.verite_terrain (
  transaction_id text    PRIMARY KEY,
  is_laundering  boolean NOT NULL
);
