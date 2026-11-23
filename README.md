# Détection du blanchiment en temps réel

Plateforme de streaming qui reçoit des opérations bancaires, les inscrit dans un grand livre en partie
double sans jamais compter deux fois le même euro, et (lots suivants) détecte les schémas de blanchiment
dans le graphe des flux.

> **État : lot 1 (socle) terminé.** Ingestion, grand livre, DLQ et outbox sont en place et vérifiés.
> La détection, le ML et l'interface viendront dans les lots suivants.

## Ce que fait le socle

```
 producteurs ─► transactions.raw ─► ingestion (Go) ─┬─► transactions.dlq   message intact + raison en en-tête
   (format Schema Registry)                         │
                                                    └─► Postgres, UNE transaction :
                                                        opération + 2 écritures comptables + outbox
                                                                    │
                                                      relais outbox ─► transactions.valid ─► (lots 2-4)
```

| Garantie | Comment | Vérifié par |
|---|---|---|
| Une opération n'est jamais comptée deux fois | Clé primaire `transaction_id` dans la même transaction SQL que les écritures ; offset Kafka validé **après** le commit | `TestDoublonsConcurrents` (50 copies simultanées → 1), bout en bout (100 copies → 1) |
| Un identifiant réutilisé avec un autre contenu est signalé | Empreinte SHA-256 des octets bruts du message comparée → DLQ `conflit_idempotence` | `TestConflitIdempotence` |
| Les comptes sont toujours équilibrés | Partie double ; contraintes **différées** vérifiées au COMMIT : exactement deux écritures par opération, dans sa devise, −montant au compte débité et +montant au compte crédité (une opération sans écriture échoue aussi) | `TestEquilibreVerifieAuCommit` (8 cas fautifs) |
| Le grand livre ne se réécrit pas | Triggers qui refusent `UPDATE`, `DELETE` et `TRUNCATE` ; rôle applicatif limité à `INSERT`/`SELECT` | `TestAjoutSeul` |
| Un message invalide ne bloque rien | Validation (format du registre, schéma, IBAN mod-97, devise ISO 4217, montant, JSON strict) → DLQ avec la raison et la position d'origine (`dlq.source`) | `TestRejetsVersDLQ`, `TestRegistryKnown`, bout en bout (20 corrompus → 20 en DLQ) |
| Une panne ne perd rien | Panne (base, registre, Kafka) → nouvel essai sur le même message, délai de 5 s par essai, offset jamais sauté | `TestPanneRenvoieErreur`, tests de panne ci-dessous |
| Aval cohérent avec le grand livre | Outbox publiée au moins une fois ; publication marquée seulement si Kafka a accusé réception | `TestOutboxPublieeUneFois` |

La DLQ est, comme l'outbox, **au moins une fois** : après une panne au mauvais moment, un même message
peut y figurer deux fois ; ses lecteurs dédoublonnent sur l'en-tête `dlq.source` (topic/partition/offset).

Les montants sont des **entiers en unités mineures** (centimes). Les soldes et l'écart de réconciliation
sont **calculés** à partir des écritures (`v_balances`, `v_reconciliation`), jamais stockés à part.
Pourquoi ces choix : [docs/decisions/001-idempotence-et-outbox.md](docs/decisions/001-idempotence-et-outbox.md).

## Tests de panne (2026-11-23, PC local)

| Scénario | Résultat observé |
|---|---|
| 1 000 opérations + 100 doublons + 20 messages corrompus | 1 001 écrites, 99 doublons absorbés, 20 en DLQ, écart 0 |
| Ingestion tuée (`docker kill`) en plein lot de 5 000 | 5 000/5 000 écrites ; 610 messages relus après la reprise, tous absorbés ; reprise en 10 s ; écart 0 |
| Postgres arrêté pendant l'envoi de 300 opérations | Message bloqué et réessayé toutes les 5 s (visible dans les journaux et `ingestion_reessais_total`) ; 300/300 écrites 2 s après le retour de la base ; écart 0 |

## Feuille de route

1. **Socle** — ingestion, grand livre, DLQ, outbox, tests de panne *(fait)*
2. **Données** — rejeu du jeu public IBM AMLworld (NeurIPS 2023, transactions synthétiques étiquetées), graphe Neo4j, réconciliation Postgres ↔ Neo4j
3. **Détection** — règles à fenêtres glissantes, cycles ordonnés dans le temps, alertes
4. **ML** — score LightGBM expliqué par SHAP, suivi MLflow, évaluation contre la référence publiée
5. **Interface** — poste de l'analyste conformité (file d'alertes, dossier, piste d'audit)
6. **Chaos et mesures** — scénarios de démonstration, test de charge

## Lancer

Prérequis : Docker, Go 1.26.

```bash
docker compose up -d --build           # Redpanda, Schema Registry, console, Postgres, ingestion
bash scripts/verifier_bout_en_bout.sh  # 1 000 + 100 doublons + 20 corrompus, puis contrôles
```

| Service | Adresse |
|---|---|
| Console Redpanda (topics, DLQ, schémas) | http://localhost:18088 |
| Métriques de l'ingestion | http://localhost:18090/metrics |
| Postgres (grand livre) | `localhost:15433`, base `grand_livre` |
| Kafka / Schema Registry | `localhost:19092` / http://localhost:18081 |

Envoyer des messages à la main :

```bash
cd ingestion
go run ./cmd/injecter -mode normal -n 1000    # virements et dépôts valides entre 200 comptes fictifs
go run ./cmd/injecter -mode doublons -n 100   # la même opération 100 fois
go run ./cmd/injecter -mode corrompus -n 20   # IBAN faux, devise inconnue, montant négatif, JSON tronqué, schéma absent ou inconnu
```

Réconciliation :

```sql
SELECT * FROM v_reconciliation;   -- ecart_minor et ecritures_manquantes doivent valoir 0
```

## Tests

```bash
cd ingestion
go test ./...                                                              # unitaires
TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/test go test ./...  # + intégration (base vide)
```

La CI (GitHub Actions) lance les tests unitaires et d'intégration sur un Postgres neuf, puis toute la
plateforme dans Docker Compose avec le script de bout en bout.

## Arborescence

```
schemas/transaction.v1.json   contrat du message (enregistré dans le Schema Registry, compatibilité BACKWARD)
db/001_grand_livre.sql        grand livre, contraintes, outbox, vues de réconciliation
ingestion/                    service Go (franz-go, pgx) + injecteur de test
scripts/                      vérification de bout en bout
docs/decisions/               choix d'architecture argumentés
```

Usage local uniquement : les mots de passe du Compose sont des valeurs de développement.
