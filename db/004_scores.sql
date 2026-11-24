-- Scores du modèle (lot 4) : une ligne par (alerte, nombre de preuves, version du modèle). Une alerte qui
-- s'enrichit de preuves, ou un nouveau modèle publié, déclenche un nouveau score ; l'historique reste.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'scoreur') THEN
    CREATE ROLE scoreur LOGIN PASSWORD 'scoreur';  -- mot de passe de développement local
  END IF;
END $$;

CREATE TABLE alertes_scores (
  alert_id       text        NOT NULL REFERENCES alertes (alert_id),
  preuves        integer     NOT NULL,  -- nombre d'opérations en preuve au moment du calcul
  score          real        NOT NULL CHECK (score BETWEEN 0 AND 1),  -- max des probabilités des preuves
  transaction_id text        NOT NULL,  -- preuve la plus suspecte selon le modèle
  raisons        jsonb       NOT NULL,  -- 3 variables qui poussent le plus le score (contributions SHAP)
  modele         text        NOT NULL,  -- version du modèle (registre MLflow)
  calcule_le     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (alert_id, preuves, modele)
);

CREATE TRIGGER alertes_scores_ajout_seul BEFORE UPDATE OR DELETE ON alertes_scores
  FOR EACH ROW EXECUTE FUNCTION refuser_modification();

GRANT SELECT ON transactions, alertes, alertes_operations TO scoreur;
GRANT SELECT, INSERT ON alertes_scores TO scoreur;
