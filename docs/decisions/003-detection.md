# 003 — Détection : règles calibrées, indépendantes de l'ordre d'arrivée, mesurées sur des jours jamais vus

Date : 2026-11-24 · Statut : adoptée

## Contexte

Le lot 3 doit lever des alertes en flux, sur les opérations AMLworld rejouées, et dire honnêtement ce
qu'elles valent. Le fichier des motifs du jeu (`HI-Small_Patterns.txt`, 370 tentatives) permet de
mesurer le rappel motif par motif. Les motifs s'étalent sur **1 à 8 jours** (médiane de 24 h pour
« bipartite » à 150 h pour « gather-scatter ») : les fenêtres de détection se comptent en jours.

## Architecture

```
transactions.valid ─► graph-sink ─► Neo4j ─► transactions.graphe ─► détecteur ─► Postgres (alertes + outbox) ─► alerts.aml
                                                                       │  ▲
                                                                 Redis (fenêtres glissantes)
```

- Le détecteur lit chaque opération **après** son écriture dans le graphe (relais du graph-sink).
- Fenêtres glissantes en **temps de l'événement** dans Redis (appendonly ; clés oubliées après 30 jours
  sans écriture).
- Alertes et preuves dans Postgres, en ajout seul ; l'annonce part par l'outbox (pas de double écriture).
  Le rôle `detection` ne peut écrire que sur `alerts.aml` (trigger) et ne voit pas la vérité terrain.

### Ordre d'arrivée : le résultat n'en dépend pas

Les opérations ne parviennent pas au détecteur dans l'ordre chronologique : `transactions.graphe` a
6 partitions (clé = compte débité), et en rejeu accéléré une seconde d'écart de traitement représente
plus d'une heure d'horloge simulée. Une relecture indépendante l'a mesuré sur la première version
(2 933 opérations en retard côté crédit, des alertes en double, des réseaux manqués quand leurs
opérations arrivaient à l'envers). D'où trois choix :

1. **Fenêtres** : chaque opération réévalue toutes les fenêtres de 96 h qui la contiennent, y compris
   celles qui se terminent après elle. Élagage avec un retard toléré de 24 h (horloge de l'événement).
2. **Alertes par période fixe** : identifiant = (règle, compte, période de 96 h, 7 jours ou mois civil).
   Plus d'état d'« épisode » qui dépendrait de l'ordre. Prix : un réseau à cheval sur deux périodes donne
   deux alertes (mesuré ci-dessous).
3. **Cycles** : une opération cherche le cycle dont elle fait partie à n'importe quelle place (opérations
   dans les 7 jours avant et après, cycle complet tenant en 7 jours). L'opération du cycle écrite en
   dernier dans le graphe le trouve donc toujours ; le compte d'origine (émetteur de l'opération la plus
   ancienne) et la période ne dépendent pas de celle qui le trouve.

Test : 20 ordres d'arrivée aléatoires (dont l'ordre inverse) d'un même réseau donnent exactement les
mêmes alertes et les mêmes preuves (`TestIndependantDeLOrdreDArrivee`).

## Règles et calibrage

Réglages choisis **uniquement sur les jours 1 à 5** (balayage hors ligne des fenêtres et des seuils,
dans DuckDB), puis figés.

| Règle | Réglage | Pourquoi |
|---|---|---|
| Fan-in | ≥ 5 émetteurs distincts en 96 h, sur un compte qui a eu ≤ 20 contreparties | Sans le plafond d'historique, les « hubs » légitimes dominent |
| Fan-out | ≥ 6 bénéficiaires distincts en 96 h, même plafond | Plafond indispensable : F1 du balayage 0,018 sans, 0,144 avec |
| Cycle | retour au compte d'origine en 2 à 6 opérations sur 7 jours, sans passer par un compte de plus de 200 liens | Au-delà de 6, le nombre de chemins explose (Neo4j saturé, constaté) ; imposer l'ordre chronologique dans le cycle n'apportait rien |
| Espèces fractionnées | ≥ 3 dépôts d'espèces dans le mois civil, cumul entre 8 000 et 10 000 € | Contournement du seuil COSI (art. L.561-15-1 du CMF). AMLworld n'a pas de dépôts d'espèces : règle pour la démonstration |

Pas de règle scatter-gather dédiée : fan-in et fan-out couvrent déjà 91 % de ce motif (ci-dessous).

## Résultats (rejeu de 176 060 opérations, 2026-11-24)

`scripts/evaluer_detection.sql`. Unité : l'opération. Elle est signalée si elle figure dans les preuves
d'une alerte **vérifiée avant la fin de sa période** : une preuve apportée par les jours suivants ne
compte pas.

| Période | Règle | Précision | Rappel | F1 |
|---|---|---|---|---|
| Calibrage, 1-5 sept. | toutes | 0,267 | 0,318 | 0,291 |
| **Test, 6-10 sept.** | fan-in | 0,318 | 0,183 | 0,232 |
| | fan-out | 0,576 | 0,148 | 0,236 |
| | cycle | 0,500 | 0,085 | 0,145 |
| | **toutes** | **0,410** | **0,404** | **0,407** |
| Fin du jeu, 11-18 sept. | toutes | 0,947 | 0,793 | 0,863 |

**La ligne « fin du jeu » n'est pas un résultat** : 91 % des opérations de ces jours sont illicites (le
générateur termine les tentatives en cours), la précision y est mécaniquement élevée. On la montre
pour l'écarter.

Rappel par motif, période de test :

| Motif | Rappel | | Motif | Rappel |
|---|---|---|---|---|
| fan-in | 0,925 | | cycle | 0,322 |
| scatter-gather | 0,907 | | random | 0,146 |
| fan-out | 0,865 | | bipartite | 0,089 |
| gather-scatter | 0,814 | | stack | 0,057 |
| | | | illicites hors motif | 0,147 |

Les motifs « en étoile » sont bien couverts. Les motifs qui se répartissent sur plusieurs comptes sans
centre net (stack, bipartite, random), et les opérations illicites isolées, échappent aux règles : c'est
le rôle du modèle du lot 4, qui combinera ces signaux avec d'autres variables.

Charge pour un analyste (toutes périodes) : 791 alertes, de 3 (cycle) à 17 (fan-out) opérations en
moyenne ; 69 % (cycle) à 85 % (fan-out) contiennent au moins une opération illicite. Comptes recevant
plusieurs alertes d'une même règle : 57 sur 256 en fan-in, 47 sur 101 en fan-out, 24 sur 299 en cycle,
presque tous sur des périodes contiguës (effet du découpage en périodes fixes).

Latence (ingestion de l'opération déclenchante → alerte créée), 18 scénarios injectés à vide :
**médiane 168 ms, maximum 1,06 s**. Pendant le rejeu au plus vite, la latence mesure la file d'attente
(médiane 5,1 s) et n'est pas un indicateur de réactivité.

Débit de la chaîne complète pendant ce rejeu : 176 060 opérations détectées en 20 min 41 s (≈ 140 op/s),
contre 4 à 9 min pour l'ingestion seule selon les essais sur le même PC. La recherche de cycles sur
± 7 jours, l'ingestion (un COMMIT par message) et le partage du processeur entre tous les services
limitent ce débit ; l'optimisation est prévue au lot 6.

## Comparaison avec la littérature : pas directe

L'article AMLworld rapporte un F1 d'environ 0,57 (PNA) sur HI-Small. Nos chiffres **ne sont pas
comparables** : nous travaillons sur un sous-graphe où 2,9 % des opérations sont illicites (2,6 % sur
les jours 1-10, contre 0,10 % dans le jeu complet), avec un découpage temporel différent. Une comparaison
honnête demandera d'évaluer le modèle du lot 4 dans les conditions de l'article.

## Limites connues

- Une opération arrivée plus de 24 h (horloge de l'événement) après les plus récentes de son compte peut
  trouver une fenêtre déjà élaguée.
- Le plafond d'historique (≤ 20 contreparties) compte les contreparties vues jusqu'ici ; le plafond
  « 200 liens » des cycles porte sur le degré dans le graphe au moment de la requête.
- Toute erreur arrête le lot et le réessaie : une erreur définitive bloque le détecteur (visible dans
  `detecteur_reessais_total`) plutôt que de laisser passer des opérations sans les examiner.
- Les seuils sont fixes ; un score continu viendra du modèle (lot 4).
