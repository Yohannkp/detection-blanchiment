# 004 — Modèle : un modèle de référence comparable à l'article, un modèle de service sans écart d'entraînement

Date : 2026-11-24 · Statut : adoptée

## Contexte

Les règles du lot 3 trouvent bien les motifs en étoile, mais presque rien des motifs sans centre net
(stack, bipartite, random) ni des opérations illicites isolées. Le modèle doit (1) être évalué de façon
comparable à la littérature, (2) donner à chaque alerte un score et des raisons lisibles, (3) être servi
sur exactement les mêmes variables que celles qu'il a apprises.

Une relecture indépendante a montré que (1) et (3) sont incompatibles avec un seul modèle : appris sur le
jeu AMLworld complet (5 M d'opérations), il était servi sur le grand livre de la démonstration, qui n'en
contient qu'un sous-graphe (176 060 opérations). Même code, mais historiques des comptes tronqués : le
nombre de sorties d'un compte sur 7 jours n'était identique que dans 4 % des cas. D'où **deux modèles**.

| | Modèle de **référence** | Modèle de **service** |
|---|---|---|
| Rôle | se comparer à l'article | noter les alertes en direct |
| Données | HI-Small complet (5 078 345 op., 0,10 % illicites) | le grand livre de la plateforme (176 060 op. étiquetées, 2,9 % illicites) |
| Variables | `ml/variables.py` sur le CSV | `ml/variables.py` sur le grand livre, via `ml/grand_livre.py`, **le code même du scoreur** |
| Registre MLflow | `detection-blanchiment-lgbm` v3 | `detection-blanchiment-lgbm-service` v1, exporté dans `modeles/production/` |

## Protocole (celui de l'article AMLworld, vérifié le 2026-11-24)

- Découpage **temporel 60/20/20** des opérations triées par horodatage.
- Métrique principale : **F1 de la classe illicite au seuil 0,5** ; aussi au seuil choisi sur la validation.
- Variables gardées, poids de la classe illicite (grille 1, 3, 10) et graine publiée : choisis sur la
  **validation**. Pour le modèle de référence, le test a été mesuré une première fois (premier essai,
  qui a révélé l'instabilité décrite plus bas), puis au passage final ; un troisième passage, identique,
  a seulement servi à publier la graine choisie sur la validation. Le modèle de service n'a vu son test
  qu'une fois.

Références de l'article (F1, HI-Small) : GIN + EU 0,477 ; PNA 0,568 ; GFP + LightGBM 0,629 ;
GFP + XGBoost 0,632.

## Variables : 49, calculées « au moment de l'opération »

`ml/variables.py`, en SQL DuckDB. Pour l'opération u → v à l'instant T (tronqué à la minute, résolution
d'AMLworld), seules comptent l'opération elle-même et les opérations des minutes **strictement
antérieures**. Chaque opération devient deux événements de compte (sortie pour u, entrée pour v) ; toutes
les statistiques sont des fenêtres glissantes par compte : nombre d'opérations, contreparties distinctes
et montants en sortie et en entrée sur 24 h, 96 h et 7 jours (36 variables), délais depuis la dernière
opération, montant rapporté à l'habitude du compte, paire déjà vue sur 7 jours, aller-retour v → u sur
7 jours, et 8 variables de l'opération (montant, montant rond, devise, moyen de paiement, change,
autovirement, même banque, heure).

Tests (`tests/test_variables.py`) : ajouter des opérations futures ne change aucune variable passée ;
deux opérations de la même minute ne se voient pas, même à 40 s d'écart ; valeurs vérifiées à la main ;
un calcul limité aux comptes concernés donne exactement les mêmes variables (c'est ce que fait le scoreur).

## Ce que les essais ont appris (modèle de référence)

1. **Premier essai : F1 test 0,43 ± 0,09**, très instable selon la graine, arrêt anticipé dès la 1re
   itération pour les poids élevés, contributions SHAP jusqu'à 67 en log-cote. Causes : des compteurs
   « depuis le début » et l'ancienneté des comptes, qui grandissent avec le temps (le modèle apprenait
   l'époque, pas le comportement), et des feuilles non régularisées. Corrigé : fenêtres glissantes
   uniquement, `min_sum_hessian_in_leaf` 1 et `lambda_l2` 10. F1 de validation : 0,40 → 0,54.
2. **Scatter-gather et cycles à 3 étapes** (u → m → v, v → m → u), proches des variables GFP de l'article :
   calcul rapide (9 s), mais aucun gain sur la validation (0,532 contre 0,536). Retirés.
3. **Poids de la classe illicite** : 3 retenu (F1 validation 0,536).

## Résultats

### Modèle de référence : comparable à l'article (test, 3 graines)

| Mesure | Seuil 0,5 | Seuil choisi sur la validation |
|---|---|---|
| **F1** | **0,525 ± 0,027** | 0,537 ± 0,015 |
| Précision | 0,752 ± 0,027 | 0,730 ± 0,034 |
| Rappel | 0,404 ± 0,024 | 0,425 ± 0,012 |
| PR-AUC | 0,538 ± 0,024 | — |

**Au-dessus de GIN + EU, en dessous de PNA et nettement en dessous de GFP + LightGBM (0,629).** L'écart
avec GFP vient vraisemblablement de ses variables de motifs plus riches (cycles temporels jusqu'à 10
sauts, scatter-gather calculés sur tout le graphe), que nos versions simplifiées n'ont pas reproduites
utilement. Piste : calculer ces motifs dans Neo4j, là où vivent déjà les cycles du lot 3.

### Modèle de service : ce que le ML apporte à la plateforme

Test du modèle de service (après le 8 sept. 20 h 45), **hors fin du jeu** (le 11 septembre et après,
91 % d'illicites) : 34 498 opérations, 2,9 % illicites. Même fenêtre, même vérité terrain :

| Sur la même fenêtre | Précision | Rappel | F1 |
|---|---|---|---|
| Règles du lot 3 (toutes) | 0,328 | 0,367 | 0,347 |
| **Modèle de service, seuil 0,5** | **0,667** | **0,800** | **0,727** |

Ces chiffres ne se comparent pas à l'article : la part d'opérations illicites y est 30 fois plus forte
que dans le jeu complet (sous-graphe choisi autour des comptes illicites).

Variables les plus utiles (moyenne |SHAP|) : moyen de paiement, paire déjà vue sur 7 jours,
bénéficiaires distincts de l'émetteur sur 96 h, montant, montant rapporté à l'habitude.

## Score en direct

`ml/scoreur.py` (service `scoreur`, profil Compose `modele`) : pour chaque alerte dont les preuves
présentes au grand livre n'ont pas encore été notées par la version courante du modèle, il charge les
opérations de tous les comptes concernés (`ml/grand_livre.py`), calcule les variables, et écrit dans
`alertes_scores` (ajout seul) : le score (probabilité maximale parmi les preuves), la preuve la plus
suspecte et ses 3 raisons (contributions SHAP calculées par LightGBM ; un test vérifie qu'elles
redonnent exactement le score). Une alerte qui s'enrichit, ou un nouveau modèle publié, déclenche un
nouveau score ; l'historique reste. Mesuré : 1 121 alertes notées en moins d'une minute.

Le score aide-t-il à trier les alertes ? `scripts/evaluer_scores.sql`, sur les **63** alertes dont toutes
les preuves tombent dans la période de test du modèle de service, hors fin du jeu : 39 utiles (contenant
au moins une opération illicite), 24 inutiles, **taux de base 0,62**. **AUC 0,64** ; le quart le mieux
noté est utile à 0,67, le quart le moins bien noté à 0,38. Un gain réel mais modeste, sur un petit
échantillon. (Une première mesure, avant la correction de l'écart d'entraînement et sans exclure la
fin du jeu, donnait 0,91 : elle était gonflée.)

## Limites connues

- Le modèle de service a appris sur le sous-graphe de la démonstration, choisi autour des comptes
  illicites : ses chiffres absolus ne se transposent pas à une banque réelle.
- Les scénarios de démonstration de l'injecteur (euros, sans moyen de paiement) sont étrangers au monde
  AMLworld : le modèle leur donne un score proche de 0 ; les règles, elles, les détectent.
- MLflow (`mlflow.db`, `mlruns/`) et les modèles exportés restent locaux ; ils se reconstruisent avec
  `ml/calculer_variables.py` puis `ml/entrainer.py` (≈ 15 min pour la référence, ≈ 3 min pour le service).
