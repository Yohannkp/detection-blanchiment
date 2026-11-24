-- Alertes de détection. Une alerte = une règle déclenchée sur un compte pendant une période fixe
-- (96 h, 7 jours ou mois civil selon la règle) ; ses preuves s'ajoutent au fil du flux. Tout est en ajout seul :
-- la décision de l'analyste (lot 5) sera une ligne de plus, jamais une modification.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'detection') THEN
    CREATE ROLE detection LOGIN PASSWORD 'detection';  -- mot de passe de développement local
  END IF;
END $$;

CREATE TABLE alertes (
  alert_id    text        PRIMARY KEY,  -- déterministe (règle, compte, période) : ni le rejeu ni l'ordre d'arrivée ne dupliquent
  regle       text        NOT NULL CHECK (regle IN ('fan_in', 'fan_out', 'cycle', 'especes_fractionnees')),
  entite      text        NOT NULL,     -- compte au centre de l'alerte
  ouverte_le  timestamptz NOT NULL,     -- début de la période
  declencheur text        NOT NULL,     -- première opération traitée qui a fait franchir le seuil (indicatif)
  creee_le    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alertes_entite ON alertes (entite);

CREATE TABLE alertes_operations (
  alert_id       text        NOT NULL REFERENCES alertes (alert_id),
  transaction_id text        NOT NULL,
  signalee_le    timestamptz NOT NULL,  -- temps de l'événement auquel la règle a été vérifiée (évaluation honnête par période)
  ajoutee_le     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (alert_id, transaction_id, signalee_le)
);
CREATE INDEX alertes_operations_tx ON alertes_operations (transaction_id);

CREATE TRIGGER alertes_ajout_seul BEFORE UPDATE OR DELETE ON alertes
  FOR EACH ROW EXECUTE FUNCTION refuser_modification();
CREATE TRIGGER alertes_operations_ajout_seul BEFORE UPDATE OR DELETE ON alertes_operations
  FOR EACH ROW EXECUTE FUNCTION refuser_modification();

GRANT SELECT, INSERT ON alertes, alertes_operations TO detection;
GRANT INSERT ON outbox TO detection;  -- l'annonce d'une alerte part par l'outbox (topic alerts.aml)

-- … et seulement elle : le relais publie le topic écrit dans l'outbox ; sans ce garde-fou, un détecteur
-- compromis pourrait injecter des opérations sur transactions.valid, hors du grand livre.
CREATE FUNCTION outbox_topics_autorises() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF current_user = 'detection' AND NEW.topic <> 'alerts.aml' THEN
    RAISE EXCEPTION 'le rôle detection ne peut écrire que sur alerts.aml (topic demandé : %)', NEW.topic
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER outbox_topics_autorises BEFORE INSERT ON outbox
  FOR EACH ROW EXECUTE FUNCTION outbox_topics_autorises();
