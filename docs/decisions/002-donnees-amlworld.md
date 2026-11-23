# 002 — Données : IBM AMLworld rejoué en flux, étiquettes tenues à part

Date : 2026-11-23 · Statut : adoptée

## Contexte

Si l'on injecte soi-même des motifs de blanchiment et qu'on les détecte, la détection paraît parfaite
mais ne prouve rien. Il faut des opérations réalistes, étiquetées par quelqu'un d'autre, pour mesurer
honnêtement la précision et le rappel.

## Décision

1. **Source** : IBM AMLworld, jeu HI-Small (article NeurIPS 2023 « Realistic Synthetic Financial
   Transactions for Anti-Money Laundering Models », arXiv 2306.16424 ; données sous licence CDLA-Sharing-1.0,
   distribuées sur Kaggle). Monde multi-agents synthétique : aucune personne réelle. Les données ne sont
   pas versionnées dans ce dépôt (`data/` est ignoré).
2. **Préparation** (`outils/preparer_amlworld.py`, testée en CI sur un extrait écrit à la main) :
   - identifiant déterministe `AMLW-<n° de ligne>` : un second rejeu est absorbé par l'idempotence ;
   - comptes AMLworld → IBAN français **fictifs** mais valides (clé RIB et clé IBAN), de façon bijective ;
   - montants en unités mineures **exactes** : centièmes pour les devises fiduciaires (le jeu exprime aussi
     le yen au centième), satoshi pour le bitcoin (code `XBT`) ; une conversion inexacte arrête le script ;
   - **étiquettes séparées** : `verite.csv` est chargé dans le schéma `evaluation`, sans droit pour le rôle
     applicatif. L'étiquette n'entre jamais dans le flux.
3. **Écarts assumés** (comptés dans `bilan.json`) :
   - autovirements (591 212, dont 11 illicites) : le grand livre refuse un virement d'un compte vers lui-même ;
   - opérations avec change (2 191, aucune illicite) : le grand livre est mono-devise par opération.
     *Évolution prévue* : champs « montant et devise reçus » (contrat v3) et compte de position de change,
     soit quatre écritures par opération.
4. **Sous-graphe pour la démonstration en direct** : tous les comptes qui touchent une opération illicite,
   plus 25 % de leurs voisins directs (choix stable par empreinte md5), et les opérations dont les deux
   comptes sont retenus. Résultat : 20 768 comptes, 176 060 opérations, 5 166 illicites (2,9 %).
   Ce taux est **plus élevé** que dans le jeu complet (0,10 %) : il sert la démonstration. L'entraînement
   et l'évaluation du modèle se feront sur le jeu complet.
5. **Horodatages d'origine conservés** (septembre 2022) : la détection raisonne en temps de l'événement ;
   le débit d'envoi est un simple réglage du rejeu.

## Évolution du contrat : v2

Le moyen de paiement d'origine (`payment_format` : ach, cheque, credit_card, cash, wire, bitcoin) est une
variable forte pour la détection (dans HI-Small, 4 483 des 5 177 opérations illicites passent par ACH).
Il est ajouté en **champ facultatif** dans `schemas/transaction.v2.json` ; le registre est en compatibilité
BACKWARD et refuse toute version incompatible au démarrage.

Constaté en testant le registre : il refuse un changement de type, une énumération réduite ou
`additionalProperties: false`, mais **accepte** un nouveau champ obligatoire (avec un modèle de contenu
ouvert, un ancien message aurait pu contenir ce champ). Règle retenue : un nouveau champ est toujours
facultatif, et l'ingestion accepte qu'il manque.

## Point d'attention pour l'évaluation (lot 4)

Dans HI-Small, presque toutes les opérations des derniers jours sont illicites (le 11/09 : 232 sur 396) :
le générateur termine les cycles de blanchiment en cours. Un découpage temporel naïf entraînerait sur un
taux de fraude et testerait sur un autre ; il faudra le traiter explicitement.
