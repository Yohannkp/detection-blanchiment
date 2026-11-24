-- Décisions de l'analyste (lot 5) et rôle de l'API. Ajout seul : changer d'avis, c'est ajouter une
-- décision ; le statut d'une alerte est sa dernière décision. Chaque décision dit qui, quand et pourquoi.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'api') THEN
    CREATE ROLE api LOGIN PASSWORD 'api';  -- mot de passe de développement local
  END IF;
END $$;

CREATE TABLE decisions (
  decision_id   bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  alert_id      text        NOT NULL REFERENCES alertes (alert_id),
  decision      text        NOT NULL CHECK (decision IN ('sans_suite', 'escalade', 'soupcon')),
  justification text        NOT NULL CHECK (length(btrim(justification)) >= 20),  -- une vraie phrase, pas « ok »
  analyste      text        NOT NULL CHECK (length(btrim(analyste)) > 0),
  decidee_le    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX decisions_alerte ON decisions (alert_id, decidee_le DESC);

CREATE TRIGGER decisions_ajout_seul BEFORE UPDATE OR DELETE ON decisions
  FOR EACH ROW EXECUTE FUNCTION refuser_modification();
CREATE TRIGGER decisions_sans_truncate BEFORE TRUNCATE ON decisions
  FOR EACH STATEMENT EXECUTE FUNCTION refuser_modification();

-- Index pour instruire un dossier (voisinage des comptes) et calculer le débit sans parcourir toute la table.
CREATE INDEX IF NOT EXISTS transactions_debiteur ON transactions (debtor_account, booked_at);
CREATE INDEX IF NOT EXISTS transactions_crediteur ON transactions (creditor_account, booked_at);
CREATE INDEX IF NOT EXISTS transactions_ingestion ON transactions (ingested_at);

-- L'API lit tout ce qui sert à instruire un dossier, n'écrit que des décisions, et ne voit pas la vérité terrain.
GRANT SELECT ON transactions, alertes, alertes_operations, alertes_scores, outbox, v_reconciliation TO api;
GRANT SELECT, INSERT ON decisions TO api;
