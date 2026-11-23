# Détection du blanchiment en temps réel

Plateforme de streaming qui reçoit des opérations bancaires, les inscrit dans un grand livre en partie
double sans jamais compter deux fois le même euro, et (lots suivants) détecte les schémas de blanchiment
dans le graphe des flux.

> **État : lots 1 (socle) et 2 (données, graphe) terminés.** 176 060 opérations du jeu public IBM AMLworld
> passent par la plateforme jusqu'au graphe Neo4j, avec un grand livre et un graphe qui concordent au centime.
> La détection, le ML et l'interface viendront dans les lots suivants.

## Ce que fait le socle

```
 producteurs ─► transactions.raw ─► ingestion (Go) ─┬─► transactions.dlq   message intact + raison en en-tête
   (format Schema Registry)                         │
                                                    └─► Postgres, UNE transaction :
                                                        opération + 2 écritures comptables + outbox
                                                                    │
                                                      relais outbox ─► transactions.valid
                                                                                    │
                                              graph-sink (Go, MERGE idempotent) ─► Neo4j ─► (détection, lot 3)
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
| Le graphe ne diverge pas du grand livre | Graph-sink idempotent (contraintes d'unicité Neo4j sur le compte et sur `transaction_id`) ; `reconcilier` compare, devise par devise, nombre d'opérations et sommes | Bout en bout (CI), rejeu complet, test de panne ci-dessous |
| Le contrat évolue sans casser les producteurs | Schema Registry en compatibilité BACKWARD : v2 ajoute `payment_format` (facultatif) ; une version incompatible est refusée au démarrage | Démarrage du Compose (v1 puis v2 enregistrées) |

La DLQ est, comme l'outbox, **au moins une fois** : après une panne au mauvais moment, un même message
peut y figurer deux fois ; ses lecteurs dédoublonnent sur l'en-tête `dlq.source` (topic/partition/offset).

Les montants sont des **entiers en unités mineures** (centimes). Les soldes et l'écart de réconciliation
sont **calculés** à partir des écritures (`v_balances`, `v_reconciliation`), jamais stockés à part.
Pourquoi ces choix : [docs/decisions/001-idempotence-et-outbox.md](docs/decisions/001-idempotence-et-outbox.md).

## Données : IBM AMLworld

Jeu public de transactions **synthétiques étiquetées** (IBM, NeurIPS 2023, licence CDLA-Sharing-1.0) : un
monde multi-agents où certains acteurs blanchissent selon 8 motifs (fan-in, fan-out, cycles…). Aucune
personne réelle. `outils/preparer_amlworld.py` transforme le CSV Kaggle en messages du contrat : IBAN fictifs
valides, montants exacts en unités mineures, identifiants déterministes, **étiquettes tenues hors du flux**
(schéma Postgres `evaluation`, illisible par le rôle applicatif). Détails et écarts assumés :
[docs/decisions/002-donnees-amlworld.md](docs/decisions/002-donnees-amlworld.md).

| HI-Small | Valeur |
|---|---|
| Transactions source / illicites | 5 078 345 / 5 177 |
| Sous-graphe rejoué (comptes illicites + 25 % de leurs voisins) | 20 768 comptes, 176 060 opérations, 5 166 illicites |
| Écartées | 591 212 autovirements (11 illicites), 2 191 opérations avec change (0 illicite) |

```bash
pip install -r requirements.txt
python outils/preparer_amlworld.py data/HI-Small_Trans.csv --sortie data --voisins 25
cd ingestion && go run ./cmd/rejouer -debit 200          # horodatages d'origine conservés
cd .. && bash scripts/charger_verite.sh                   # étiquettes → evaluation.verite_terrain
cd ingestion && go run ./cmd/reconcilier -attendre 60s    # grand livre ↔ graphe
```

## Mesures et tests de panne (2026-11-23, PC portable, Docker Desktop)

| Scénario | Résultat observé |
|---|---|
| 1 000 opérations + 100 doublons + 20 messages corrompus | 1 001 écrites, 99 doublons absorbés, 20 en DLQ, écart 0 |
| Ingestion tuée (`docker kill`) en plein lot de 5 000 | 5 000/5 000 écrites ; 610 messages relus après la reprise, tous absorbés ; reprise en 10 s ; écart 0 |
| Postgres arrêté pendant l'envoi de 300 opérations | Message bloqué et réessayé toutes les 5 s (visible dans les journaux et `ingestion_reessais_total`) ; 300/300 écrites 2 s après le retour de la base ; écart 0 |
| Graph-sink tué en plein envoi de 8 000 opérations | Réconciliation en écart (3 694 opérations manquantes dans le graphe) pendant la panne ; rattrapage en 9 s après la relance ; écart 0 |
| Rejeu complet AMLworld (176 060 opérations, envoi au plus vite) | Toutes écrites, 0 en DLQ ; grand livre équilibré ; graphe concordant ; **≈ 320 opérations/s** de bout en bout (9 min 8 s). Limite connue : un COMMIT Postgres par message ; regroupement prévu au lot 6 |

## Feuille de route

1. **Socle** — ingestion, grand livre, DLQ, outbox, tests de panne *(fait)*
2. **Données** — rejeu du jeu public IBM AMLworld, graphe Neo4j, réconciliation Postgres ↔ Neo4j *(fait)*
3. **Détection** — règles à fenêtres glissantes, cycles ordonnés dans le temps, alertes
4. **ML** — score LightGBM expliqué par SHAP, suivi MLflow, évaluation contre la référence publiée
5. **Interface** — poste de l'analyste conformité (file d'alertes, dossier, piste d'audit)
6. **Chaos et mesures** — scénarios de démonstration, test de charge

## Lancer

Prérequis : Docker, Go 1.26 (et Python 3.13 pour préparer les données).

```bash
docker compose up -d --build           # Redpanda, Schema Registry, console, Postgres, ingestion, Neo4j, graph-sink
bash scripts/verifier_bout_en_bout.sh  # 1 000 + 100 doublons + 20 corrompus, puis contrôles
```

| Service | Adresse |
|---|---|
| Console Redpanda (topics, DLQ, schémas) | http://localhost:18088 |
| Métriques de l'ingestion | http://localhost:18090/metrics |
| Postgres (grand livre) | `localhost:15433`, base `grand_livre` |
| Kafka / Schema Registry | `localhost:19092` / http://localhost:18081 |
| Neo4j (navigateur ; utilisateur `neo4j`, mot de passe de développement `motdepasse-local`) | http://localhost:17474 |
| Métriques du graph-sink | http://localhost:18091/metrics |

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

```bash
pip install -r requirements.txt && pytest -q tests                                 # préparation des données
```

La CI (GitHub Actions) lance les tests Go (unitaires et intégration sur un Postgres neuf, `-race`), les tests
de la préparation, puis toute la plateforme dans Docker Compose : script de bout en bout, rejeu d'un extrait
AMLworld et réconciliation Postgres ↔ Neo4j.

## Arborescence

```
schemas/transaction.v*.json   contrat du message, versions successives (Schema Registry, compatibilité BACKWARD)
db/001_grand_livre.sql        grand livre, contraintes, outbox, vues de réconciliation
db/002_evaluation.sql         vérité terrain, hors de portée du rôle applicatif
ingestion/cmd/                ingestion, graphsink, reconcilier, rejouer, injecter (Go : franz-go, pgx, neo4j-go-driver)
outils/                       préparation du jeu AMLworld (Python, DuckDB)
tests/                        tests de la préparation, sur un extrait écrit à la main
scripts/                      vérification de bout en bout, chargement de la vérité terrain
docs/decisions/               choix d'architecture argumentés
```

Usage local uniquement : les mots de passe du Compose sont des valeurs de développement.
