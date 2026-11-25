# 006 — Console Chaos et débit

Date : 2026-11-24 · Statut : adoptée

## Contexte

Une plateforme de lutte contre le blanchiment doit prouver deux choses devant un jury ou un auditeur :
qu'elle lève les bonnes alertes (et pas les mauvaises) sur des montages connus, et qu'elle ne perd ni ne
compte deux fois aucune opération quand quelque chose casse. Le rejeu AMLworld montrait aussi une limite :
un COMMIT Postgres par message plafonnait l'ingestion à quelques centaines d'opérations par seconde.

## Console Chaos

Un service Go (`ingestion/cmd/chaos`, port 18110) joue à la demande des scénarios écrits dans
`ingestion/internal/scenarios`, partagés avec l'injecteur en ligne de commande. **Chaque scénario déclare
les alertes qu'il doit faire lever, et celles qu'il ne doit pas faire lever.** Dans Filature, le bouton
« Chaos » ouvre un tiroir qui lance le scénario puis **vérifie lui-même le résultat sur l'API** (alertes par
compte, compteurs de santé) : il ne croit pas le service Chaos sur parole.

| Scénario | Ce qui doit se passer |
|---|---|
| Schtroumpfage | 6 mules déposent 3 fois 2 900 à 3 300 € en espèces (sous le seuil COSI), puis virent tout à un bénéficiaire : 6 alertes « espèces fractionnées » et un fan-in |
| Cycle | 80 000 € font le tour de 4 sociétés écrans : une alerte de cycle |
| Piège : une paie | Une entreprise déjà établie (30 fournisseurs payés, un par jour) verse 80 salaires le même jour : **aucun** fan-out (vérifié pendant 30 s) |
| 100 doublons | La même opération 100 fois : 99 doublons absorbés, une seule écriture |
| 20 messages corrompus | IBAN faux, devise inconnue, montant négatif, JSON tronqué, sans en-tête, schéma inconnu : 20 en DLQ, rien de bloqué |
| Panne du graph-sink (20 s) | 300 opérations arrivent pendant la panne : le grand livre les écrit, le graphe prend du retard puis rattrape tout |

Résultat observé le 2026-11-24 (capture `docs/captures/filature-chaos.png`) : les 6 essais conformes ;
alertes du schtroumpfage et du cycle visibles dans l'API en moins de 2 s hors panne ; graphe en retard de 300 opérations
pendant la panne, rattrapé 1,6 s après la fin de la pause.

**Ce que la panne a appris.** Le détecteur lit les opérations *après* le graphe (il en a besoin pour les
cycles) : pendant une panne du graph-sink, la détection attend aussi. Les alertes sont retardées, jamais
perdues (lors d'un essai, un schtroumpfage joué pendant la panne a été détecté 14,8 s plus tard, à la reprise).
L'interface le dit sur la carte de la panne.

**Vérifier l'essai, pas des compteurs globaux.** Toutes les opérations d'un essai partagent un préfixe
d'identifiant (`inj-<n>-`) ; l'API les compte au grand livre et dans le graphe (`GET /operations?prefixe=`).
La panne est donc prouvée sur SES 300 opérations (toutes au grand livre, absentes du graphe pendant la pause,
puis toutes dans le graphe), et un autre essai lancé en même temps ne peut pas la valider à sa place. Le piège
de la paie ne compte ses 30 s de silence qu'une fois toutes ses opérations passées dans le graphe, c'est-à-dire
transmises au détecteur ; une erreur de l'API ne valide jamais rien. Limites : les compteurs de doublons et de
DLQ restent globaux (un message refusé n'atteint pas le grand livre) ; et « passées dans le graphe » ne prouve
pas que le détecteur les a déjà traitées, seulement qu'il les a reçues (sa latence médiane est de 168 ms, la
fenêtre de 30 s couvre largement un retard ordinaire).

**La panne est simulée, pas provoquée.** Le service Chaos ne monte pas le socket Docker (ce serait donner
le contrôle de la machine à un service web) : il appelle `POST /chaos/pause` du graph-sink. Ce point d'entrée
exige un corps JSON : une page web tierce ne peut pas envoyer ce Content-Type sans pré-vol CORS, que le
graph-sink n'accorde jamais. Il reste appelable depuis la machine elle-même (port publié sur 127.0.0.1 pour
les métriques), comme le reste de la plateforme de démonstration. La pause est testée *après* chaque lecture
Kafka : la boucle attend presque toujours dans `PollFetches`, et une pause testée avant laissait passer les
messages déjà en route (défaut trouvé en testant la console). Les messages lus restent en mémoire, non
validés ; le rééquilibrage du groupe est bloqué au plus 120 s, acceptable pour une démonstration. Le service
Chaos n'annonce la panne que si le graph-sink l'a acceptée (réponse 204, délai de 5 s).

**Garde.** Comme l'API : une requête qui porte une origine non autorisée est refusée (403), JSON obligatoire
pour les POST (415). Une requête sans en-tête Origin (curl, script) est acceptée : la protection vise les pages
web, pas les programmes de la machine. Dans Docker Compose, les ports ne sont publiés que sur 127.0.0.1 ; lancé
hors Docker, le service écoute sur toutes les interfaces, sans authentification : outil de démonstration locale.

**Limite assumée du piège.** Le plafond d'historique de la règle fan-out compte les contreparties déjà vues :
une entreprise toute neuve qui verse 80 salaires le premier jour déclencherait l'alerte.

## Débit : des lots au lieu d'un COMMIT par message

Avant : une transaction SQL par message, donc un COMMIT (et une écriture du journal sur disque) par
opération. Après :

- l'ingestion découpe chaque lecture Kafka en lots d'au plus 500 messages, **une transaction par lot** ;
- chaque opération est **une seule requête** (CTE : opération, deux écritures, ligne d'outbox) envoyée avec
  son SAVEPOINT dans un même aller-retour (`pgx.Batch`) ;
- un refus des données (SQLSTATE 22 ou 23, par exemple une contrainte CHECK) annule **seulement cette
  opération** (ROLLBACK TO SAVEPOINT) et l'envoie en DLQ ; le reste du lot passe ;
- l'équilibre de chaque opération est vérifié tout de suite (`SET CONSTRAINTS ALL IMMEDIATE`), pas au COMMIT
  du lot. C'est une défense de principe : la requête écrit toujours deux jambes égales, et le test couvre le
  refus d'une contrainte CHECK à l'insertion, pas une opération déséquilibrée ;
- les offsets Kafka ne sont validés qu'après le COMMIT du lot, y compris si l'arrêt arrive juste après. Au pire,
  un lot est relu : ses opérations sont absorbées comme doublons et ses refus repartent une seconde fois en DLQ ;
- les refus de décodage partent en DLQ *avant* l'écriture. Reste un cas : la DLQ tombe juste après le COMMIT
  pour un refus de la base ; le lot réessayé revient en doublons (aucune perte, compteurs faussés pour ce lot).

Test d'intégration : `TestLotMixte` (opération neuve, doublon, refus, conflit d'idempotence dans un même lot).

## Mesures (`scripts/mesurer_debit.sh`, PC portable, Docker Desktop)

Le script envoie N opérations d'un coup et mesure quand chaque étage a tout absorbé. Le trafic se fait entre
200 comptes **neufs à chaque essai** : avec des comptes fixes, leurs fenêtres de 96 h se remplissaient d'un
essai à l'autre et la mesure se dégradait (artefact du banc, corrigé).

| Conditions | Grand livre | Graphe | Détection |
|---|---|---|---|
| Avant les lots (20 000 opérations, base de 200 000) | 50,1 s, 399 op/s | 50,7 s, 394 op/s | 51,3 s, 390 op/s |
| Après les lots, mêmes conditions, juste après | 10,0 s, **1 995 op/s** | 10,6 s, 1 893 op/s | 11,1 s, 1 800 op/s |
| Après les lots, base de 400 000 opérations, interface ouverte, scoreur et deux autres projets actifs sur la machine (20 000 opérations, 2 essais sur 3) | 18,6 à 18,8 s, **≈ 1 070 op/s** | ≈ 1 035 op/s | 34 à 36 s, ≈ 575 op/s |

Le troisième essai de cette série (301 op/s, envoi Kafka seul 22,6 s au lieu de 1 s) a été perturbé par la
machine : écarté, mais signalé. **Le gain mesurable dans des conditions identiques est d'environ 5×** (4,6× de bout en bout). Le rejeu AMLworld
du lot 2 (320 à 660 op/s pour l'ingestion seule) est un autre banc, non comparable. Les
valeurs absolues dépendent beaucoup de la machine ; seule la comparaison avant/après dans les mêmes
conditions a un sens.

**Ce que la mesure a corrigé ailleurs.** La santé de l'API recalculait la réconciliation (parcours complet du
grand livre, deux fois) toutes les 2 s ; sous charge elle prenait près de 1 s, pendant que Postgres montait jusqu'à 3,5 cœurs (ingestion comprise).
Elle est désormais calculée en une passe, au plus toutes les 30 s ; les compteurs légers restent rafraîchis
toutes les 2 s. Coût restant connu : `count(*)` des opérations (≈ 0,2 s, nécessaire au retard exact du graphe)
et l'authentification Neo4j à chaque appel (≈ 0,2 s).

## Pistes non faites

- Détection par lots (le détecteur traite encore opération par opération : c'est l'étage le plus lent).
- Compteur d'opérations entretenu par l'ingestion plutôt que `count(*)`.
- Plusieurs partitions et plusieurs instances d'ingestion (l'ordre par compte le permet déjà : clé = débiteur).
