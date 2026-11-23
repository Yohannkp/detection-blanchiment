# 001 — Idempotence par la base, outbox au lieu de la double écriture

Date : 2026-11-23 · Statut : adoptée

## Contexte

Kafka livre « au moins une fois » : après une panne ou une relance réseau, un message peut être lu
deux fois. Une opération bancaire comptée deux fois fausse les soldes. Il faut aussi alimenter plusieurs
systèmes en aval (graphe Neo4j, détection, ML) sans qu'ils divergent du grand livre.

## Options écartées

- **Déduplication par Redis (`SETNX` + TTL)** : un doublon qui arrive après l'expiration de la clé passe ;
  un plantage entre `SETNX` et l'écriture en base perd le message (Redis le croit traité). Deux systèmes,
  aucune atomicité entre eux.
- **Écrire en parallèle dans Postgres et Neo4j** : si l'une des deux écritures échoue, les bases divergent,
  et rien ne permet de savoir laquelle a raison.
- **Transactions Kafka (exactly-once)** : elles couvrent Kafka → Kafka, pas l'écriture dans Postgres.

## Décision

1. Postgres est la **source de vérité**. Une seule transaction SQL écrit l'opération (clé primaire
   `transaction_id`, `ON CONFLICT DO NOTHING`), ses deux écritures comptables et une ligne d'**outbox**.
2. L'offset Kafka n'est validé **qu'après** le commit SQL. Un message relu après une panne bute sur la clé
   primaire : il est absorbé sans effet.
3. Un même `transaction_id` reçu avec un **autre contenu** n'est pas un doublon : c'est un conflit, envoyé en
   DLQ. L'empreinte SHA-256 porte sur les **octets bruts** du message : un renvoi réseau est identique octet
   pour octet, alors que deux messages qui ne diffèrent que par un champ ignoré au décodage ne le sont pas.
4. Un relais publie l'outbox vers `transactions.valid` (au moins une fois). Chaque consommateur aval est
   idempotent sur `transaction_id`.
5. Erreur de contenu → DLQ (le relire ne servirait à rien). Panne (base, registre, Kafka) → nouvel essai sur
   le même message, sans jamais avancer l'offset.

## Conséquences

- Mesuré le 2026-11-23 (`scripts/verifier_bout_en_bout.sh` et tests de panne manuels) : 100 copies → 1
  opération ; ingestion tuée en plein lot de 5 000 → 610 messages relus et absorbés, 5 000/5 000 écrits,
  écart 0 ; Postgres coupé → message bloqué et réessayé toutes les 5 s, aucune perte.
- Coût : une transaction SQL par message. Si le débit visé l'exige, on regroupera plusieurs messages par
  transaction (une savepoint par message).
- Redis reste utile pour l'**état des fenêtres** de la détection (lot 3), pas pour garantir l'unicité.
