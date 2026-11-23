-- Grand livre en partie double, en ajout seul.
-- Exécuté une fois par Postgres à la création du volume (docker-entrypoint-initdb.d),
-- et par les tests d'intégration sur une base vide.

-- Rôle applicatif : il insère et lit, il ne modifie ni ne supprime jamais une écriture.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ingestion') THEN
    CREATE ROLE ingestion LOGIN PASSWORD 'ingestion';  -- mot de passe de développement local
  END IF;
END $$;

-- Une ligne par opération reçue. transaction_id = clé d'idempotence : la garantie
-- « une seule fois » repose sur cette clé primaire, dans la même transaction SQL que les écritures.
CREATE TABLE transactions (
  transaction_id   text        PRIMARY KEY,
  booked_at        timestamptz NOT NULL,
  payment_type     text        NOT NULL CHECK (payment_type IN ('transfer', 'cash_deposit', 'cash_withdrawal')),
  debtor_account   text        NOT NULL,
  creditor_account text        NOT NULL CHECK (creditor_account <> debtor_account),
  amount_minor     bigint      NOT NULL CHECK (amount_minor > 0),
  currency         char(3)     NOT NULL,
  payload_sha256   bytea       NOT NULL,  -- empreinte du message : distingue un doublon d'un conflit
  source_partition int         NOT NULL,
  source_offset    bigint      NOT NULL,
  ingested_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX transactions_booked_at ON transactions (booked_at);

-- Partie double : chaque opération produit exactement deux écritures de signes opposés
-- (débit négatif, crédit positif). La somme de toutes les écritures d'une devise vaut donc 0.
CREATE TABLE ledger_entries (
  entry_id       bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  transaction_id text    NOT NULL REFERENCES transactions (transaction_id),
  account        text    NOT NULL,
  currency       char(3) NOT NULL,
  amount_minor   bigint  NOT NULL CHECK (amount_minor <> 0)
);
CREATE INDEX ledger_entries_account ON ledger_entries (account, currency);
CREATE INDEX ledger_entries_transaction ON ledger_entries (transaction_id);

-- Ajout seul : toute modification passe par une écriture d'extourne, jamais par UPDATE/DELETE.
CREATE FUNCTION refuser_modification() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'grand livre en ajout seul : % interdit sur %', TG_OP, TG_TABLE_NAME
    USING ERRCODE = 'insufficient_privilege';
END $$;

CREATE TRIGGER transactions_ajout_seul BEFORE UPDATE OR DELETE ON transactions
  FOR EACH ROW EXECUTE FUNCTION refuser_modification();
CREATE TRIGGER transactions_sans_truncate BEFORE TRUNCATE ON transactions
  FOR EACH STATEMENT EXECUTE FUNCTION refuser_modification();
CREATE TRIGGER ledger_entries_ajout_seul BEFORE UPDATE OR DELETE ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION refuser_modification();
CREATE TRIGGER ledger_entries_sans_truncate BEFORE TRUNCATE ON ledger_entries
  FOR EACH STATEMENT EXECUTE FUNCTION refuser_modification();

-- Équilibre vérifié au COMMIT (contraintes différées) : chaque opération a exactement deux
-- écritures, dans sa devise, −montant sur le compte débité et +montant sur le compte crédité.
-- Vérifié depuis les deux tables : une opération sans écriture échoue aussi.
CREATE FUNCTION verifier_equilibre() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  t      transactions%ROWTYPE;
  jambes int;
  debit  int;
  credit int;
BEGIN
  SELECT * INTO t FROM transactions WHERE transaction_id = NEW.transaction_id;
  SELECT count(*),
         count(*) FILTER (WHERE e.currency = t.currency AND e.account = t.debtor_account
                            AND e.amount_minor = -t.amount_minor),
         count(*) FILTER (WHERE e.currency = t.currency AND e.account = t.creditor_account
                            AND e.amount_minor = t.amount_minor)
    INTO jambes, debit, credit
    FROM ledger_entries e WHERE e.transaction_id = NEW.transaction_id;
  IF t.transaction_id IS NULL OR jambes <> 2 OR debit <> 1 OR credit <> 1 THEN
    RAISE EXCEPTION 'opération % déséquilibrée : % écriture(s), débit conforme %, crédit conforme %',
      NEW.transaction_id, jambes, debit, credit
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER ledger_entries_equilibre AFTER INSERT ON ledger_entries
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verifier_equilibre();
CREATE CONSTRAINT TRIGGER transactions_equilibre AFTER INSERT ON transactions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verifier_equilibre();

-- Outbox : écrite dans la même transaction que le grand livre, publiée ensuite vers
-- transactions.valid par le relais (au moins une fois ; les consommateurs sont idempotents).
CREATE TABLE outbox (
  outbox_id    bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  topic        text        NOT NULL,
  message_key  text        NOT NULL,
  payload      jsonb       NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX outbox_a_publier ON outbox (outbox_id) WHERE published_at IS NULL;

-- Soldes et réconciliation : calculés depuis les écritures, jamais stockés à part.
CREATE VIEW v_balances AS
  SELECT account, currency, sum(amount_minor) AS balance_minor, count(*) AS entries
    FROM ledger_entries GROUP BY account, currency;

-- Écart de réconciliation par devise (devise des écritures) : ecart_minor et ecritures_manquantes
-- doivent valoir 0. C'est ce chiffre que l'interface affiche.
CREATE VIEW v_reconciliation AS
  WITH e AS (SELECT currency, sum(amount_minor) AS somme, count(*) AS n FROM ledger_entries GROUP BY currency),
       t AS (SELECT currency, count(*) AS n FROM transactions GROUP BY currency)
  SELECT coalesce(t.currency, e.currency)        AS currency,
         coalesce(t.n, 0)                        AS transactions,
         coalesce(e.somme, 0)                    AS ecart_minor,
         2 * coalesce(t.n, 0) - coalesce(e.n, 0) AS ecritures_manquantes
    FROM t FULL JOIN e ON e.currency = t.currency;

GRANT SELECT, INSERT ON transactions, ledger_entries TO ingestion;
GRANT SELECT, INSERT ON outbox TO ingestion;
GRANT UPDATE (published_at) ON outbox TO ingestion;  -- le contenu d'un message à publier ne se réécrit pas
GRANT SELECT ON v_balances, v_reconciliation TO ingestion;
