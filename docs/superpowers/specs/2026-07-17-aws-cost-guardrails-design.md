# Design — Garde-fous de coût AWS persistants

**Date:** 2026-07-17
**Statut:** approuvé (brainstorming), en attente de revue de la spec écrite
**Compte:** `123456789012` (standalone, hors AWS Organization), accès SSO `AdministratorAccess`, région `ca-central-1`.

## Contexte & problème

Une alerte budget a signalé un coût mensuel aberrant (~2,6 M$ réels en Cost Explorer, 16,27 M$ annoncés par Budgets). L'enquête a montré qu'il s'agit d'une **erreur de métrologie S3 côté AWS** (facturation de ~100 PO sur un bucket de 0,85 Mo), pas d'une dépense réelle ni d'une compromission — voir la mémoire projet `aws-s3-billing-anomaly-2026-07`.

Suite à ça, l'utilisateur veut « ne jamais dépasser 20 $/mois ». **Fait AWS à intégrer :** il n'existe aucun plafond de facturation *dur* sur AWS. On construit donc le meilleur équivalent réaliste : détection précoce fiable + coupe-circuit automatique réactif.

**Contrainte transitoire (constatée le 2026-07-17) :** tant que la facture fantôme n'est pas corrigée, `ActualSpend` (16,27 M$) sature tout budget mensuel : les seuils `ACTUAL` sont nés en `ALARM` et ne re-déclencheront plus ce mois-ci, et une action `AUTOMATIC` s'exécuterait immédiatement pour une dépense inexistante. D'où le déploiement en deux temps (§ Séquencement).

## Objectifs

- Détecter toute dépense réelle **très tôt** (bien avant 20 $) par email.
- Appliquer **automatiquement** un frein quand la dépense réelle dépasse 20 $/mois.
- Les garde-fous doivent **survivre au `terraform destroy`** du stack applicatif.
- Aucun risque de verrouillage définitif hors du compte.

## Non-objectifs (limites assumées, documentées)

- ❌ Couper la facturation à 20 $ pile — impossible sur AWS.
- ❌ Empêcher un bug de métrologie AWS (comme l'incident du 2026-07-17) — un frein sur les créations de ressources n'agit pas sur une facturation fantôme.
- ❌ Stopper les ressources **déjà en cours** (le Deny bloque les *nouvelles* créations ; les ressources actives continueraient de facturer — non bloquant ici car rien ne tourne au repos).
- Le vrai garant du ~0 $ reste opérationnel : **rien de déployé au repos** (demo-strategy : `apply` à la demande, `destroy` en fin de session).

## Architecture

Nouveau **root Terraform séparé et persistant** : `infra/environments/guardrails/`.

- Backend S3 existant (`vigil-api-tfstate-dev`), **clé distincte** `guardrails/terraform.tfstate` (état isolé de `dev`).
- **Appliqué une seule fois, jamais détruit** en fin de session (contrairement à `environments/dev/`). Documenté en tête de module + dans `infra/README.md`.
- Réutilise la config provider/région du repo (`ca-central-1`, profil SSO `default`).

Toutes les alertes vont à une seule adresse : `alerts@example.com`.

## Composant 1 — Détection précoce

1. **Budget mensuel 20 $** (`aws_budgets_budget`, `COST`, `MONTHLY`) — repris en Terraform pour remplacer le budget manuel « My Monthly Cost Budget » (supprimé pour éviter les doublons d'alerte). Notifications `ACTUAL` aux seuils **5 / 25 / 50 / 75 / 90 / 100 %** (= 1 / 5 / 10 / 15 / 18 / 20 $) + une notification `FORECASTED` à 100 %.
2. **Budget quotidien 2 $** (`aws_budgets_budget`, `COST`, `DAILY`) — notifications `ACTUAL` à **50 / 100 %**. Ce budget aurait crié dès le **jour 1** du pic à ~520 k$/jour. *Seuil ajustable :* pendant une période active (stack complet déployé ≈ 1–1,5 $/jour + NAT), il pourrait générer des emails ; on pourra le monter à ~3 $/jour si trop bruyant — ça attrape toujours toute vraie anomalie.
3. **Cost Anomaly Detection** — vérifier/aligner l'abonnement existant (`Default-Services-Subscription`) pour qu'il notifie `alerts@example.com` avec un seuil d'impact bas (> 1 $). Note documentée : sur un compte de <10 jours, l'anomaly detection est aveugle faute de baseline ; utile en vieillissant.

## Composant 2 — Coupe-circuit automatique (Budget Action)

- **Déclencheur :** budget mensuel 20 $, `ACTUAL` `GREATER_THAN` `100 %`.
- **Mode cible :** `AUTOMATIC` (choix utilisateur), atteint en deux temps — voir § Séquencement. Le mode est porté par une variable Terraform (`action_approval_model`) pour que la bascule `MANUAL` → `AUTOMATIC` soit un one-liner + `apply`.
- **Action :** `aws_budgets_budget_action` de type `POLICY` qui attache une politique IAM managée `vigil-deny-expensive-creates` au **rôle SSO `AWSReservedSSO_AdministratorAccess_*`** (ARN résolu au moment de l'implémentation via `aws iam list-roles`).

### Politique `vigil-deny-expensive-creates`

`Effect: Deny` sur la **création** des ressources coûteuses uniquement :

- Compute/conteneurs : `ec2:RunInstances`, `ecs:CreateService`, `ecs:RunTask`, `eks:CreateCluster`, `eks:CreateNodegroup`
- Réseau facturé : `ec2:CreateNatGateway`, `ec2:AllocateAddress`, `elasticloadbalancing:CreateLoadBalancer`
- Data/ML : `rds:CreateDBInstance`, `rds:CreateDBCluster`, `sagemaker:Create*`, `elasticache:CreateCacheCluster`, `redshift:CreateCluster`, `emr:RunJobFlow`

**Non refusé (donc toujours autorisé)** — garantit l'absence de verrouillage :
- `iam:*` (donc **tu peux détacher la politique toi-même**), `budgets:*`, `ce:*`, `support:*`, `sts:*`
- toutes les actions `Describe*/List*/Get*` (lecture)
- toutes les actions `Delete*/Terminate*` (nettoyage / `terraform destroy` possible)
- `s3:*` (accès au state Terraform)

### Rôles IAM requis

- **Service-linked role** `AWSServiceRoleForBudgets` (absent aujourd'hui) — créé via `aws_iam_service_linked_role` (`budgets.amazonaws.com`).
- **Rôle d'exécution** assumé par `budgets.amazonaws.com` (trust policy), avec permissions minimales `iam:AttachRolePolicy` sur le rôle SSO cible et la politique managée.

## Tests & validation

- `terraform fmt` / `validate` / `tflint` propres (standard projet).
- `terraform plan` revu **avant** tout `apply` (l'`apply` crée des ressources IAM/Budgets réelles → confirmation explicite requise).
- Après `apply` : `aws budgets describe-budgets` (2 budgets), `describe-budget-actions-for-budget` (action présente, statut `STANDBY`), `aws iam get-policy` (politique présente mais **non attachée** au repos).
- Contrôle de sécurité anti-verrouillage : relire la politique pour confirmer qu'aucune action `iam:Detach*`/`iam:Delete*Policy` n'est refusée.

## Routing modèle/effort (règle CLAUDE.md)

| Étape d'implémentation | Modèle | Effort |
|---|---|---|
| Squelette du module `guardrails/` (backend, provider, variables) | Sonnet 5 | low |
| Budgets (mensuel + quotidien) + notifications | Sonnet 5 | medium |
| Politique IAM `deny-expensive-creates` + rôles (sensible : anti-verrouillage) | Opus 4.8 | medium |
| Câblage `aws_budgets_budget_action` (AUTOMATIC) | Sonnet 5 | high |
| `plan`, revue, `apply`, vérifications post-apply | (boucle principale, revue humaine) | — |

## Séquencement (décision utilisateur, 2026-07-17)

1. **Temps 1 — maintenant :** tout est implémenté et appliqué (budgets, politique, action, rôles), mais l'action est déployée en `approval_model = MANUAL` : si elle se déclenche (ce qu'elle fera, budget saturé par la facture fantôme), AWS envoie un email de demande d'approbation au lieu d'attacher le Deny. **La détection effective immédiate repose sur le budget quotidien 2 $**, qui se remet à zéro chaque jour et n'est pas saturé.
2. **Temps 2 — dès que `ActualSpend` redevient sain** (correction AWS de l'anomalie, ou reset mensuel du 1er août) : basculer `action_approval_model` sur `AUTOMATIC` et ré-appliquer. Vérifier alors que l'action est en `STANDBY` et non déclenchée.

## Lifecycle

- Appliquer une fois. **Ne jamais** inclure `guardrails/` dans la commande `terraform destroy` de fin de session.
- Documenté dans `infra/README.md` et en commentaire d'en-tête du module.
