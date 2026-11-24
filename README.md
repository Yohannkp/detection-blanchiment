# Filature — détection du blanchiment en temps réel

Plateforme de streaming qui reçoit des opérations bancaires, les inscrit dans un grand livre en partie
double sans jamais compter deux fois le même euro, et détecte en flux les schémas de blanchiment dans le
graphe des flux, avec des performances mesurées sur des données étiquetées que la détection n'a jamais vues.

![Démonstration de Filature : file d'alertes, dossier d'un cycle, raisons du score, décision de l'analyste, puis un montage injecté en direct qui apparaît en tête de file](docs/demo/demo.gif)

*Démonstration de 74 s (version nette : [docs/demo/demo.mp4](docs/demo/demo.mp4)), enregistrée par un navigateur piloté
(`docs/demo/enregistrer_demo.js`) sur la plateforme lancée en local : un vrai cycle AMLworld est instruit et escaladé,
puis un montage injecté traverse ingestion, graphe et détection et apparaît dans la file en quelques secondes.*

> **État : lots 1 à 5 terminés (socle, données et graphe, détection, modèle, interface).** 176 060 opérations du jeu public
> IBM AMLworld traversent la plateforme ; grand livre et graphe concordent au centime ; sur la période de test,
> les règles retrouvent **40 % des opérations illicites avec 41 % de précision** (plus de 80 % des motifs en
> étoile), alertes levées en **~170 ms**. Un modèle LightGBM évalué dans le protocole de l'article AMLworld
> (F1 **0,525** ; références publiées 0,48 à 0,63) et un modèle de service appris sur le grand livre lui-même
> (F1 **0,73** contre 0,35 pour les règles, même fenêtre) note chaque alerte avec ses 3 raisons. L'analyste instruit
> et décide dans **Filature**, et chaque décision entre dans une piste d'audit qu'on ne peut ni modifier ni effacer.

## Ce que fait le socle

```
 producteurs ─► transactions.raw ─► ingestion (Go) ─┬─► transactions.dlq   message intact + raison en en-tête
   (format Schema Registry)                         │
                                                    └─► Postgres, UNE transaction :
                                                        opération + 2 écritures comptables + outbox
                                                                    │
                                                      relais outbox ─► transactions.valid
                                                                                    │
                                              graph-sink (Go, MERGE idempotent) ─► Neo4j ─► transactions.graphe
                                                                                                    │
                                     alerts.aml ◄─ outbox ◄─ Postgres (alertes, preuves) ◄─ détecteur (Go) ◄─► Redis (fenêtres)
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
| La détection ne dépend pas de l'ordre d'arrivée | Toutes les fenêtres contenant l'opération sont réévaluées ; alertes par période fixe ; un cycle est cherché depuis n'importe laquelle de ses opérations | `TestIndependantDeLOrdreDArrivee` (20 ordres, dont l'inverse), `TestRetraitementIdempotent` |
| La détection ne peut ni tricher ni déborder | Vérité terrain dans un schéma illisible par le rôle `detection` ; ce rôle ne peut écrire dans l'outbox que sur `alerts.aml` | Contrôlé sur la base (refus Postgres) |
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
python outils/preparer_amlworld.py data/HI-Small_Trans.csv --sortie data --voisins 25 --motifs data/HI-Small_Patterns.txt
cd ingestion && go run ./cmd/rejouer -debit 200          # horodatages d'origine conservés
cd .. && bash scripts/charger_verite.sh                   # étiquettes → evaluation.verite_terrain
cd ingestion && go run ./cmd/reconcilier -attendre 60s    # grand livre ↔ graphe
```

## Détection

Quatre règles, calibrées sur les jours 1 à 5 du jeu puis figées : **fan-in** et **fan-out** (au moins 5 ou
6 contreparties distinctes en 96 h, sur un compte à faible historique), **cycle** (l'argent revient à son
compte d'origine en 2 à 6 opérations sur 7 jours, recherche Cypher dans Neo4j), **espèces fractionnées**
(dépôts juste sous le seuil COSI de Tracfin, 10 000 € par mois). Choix et chiffres détaillés :
[docs/decisions/003-detection.md](docs/decisions/003-detection.md).

| Période (évaluation `scripts/evaluer_detection.sql`) | Précision | Rappel | F1 |
|---|---|---|---|
| Calibrage, 1-5 sept. | 0,267 | 0,318 | 0,291 |
| **Test, 6-10 sept.** | **0,410** | **0,404** | **0,407** |

Rappel sur la période de test : fan-in 0,93, scatter-gather 0,91, fan-out 0,87, gather-scatter 0,81,
cycle 0,32, mais stack 0,06 et bipartite 0,09 : ces motifs sans centre net, et les opérations illicites
isolées, sont l'affaire du modèle du lot 4. Les jours 11 à 18 sont exclus de l'évaluation : 91 % des
opérations y sont illicites (le générateur termine ses tentatives), la précision y serait artificielle.
Ces chiffres ne se comparent pas directement à ceux de l'article AMLworld (taux d'illicites et découpage
différents).

## Modèle

LightGBM sur 49 variables calculées « au moment de l'opération » (fenêtres glissantes par compte, sans
jamais regarder le futur : testé). **Deux modèles, même code**, parce qu'on ne peut pas à la fois se
comparer à l'article (jeu complet) et servir sans écart d'entraînement (le grand livre ne contient qu'un
sous-graphe). Décisions prises sur la validation, suivi et registre MLflow. Détails, essais ratés et
correction de l'écart d'entraînement compris : [docs/decisions/004-modele.md](docs/decisions/004-modele.md).

**Référence** : HI-Small complet (5 M d'opérations, 0,10 % illicites), protocole de l'article
(découpage temporel 60/20/20, F1 de la classe illicite au seuil 0,5).

| HI-Small, test (F1 classe illicite) | F1 |
|---|---|
| GIN + EU (article) | 0,477 |
| **Ce projet : LightGBM, 3 graines** | **0,525 ± 0,027** (précision 0,75, rappel 0,40) |
| PNA (article) | 0,568 |
| GFP + LightGBM (article) | 0,629 |

**Service** : appris sur les variables du grand livre (celles que voit le scoreur). Sur sa période de
test hors fin du jeu (34 498 opérations, 2,9 % illicites) : **F1 0,727** (précision 0,67, rappel 0,80),
contre **0,347** pour les règles du lot 3 sur la même fenêtre. Le **scoreur** (`ml/scoreur.py`) note
chaque alerte en direct et écrit ses 3 raisons (contributions SHAP) ; pour trier les alertes, le gain est
réel mais modeste : AUC 0,64 sur 63 alertes de la période de test (taux de base 0,62).

```bash
python -m venv .venv && .venv/Scripts/pip install -r requirements-ml.txt   # (Windows ; bin/ sous Linux)
# Référence (≈ 15 min)
python ml/calculer_variables.py data/HI-Small_Trans.csv data/variables_hi_small.parquet
python ml/entrainer.py data/variables_hi_small.parquet
# Service (≈ 3 min, plateforme lancée et rejeu fait)
python ml/calculer_variables.py --grand-livre postgresql://postgres:postgres@localhost:15433/grand_livre data/variables_grand_livre.parquet
python ml/entrainer.py data/variables_grand_livre.parquet --nom detection-blanchiment-lgbm-service --exporter modeles/production
docker compose --profile modele up -d scoreur                                              # scores dans alertes_scores
mlflow ui --backend-store-uri sqlite:///mlflow.db                                          # suivi des essais
```

## Interface : Filature

Le poste de l'analyste conformité : file d'alertes triée par score, dossier (graphe des comptes aux
flèches numérotées, frise du temps qui montre les phases du montage, liste des flux), raisons du score en
mots, décision (classer sans suite, escalader, préparer une déclaration de soupçon) avec justification
obligatoire, piste d'audit, bandeau de santé et mises à jour en direct (WebSocket). API FastAPI, interface
Next.js ; détails et choix : [docs/decisions/005-interface.md](docs/decisions/005-interface.md).

![Un dossier de fan-out : 16 opérations vers 16 comptes en 4 jours, en 7 devises](docs/captures/filature-dossier-fan-out.png)

## Mesures et tests de panne (2026-11-23, PC portable, Docker Desktop)

| Scénario | Résultat observé |
|---|---|
| 1 000 opérations + 100 doublons + 20 messages corrompus | 1 001 écrites, 99 doublons absorbés, 20 en DLQ, écart 0 |
| Ingestion tuée (`docker kill`) en plein lot de 5 000 | 5 000/5 000 écrites ; 610 messages relus après la reprise, tous absorbés ; reprise en 10 s ; écart 0 |
| Postgres arrêté pendant l'envoi de 300 opérations | Message bloqué et réessayé toutes les 5 s (visible dans les journaux et `ingestion_reessais_total`) ; 300/300 écrites 2 s après le retour de la base ; écart 0 |
| Graph-sink tué en plein envoi de 8 000 opérations | Réconciliation en écart (3 694 opérations manquantes dans le graphe) pendant la panne ; rattrapage en 9 s après la relance ; écart 0 |
| Rejeu complet AMLworld (176 060 opérations, envoi au plus vite) | Toutes écrites, 0 en DLQ ; grand livre équilibré ; graphe concordant ; ingestion seule : 4 à 9 min selon les essais (≈ 320 à 660 op/s) ; avec la détection : 20 min 41 s (≈ 140 op/s). Limites connues : un COMMIT Postgres par message, recherche de cycles sur ± 7 jours ; optimisation prévue au lot 6 |
| Latence de détection, 18 scénarios injectés à vide | Médiane 168 ms, maximum 1,06 s entre l'ingestion de l'opération et la création de l'alerte |

## Feuille de route

1. **Socle** — ingestion, grand livre, DLQ, outbox, tests de panne *(fait)*
2. **Données** — rejeu du jeu public IBM AMLworld, graphe Neo4j, réconciliation Postgres ↔ Neo4j *(fait)*
3. **Détection** — règles à fenêtres glissantes, cycles dans le graphe, alertes, évaluation *(fait)*
4. **ML** — score LightGBM expliqué par SHAP, suivi MLflow, évaluation contre la référence publiée *(fait)*
5. **Interface** — poste de l'analyste conformité (file d'alertes, dossier, décision, piste d'audit) *(fait)*
6. **Chaos et mesures** — scénarios de démonstration, test de charge

## Lancer

Prérequis : Docker, Go 1.26 (et Python 3.13 pour préparer les données).

```bash
docker compose up -d --build           # toute la plateforme, API et interface comprises → http://localhost:3100
docker compose --profile modele up -d scoreur   # scores des alertes (une fois le modèle entraîné, voir « Modèle »)
bash scripts/verifier_bout_en_bout.sh  # 1 000 + 100 doublons + 20 corrompus, puis contrôles
```

| Service | Adresse |
|---|---|
| **Filature, le poste de l'analyste** | **http://localhost:3100** |
| API de l'analyste (documentation : `/docs`) | http://localhost:18100 |
| Console Redpanda (topics, DLQ, schémas) | http://localhost:18088 |
| Métriques de l'ingestion | http://localhost:18090/metrics |
| Postgres (grand livre) | `localhost:15433`, base `grand_livre` |
| Kafka / Schema Registry | `localhost:19092` / http://localhost:18081 |
| Neo4j (navigateur ; utilisateur `neo4j`, mot de passe de développement `motdepasse-local`) | http://localhost:17474 |
| Métriques du graph-sink | http://localhost:18091/metrics |
| Métriques du détecteur / Redis | http://localhost:18092/metrics / `localhost:16379` |

Envoyer des messages à la main :

```bash
cd ingestion
go run ./cmd/injecter -mode normal -n 1000    # virements et dépôts valides entre 200 comptes fictifs
go run ./cmd/injecter -mode doublons -n 100   # la même opération 100 fois
go run ./cmd/injecter -mode corrompus -n 20   # IBAN faux, devise inconnue, montant négatif, JSON tronqué, schéma absent ou inconnu
go run ./cmd/injecter -mode fanin             # 6 comptes neufs versent à un même compte → alerte fan_in
go run ./cmd/injecter -mode cycle             # 80 000 € font A → B → C → A → alerte cycle
go run ./cmd/injecter -mode especes           # 4 dépôts de 2 400 € dans le mois → alerte especes_fractionnees
```

Évaluer la détection contre la vérité terrain (superutilisateur : les rôles applicatifs n'y ont pas accès) :

```bash
docker compose exec -T postgres psql -U postgres -d grand_livre -f - < scripts/evaluer_detection.sql
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
db/002_evaluation.sql         vérité terrain (étiquette, motif), hors de portée des rôles applicatifs
db/003_alertes.sql            alertes et preuves (ajout seul), rôle detection restreint
db/004_scores.sql             scores et raisons du modèle (ajout seul), rôle scoreur
db/005_decisions.sql          décisions de l'analyste (ajout seul), rôle api
api/                          API FastAPI du poste de l'analyste (+ WebSocket)
web/                          interface Filature (Next.js, d3)
ml/                           variables sans fuite (DuckDB), entraînement LightGBM + MLflow, scoreur en direct
ingestion/cmd/                ingestion, graphsink, detecteur, reconcilier, rejouer, injecter (Go : franz-go, pgx, neo4j-go-driver, go-redis)
outils/                       préparation du jeu AMLworld (Python, DuckDB)
tests/                        tests de la préparation, des variables (anti-fuite), du scoreur et de l'API
scripts/                      vérification de bout en bout, chargement de la vérité terrain, évaluation de la détection
docs/decisions/               choix d'architecture argumentés
```

Les scripts `db/*.sql` ne s'exécutent qu'à la création du volume Postgres : après un changement de
schéma, repartir d'une base neuve (`docker compose down -v`, puis rejouer les données ; tout est
reconstructible depuis le CSV AMLworld).

Usage local uniquement : les mots de passe du Compose sont des valeurs de développement.
