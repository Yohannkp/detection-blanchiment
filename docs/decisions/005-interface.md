# 005 — Interface « Filature » : le poste de l'analyste conformité

Date : 2026-11-24 · Statut : adoptée

## Contexte

Les lots 1 à 4 produisent des alertes, des preuves et un score expliqué. Il manque l'endroit où un
analyste les instruit et décide, avec une trace que l'on peut montrer à un auditeur.

## Identité

Nom : **Filature** (on suit l'argent de compte en compte). Trois directions visuelles ont été maquettées
avec les vraies données (dossier « papier », salle de veille sombre, registre suisse). Retenue : le
« dossier d'enquête », sur papier crème, à l'encre, avec une seule couleur d'alerte (rouge brique),
des titres en serif (Fraunces) et des chiffres en mono tabulaire (IBM Plex Mono). Elle emprunte aux deux
autres : des **flèches numérotées** dans le graphe, reprises dans la liste des flux, et une **frise du
temps** qui montre les phases d'un montage (l'argent entre, puis repart quelques jours plus tard).
Elle se distingue volontairement du projet Énergie, sombre et cartographique.

## Écran unique, trois colonnes

1. **File d'alertes** triée par score, filtrable par règle et par statut (à traiter, traitées) ;
   touches `j` / `k` ; lien partageable `?alerte=<id>`.
2. **Dossier** : règle, compte au centre, période, montant (jamais de total entre devises différentes :
   on affiche alors le nombre de devises) ; graphe des comptes, frise, liste numérotée des flux, avec un
   survol synchronisé entre les trois ; l'opération la plus suspecte selon le modèle en rouge.
3. **Pourquoi ce score** (les 3 contributions SHAP, en mots), **décision** et **piste d'audit**.

Bandeau permanent : grand livre équilibré ou non (devises en écart, écritures manquantes : deux compteurs,
car on n'additionne pas des centimes, des yens et des satoshis), opérations, débit, alertes à traiter,
latence de détection des 10 dernières minutes, messages en DLQ, indicateur « en direct ». Ces chiffres sont
calculés au plus une fois toutes les 2 s, quel que soit le nombre d'onglets ouverts.

Un dossier affiché est toujours celui surligné dans la file : une réponse en retard (alertes enchaînées
au clavier, réseau lent) est ignorée, et l'ancien dossier disparaît dès qu'on en choisit un autre, pour
qu'une décision ne parte jamais sur la mauvaise alerte. Au-delà de 60 opérations, le graphe dessine les
60 plus récentes (en le disant) ; la frise et la liste restent complètes.

## Décisions de l'analyste

- Trois choix : classer sans suite, escalader au niveau 2, préparer une déclaration de soupçon.
  « Geler le réseau », prévu dans la toute première idée, a été écarté : ce n'est pas le rôle de
  l'analyste de lutte contre le blanchiment.
- Justification obligatoire (20 caractères au moins, contrôlée par l'API **et** par Postgres) et analyste
  nommé. Table `decisions` en **ajout seul** : changer d'avis, c'est ajouter une décision ; le statut d'une
  alerte est sa dernière décision.
- Piste d'audit reconstituée dans l'ordre réel des faits : ouverture, preuves, scores du modèle de service,
  décisions.
- **Écriture protégée contre les pages tierces (CSRF)** : le CORS n'empêche pas un site tiers d'*envoyer* un
  POST « simple » ; l'API exige donc un vrai `application/json` (sinon 415) et une origine autorisée
  (sinon 403). Le WebSocket, que le CORS ne couvre pas, vérifie aussi l'origine.

## Architecture

- **API FastAPI** (`api/`, image Docker de 159 Mo) : `/sante`, `/alertes`, `/alertes/{id}`,
  `POST /alertes/{id}/decisions`, WebSocket `/flux` (santé toutes les 3 s, signal « mise à jour » dès
  qu'une alerte, une preuve, un score ou une décision change). Rôle Postgres `api` : lecture de ce qui
  sert à instruire, écriture des seules décisions, aucun accès à la vérité terrain (testé).
- **Interface Next.js 16** (`web/`, image autonome de 210 Mo) : d3 pour la disposition du graphe
  (d3-force déterministe, recentré) et l'échelle de la frise ; CSS simple, sans cadriciel.
- `docker compose up -d --build` lance toute la plateforme, interface comprise (http://localhost:3100).

## Vérifié

- Tests de l'API sur un vrai Postgres (`tests/test_api.py`, sous Linux ; ajoutés à la CI) : santé, file,
  dossier, décision refusée (justification trop courte, choix inconnu) puis acceptée, passage en
  « traitée », modification refusée (droits du rôle `api`, et déclencheur d'ajout seul même pour un
  superutilisateur), protection CSRF (415 et 403), vérité terrain inaccessible.
- CI : types et build de l'interface ; le test de bout en bout vérifie que les alertes des scénarios
  apparaissent dans l'API.
- À la main, dans le navigateur : décision « escalader » saisie et enregistrée depuis l'interface,
  statut et piste d'audit mis à jour ; flux en direct connecté ; captures haute définition d'un dossier
  de cycle (6 opérations) et d'un fan-out (16 opérations, 7 devises).

## Limites connues

- Pas d'authentification : l'analyste saisit son nom. À brancher sur l'annuaire de l'entreprise (OIDC)
  avant tout usage réel.
- Écran pensé pour un poste de travail (largeur minimale 1 280 px), pas pour un téléphone.
- Pas de tests de l'interface dans un navigateur automatisé : vérification manuelle et captures.
- Une relecture indépendante a trouvé, et on a corrigé : le risque de décision sur la mauvaise alerte,
  la faille CSRF, l'écran figé après une coupure de l'API, un voisinage qui mélangeait les clients de la
  caisse, l'absence d'index sur les comptes, un total qui additionnait plusieurs devises.
