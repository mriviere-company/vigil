# AWS Cost Guardrails Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persistent Terraform root `infra/environments/guardrails/` providing low-threshold budget alerts (monthly 20 $ + daily 2 $), an aligned Cost Anomaly Detection subscription, and a Budget Action circuit breaker that attaches a `deny-expensive-creates` IAM policy to the SSO admin role — deployed `MANUAL` first, flipped to `AUTOMATIC` once the phantom S3 charge is reversed.

**Architecture:** A standalone Terraform root (own state key `guardrails/terraform.tfstate` in the existing `vigil-api-tfstate-dev` bucket) that is applied once and **never destroyed** with the dev stack. Two `aws_budgets_budget` resources feed email alerts; an `aws_budgets_budget_action` (APPLY_IAM_POLICY) wired to the monthly budget attaches a Deny-only customer-managed policy to the SSO role via a minimal execution role. The pre-existing anomaly subscription is imported and re-pointed.

**Tech Stack:** Terraform >= 1.9, AWS provider ~> 5.0, S3 backend with native lockfile (`use_lockfile`), tflint with the repo's shared `infra/.tflint.hcl`.

**Spec:** `docs/superpowers/specs/2026-07-17-aws-cost-guardrails-design.md` (read it before starting a task if anything is ambiguous).

## Global Constraints

- Account `123456789012`, region `ca-central-1`, AWS auth via SSO profile `default` (run `aws sts get-caller-identity --profile default` first; if expired, STOP and ask the user to run `! aws sso login --profile default`).
- Backend: bucket `vigil-api-tfstate-dev`, key `guardrails/terraform.tfstate`, `use_lockfile = true`, `encrypt = true` (mirrors `infra/environments/dev/backend.tf`).
- Single alert address everywhere: `alerts@example.com`.
- SSO role targeted by the brake: name `AWSReservedSSO_AdministratorAccess_<suffix>`, ARN `arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/ca-central-1/AWSReservedSSO_AdministratorAccess_<suffix>`.
- Existing anomaly resources: monitor `arn:aws:ce::123456789012:anomalymonitor/11111111-1111-1111-1111-111111111111`, subscription `arn:aws:ce::123456789012:anomalysubscription/00000000-0000-0000-0000-000000000000` (currently → outlook.com, threshold 100 $ AND 40 % — both wrong per spec).
- `approval_model` starts `MANUAL` (phantom-charge saturation would trip an AUTOMATIC action instantly); the flip to `AUTOMATIC` is a variable change, **not** part of this plan's tasks.
- Terraform standards: `terraform fmt` / `validate` / `tflint --config ../../.tflint.hcl` clean after every task; conventional commits explaining the *why*.
- **No `terraform apply` in Tasks 1–6.** Apply is Task 7 only, in the main loop, after the user reviews the plan output. Subagents must never apply.
- Every `terraform`/`aws` command below runs from `infra/environments/guardrails/` unless stated otherwise.

## Model/effort routing (CLAUDE.md rule)

| Task | Model | Effort |
|---|---|---|
| 1 — Root scaffold | Sonnet 5 | low |
| 2 — Budgets | Sonnet 5 | medium |
| 3 — Deny policy + roles | Opus 4.8 | medium |
| 4 — Budget action | Sonnet 5 | high |
| 5 — Anomaly subscription | Sonnet 5 | medium |
| 6 — Docs | Haiku 4.5 | low |
| 7 — Apply + verify | main loop (user-gated) | — |

---

### Task 1: Guardrails root scaffold (backend, provider, variables)

**Files:**
- Create: `infra/environments/guardrails/backend.tf`
- Create: `infra/environments/guardrails/versions.tf`
- Create: `infra/environments/guardrails/variables.tf`

**Interfaces:**
- Consumes: existing S3 state bucket `vigil-api-tfstate-dev` (created by `infra/scripts/bootstrap-backend.sh`, already exists).
- Produces: `var.aws_region`, `var.alert_email`, `var.action_approval_model` — used by Tasks 2–4. Provider `aws` with `default_tags` `Environment = "guardrails"`.

- [ ] **Step 1: Write `backend.tf`**

```hcl
# Cost-guardrails root — PERSISTENT. This root is applied once and is
# deliberately excluded from the end-of-session `terraform destroy`
# ritual (PLAN.md §7 / CLAUDE.md): a budget that disappears with the dev
# stack protects nothing. Its state lives in the same bucket as dev's
# but under its own key, so the two roots cannot lock or clobber each
# other. Same backend mechanics as environments/dev/backend.tf (native
# S3 lockfile; literals required because backend blocks cannot read
# variables — see that file's header for the full rationale).
terraform {
  backend "s3" {
    bucket       = "vigil-api-tfstate-dev"
    key          = "guardrails/terraform.tfstate"
    region       = "ca-central-1"
    use_lockfile = true
    encrypt      = true
  }
}
```

- [ ] **Step 2: Write `versions.tf`**

```hcl
terraform {
  required_version = ">= 1.9.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project     = "vigil-api"
      Environment = "guardrails"
      ManagedBy   = "terraform"
    }
  }
}
```

- [ ] **Step 3: Write `variables.tf`**

```hcl
variable "aws_region" {
  type        = string
  description = "AWS region for the provider. Budgets/CE are global services routed via us-east-1 internally; this only anchors the provider itself."
  default     = "ca-central-1"
}

variable "alert_email" {
  type        = string
  description = "Single destination for every cost alert (budgets, budget-action approvals, anomaly detection). One address on purpose — decision of 2026-07-17."
  default     = "alerts@example.com"
}

variable "action_approval_model" {
  type        = string
  description = <<-EOT
    Approval model for the circuit-breaker budget action. MANUAL means a
    triggered action emails an approval request instead of attaching the
    Deny policy; AUTOMATIC attaches it unattended. Starts MANUAL because
    the July 2026 phantom S3 charge saturates ActualSpend (16.27 M$ > 20 $),
    which would trip an AUTOMATIC action immediately for a cost that does
    not exist. Flip to AUTOMATIC (one-line tfvars/default change + apply)
    once ActualSpend is sane again — see the spec's Séquencement section.
  EOT
  default     = "MANUAL"

  validation {
    condition     = contains(["AUTOMATIC", "MANUAL"], var.action_approval_model)
    error_message = "action_approval_model must be either \"AUTOMATIC\" or \"MANUAL\"."
  }
}
```

- [ ] **Step 4: Init and validate (the "test" for a scaffold)**

Run: `cd infra/environments/guardrails && terraform init`
Expected: `Terraform has been successfully initialized!` (creates `.terraform.lock.hcl`)

Run: `terraform validate`
Expected: `Success! The configuration is valid.`

Run: `terraform fmt -check .`
Expected: no output, exit 0

- [ ] **Step 5: Commit**

```bash
git add infra/environments/guardrails/backend.tf infra/environments/guardrails/versions.tf infra/environments/guardrails/variables.tf infra/environments/guardrails/.terraform.lock.hcl
git commit -m "infra(guardrails): scaffold persistent cost-guardrails root

Separate state key so end-of-session destroys of dev can never take
the cost guardrails down with them."
```

---

### Task 2: Budgets — monthly 20 $ (6 ACTUAL thresholds + 1 FORECASTED) and daily 2 $

**Files:**
- Create: `infra/environments/guardrails/budgets.tf`

**Interfaces:**
- Consumes: `var.alert_email` (Task 1).
- Produces: `aws_budgets_budget.monthly` (name `vigil-guardrails-monthly-20usd`) — Task 4's action anchors to it. `aws_budgets_budget.daily` (name `vigil-guardrails-daily-2usd`).

- [ ] **Step 1: Write `budgets.tf`**

```hcl
# Two-layer detection (spec Composant 1). The monthly budget carries the
# 20 $ target with dense ACTUAL thresholds; the daily budget is the
# fast-twitch alarm — it resets every day, so unlike the monthly one it
# cannot be saturated for weeks by a single billing anomaly (the July
# 2026 phantom charge blinded the monthly budget for the whole month).
# Threshold math: 5/25/50/75/90/100 % of 20 $ = 1/5/10/15/18/20 $.

locals {
  monthly_actual_thresholds = [5, 25, 50, 75, 90, 100]
  daily_actual_thresholds   = [50, 100]
}

resource "aws_budgets_budget" "monthly" {
  name         = "vigil-guardrails-monthly-20usd"
  budget_type  = "COST"
  time_unit    = "MONTHLY"
  limit_amount = "20.0"
  limit_unit   = "USD"

  dynamic "notification" {
    for_each = toset(local.monthly_actual_thresholds)
    content {
      comparison_operator        = "GREATER_THAN"
      notification_type          = "ACTUAL"
      threshold                  = notification.value
      threshold_type             = "PERCENTAGE"
      subscriber_email_addresses = [var.alert_email]
    }
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    notification_type          = "FORECASTED"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = [var.alert_email]
  }
}

resource "aws_budgets_budget" "daily" {
  name         = "vigil-guardrails-daily-2usd"
  budget_type  = "COST"
  time_unit    = "DAILY"
  limit_amount = "2.0"
  limit_unit   = "USD"

  # 2 $/day trips on any real anomaly while staying quiet during idle
  # periods (~0 $/day). During active demo periods the full stack costs
  # ~1–1.5 $/day + NAT, which may brush the 50 % threshold — the spec
  # allows raising this to ~3 $ if it turns out too chatty.
  dynamic "notification" {
    for_each = toset(local.daily_actual_thresholds)
    content {
      comparison_operator        = "GREATER_THAN"
      notification_type          = "ACTUAL"
      threshold                  = notification.value
      threshold_type             = "PERCENTAGE"
      subscriber_email_addresses = [var.alert_email]
    }
  }
  # No FORECASTED block here: AWS does not support forecasted
  # notifications on DAILY budgets.
}
```

- [ ] **Step 2: Validate and review the plan diff**

Run: `terraform fmt .` then `terraform validate`
Expected: `Success! The configuration is valid.`

Run: `terraform plan -no-color 2>&1 | tail -5`
Expected: `Plan: 2 to add, 0 to change, 0 to destroy.`

- [ ] **Step 3: Lint**

Run: `tflint --init --config ../../.tflint.hcl` (first run only) then `tflint --config ../../.tflint.hcl`
Expected: no findings, exit 0

- [ ] **Step 4: Commit**

```bash
git add infra/environments/guardrails/budgets.tf
git commit -m "infra(guardrails): monthly 20\$ + daily 2\$ budgets with dense alert thresholds

The daily budget is the anomaly-proof layer: it resets every day, so a
month-long phantom charge cannot saturate it the way it blinded the
manual monthly budget in July 2026."
```

---

### Task 3: Deny policy + budget-action execution roles

**Files:**
- Create: `infra/environments/guardrails/circuit_breaker_iam.tf`

**Interfaces:**
- Consumes: nothing from other tasks (self-contained IAM).
- Produces: `aws_iam_policy.deny_expensive_creates` (name `vigil-deny-expensive-creates`), `aws_iam_role.budgets_action_execution` (name `vigil-budgets-action-execution`), `aws_iam_service_linked_role.budgets`, and `local.sso_admin_role_name` / `local.sso_admin_role_arn` — all consumed by Task 4.

- [ ] **Step 1: Write `circuit_breaker_iam.tf`**

```hcl
# IAM half of the circuit breaker (spec Composant 2). Deny-only policy:
# it blocks *creating* new billable resources but deliberately never
# touches iam:*, reads, deletes, budgets/ce/support or s3 — so a tripped
# brake can always be released by hand (detach the policy) and a
# `terraform destroy` of the dev stack still works while the brake is on.
# Lockout-proofing is the design invariant here; review any edit to this
# file against it.
#
# Caveat (documented, accepted): IAM Identity Center owns the
# AWSReservedSSO_* role. Re-provisioning the permission set strips
# manually-attached policies — i.e. re-provisioning releases the brake.
# Acceptable for a single-user portfolio account.

locals {
  # Resolved live on 2026-07-17 (aws iam list-roles --path-prefix /aws-reserved/).
  sso_admin_role_name = "AWSReservedSSO_AdministratorAccess_<suffix>"
  sso_admin_role_arn  = "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/ca-central-1/AWSReservedSSO_AdministratorAccess_<suffix>"
}

data "aws_iam_policy_document" "deny_expensive_creates" {
  statement {
    sid    = "DenyExpensiveResourceCreation"
    effect = "Deny"
    actions = [
      # Compute / containers
      "ec2:RunInstances",
      "ecs:CreateService",
      "ecs:RunTask",
      "eks:CreateCluster",
      "eks:CreateNodegroup",
      # Billed networking
      "ec2:CreateNatGateway",
      "ec2:AllocateAddress",
      "elasticloadbalancing:CreateLoadBalancer",
      # Data / ML
      "rds:CreateDBInstance",
      "rds:CreateDBCluster",
      "sagemaker:Create*",
      "elasticache:CreateCacheCluster",
      "redshift:CreateCluster",
      "elasticmapreduce:RunJobFlow",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "deny_expensive_creates" {
  name        = "vigil-deny-expensive-creates"
  description = "Circuit-breaker payload: attached to the SSO admin role by the guardrails budget action when monthly spend exceeds 20 USD. Deny-only on resource creation; never blocks IAM, reads, or deletes."
  policy      = data.aws_iam_policy_document.deny_expensive_creates.json
}

# Service-linked role Budgets needs to evaluate/execute actions at all.
# Absent on this account as of 2026-07-17 (verified: NoSuchEntity).
resource "aws_iam_service_linked_role" "budgets" {
  aws_service_name = "budgets.amazonaws.com"
}

data "aws_iam_policy_document" "budgets_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["budgets.amazonaws.com"]
    }
    # Confused-deputy guard: only Budgets acting for *this* account may assume.
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = ["123456789012"]
    }
  }
}

data "aws_iam_policy_document" "budgets_execution" {
  statement {
    sid = "AttachDetachTheBrakeOnly"
    actions = [
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
    ]
    resources = [local.sso_admin_role_arn]
    # Scoped twice: only this role, and only this one policy document.
    condition {
      test     = "ArnEquals"
      variable = "iam:PolicyARN"
      values   = [aws_iam_policy.deny_expensive_creates.arn]
    }
  }
}

resource "aws_iam_role" "budgets_action_execution" {
  name               = "vigil-budgets-action-execution"
  description        = "Assumed by budgets.amazonaws.com to attach/detach vigil-deny-expensive-creates on the SSO admin role. Scope: that one policy on that one role, nothing else."
  assume_role_policy = data.aws_iam_policy_document.budgets_assume.json
}

resource "aws_iam_role_policy" "budgets_action_execution" {
  name   = "attach-detach-brake"
  role   = aws_iam_role.budgets_action_execution.id
  policy = data.aws_iam_policy_document.budgets_execution.json
}
```

- [ ] **Step 2: Validate, plan, lint**

Run: `terraform fmt .` then `terraform validate`
Expected: `Success! The configuration is valid.`

Run: `terraform plan -no-color 2>&1 | tail -5`
Expected: `Plan: 6 to add, 0 to change, 0 to destroy.` (2 budgets + policy + SLR + role + role policy)

Run: `tflint --config ../../.tflint.hcl`
Expected: no findings, exit 0

- [ ] **Step 3: Lockout-proofing self-check (the "test" for this task)**

Verify, reading the final file: the Deny statement's `actions` list contains **no** `iam:*`, no `Describe*/List*/Get*`, no `Delete*/Terminate*`, no `s3:*`, no `budgets:*`/`ce:*`/`support:*`/`sts:*` entry. If any slipped in, remove it — this is the anti-lockout invariant from the spec.

- [ ] **Step 4: Commit**

```bash
git add infra/environments/guardrails/circuit_breaker_iam.tf
git commit -m "infra(guardrails): deny-expensive-creates policy + scoped budgets execution role

Deny-only on creations so a tripped brake never locks the operator out:
IAM, reads, deletes, and the tf state bucket all stay reachable, and the
execution role can only attach/detach this one policy on the SSO role."
```

---

### Task 4: Circuit-breaker budget action (APPLY_IAM_POLICY, MANUAL for now)

**Files:**
- Create: `infra/environments/guardrails/circuit_breaker_action.tf`

**Interfaces:**
- Consumes: `aws_budgets_budget.monthly` (Task 2); `aws_iam_policy.deny_expensive_creates`, `aws_iam_role.budgets_action_execution`, `aws_iam_service_linked_role.budgets`, `local.sso_admin_role_name` (Task 3); `var.action_approval_model`, `var.alert_email` (Task 1).
- Produces: `aws_budgets_budget_action.brake` — verified in Task 7.

- [ ] **Step 1: Write `circuit_breaker_action.tf`**

```hcl
# Wiring half of the circuit breaker: when the monthly budget's ACTUAL
# spend crosses 100 % (20 $), Budgets assumes the execution role and
# attaches the Deny policy to the SSO admin role (or, under MANUAL,
# emails an approval request first — see var.action_approval_model for
# why MANUAL is the launch setting). Reactive by design: Budgets
# evaluates a few times per day, so this brakes runaway spend in hours,
# not instantly — it is a brake, not a hard cap (spec Non-objectifs).

resource "aws_budgets_budget_action" "brake" {
  budget_name        = aws_budgets_budget.monthly.name
  action_type        = "APPLY_IAM_POLICY"
  approval_model     = var.action_approval_model
  notification_type  = "ACTUAL"
  execution_role_arn = aws_iam_role.budgets_action_execution.arn

  action_threshold {
    action_threshold_type  = "PERCENTAGE"
    action_threshold_value = 100
  }

  definition {
    iam_action_definition {
      policy_arn = aws_iam_policy.deny_expensive_creates.arn
      roles      = [local.sso_admin_role_name]
    }
  }

  subscriber {
    subscription_type = "EMAIL"
    address           = var.alert_email
  }

  # The action must not outlive its ability to execute.
  depends_on = [
    aws_iam_service_linked_role.budgets,
    aws_iam_role_policy.budgets_action_execution,
  ]
}
```

- [ ] **Step 2: Validate, plan, lint**

Run: `terraform fmt .` then `terraform validate`
Expected: `Success! The configuration is valid.`

Run: `terraform plan -no-color 2>&1 | tail -5`
Expected: `Plan: 7 to add, 0 to change, 0 to destroy.`

Run: `tflint --config ../../.tflint.hcl`
Expected: no findings, exit 0

- [ ] **Step 3: Commit**

```bash
git add infra/environments/guardrails/circuit_breaker_action.tf
git commit -m "infra(guardrails): budget action attaches the brake at 20\$ actual spend

Ships MANUAL: the July 2026 phantom charge keeps ActualSpend saturated,
so an AUTOMATIC action would trip immediately on a cost that does not
exist. One variable flip re-arms it once the bill is sane."
```

---

### Task 5: Align the existing anomaly subscription (import + re-point)

**Files:**
- Create: `infra/environments/guardrails/anomaly.tf`

**Interfaces:**
- Consumes: `var.alert_email` (Task 1). Existing AWS-side subscription/monitor ARNs (Global Constraints).
- Produces: `aws_ce_anomaly_subscription.default` under Terraform management.

- [ ] **Step 1: Write `anomaly.tf`**

```hcl
# The account came with a default anomaly monitor + subscription, but the
# subscription pointed at a stale address (outlook.com) with a 100 $ AND
# 40 % impact threshold — useless on an account whose normal spend is
# ~0-35 $/month. Rather than create a duplicate (two subscriptions = two
# alert streams), we import the existing one and reshape it: single
# alert address, absolute threshold 1 $, no percentage clause.
#
# The monitor itself stays unmanaged (AWS default "Default-Services-
# Monitor"); we only reference its ARN. Note: anomaly detection needs
# ~10 days of baseline before it can flag anything — this account is
# younger than that as of 2026-07-17, so this layer arms itself over time.

import {
  to = aws_ce_anomaly_subscription.default
  id = "arn:aws:ce::123456789012:anomalysubscription/00000000-0000-0000-0000-000000000000"
}

resource "aws_ce_anomaly_subscription" "default" {
  name      = "Default-Services-Subscription"
  frequency = "DAILY"

  monitor_arn_list = [
    "arn:aws:ce::123456789012:anomalymonitor/11111111-1111-1111-1111-111111111111",
  ]

  subscriber {
    type    = "EMAIL"
    address = var.alert_email
  }

  threshold_expression {
    dimension {
      key           = "ANOMALY_TOTAL_IMPACT_ABSOLUTE"
      values        = ["1"]
      match_options = ["GREATER_THAN_OR_EQUAL"]
    }
  }
}
```

- [ ] **Step 2: Validate, plan (expect the import), lint**

Run: `terraform fmt .` then `terraform validate`
Expected: `Success! The configuration is valid.`

Run: `terraform plan -no-color 2>&1 | tail -8`
Expected: `Plan: 1 to import, 7 to add, 1 to change, 0 to destroy.` — the import is the existing subscription; the `1 to change` is that same `aws_ce_anomaly_subscription.default` being updated post-import (subscriber + threshold). The plan output must show `# aws_ce_anomaly_subscription.default will be updated in-place` with the outlook→gmail subscriber swap and the threshold rewrite. If instead it shows the subscription being **created** or **destroyed**, STOP — the import block isn't matching; do not proceed.

Run: `tflint --config ../../.tflint.hcl`
Expected: no findings, exit 0

- [ ] **Step 3: Commit**

```bash
git add infra/environments/guardrails/anomaly.tf
git commit -m "infra(guardrails): import and re-point the default anomaly subscription

Was silently aimed at a stale outlook address with a 100\$/40% threshold
no 35\$/month account could ever reach; now single-address gmail at 1\$."
```

---

### Task 6: Documentation (infra/README.md + CLAUDE.md destroy-exemption)

**Files:**
- Modify: `infra/README.md` (add a `## Cost guardrails (persistent)` section after the existing environment docs; read the file first and match its heading style)
- Modify: `CLAUDE.md` (the `## AWS cost guardrails (PLAN.md §7)` section)

**Interfaces:**
- Consumes: everything Tasks 1–5 built (documents it).
- Produces: operator documentation; the destroy-exemption rule.

- [ ] **Step 1: Add the section to `infra/README.md`**

Content to add (adapt heading level to the file's existing structure):

```markdown
## Cost guardrails (persistent — never destroyed)

`environments/guardrails/` is a separate root with its own state key
(`guardrails/terraform.tfstate`). It is applied once and **excluded from
the end-of-session `terraform destroy`** — a budget that disappears with
the dev stack protects nothing. It provides:

- **Budgets** → `alerts@example.com`: monthly 20 $ (ACTUAL
  alerts at 1/5/10/15/18/20 $ + forecast) and daily 2 $ (ACTUAL at
  1/2 $ — the layer a month-long billing anomaly cannot saturate).
- **Anomaly detection**: the account-default subscription, imported and
  re-pointed (1 $ absolute threshold). Needs ~10 days of baseline to arm.
- **Circuit breaker**: a Budget Action attaches
  `vigil-deny-expensive-creates` (Deny-only on creating billable
  resources — EC2/ECS/EKS/NAT/EIP/ALB/RDS/SageMaker/ElastiCache/
  Redshift/EMR) to the SSO admin role when monthly ACTUAL spend crosses
  20 $. IAM, reads, deletes, and S3 are never denied, so a tripped brake
  is always releasable by hand:
  `aws iam detach-role-policy --role-name AWSReservedSSO_AdministratorAccess_<suffix> --policy-arn arn:aws:iam::123456789012:policy/vigil-deny-expensive-creates`

Deployed `MANUAL` (approval email instead of auto-attach) while the
July 2026 phantom S3 charge saturates ActualSpend; flip
`action_approval_model = "AUTOMATIC"` in `variables.tf` and re-apply
once the bill is sane. **Not a hard cap** — AWS has none; Budgets
evaluates a few times per day, so the brake acts in hours. The real
~0 $ guarantee remains: nothing deployed at rest.
```

- [ ] **Step 2: Update `CLAUDE.md`'s cost-guardrails section**

In the `## AWS cost guardrails (PLAN.md §7)` section, change the first bullet:

```markdown
- **Run `terraform destroy` at the end of every work session** (in `infra/environments/dev/`) — a single NAT gateway costs ~$32/month if left running. **Exception: never destroy `infra/environments/guardrails/`** — it holds the persistent budgets + circuit breaker (see `infra/README.md`, "Cost guardrails").
```

- [ ] **Step 3: Verify docs render and nothing else changed**

Run (repo root): `git diff --stat`
Expected: exactly 2 files changed (`infra/README.md`, `CLAUDE.md`).

- [ ] **Step 4: Commit**

```bash
git add infra/README.md CLAUDE.md
git commit -m "docs(guardrails): document the persistent root and exempt it from session destroys"
```

---

### Task 7: Apply + post-apply verification (MAIN LOOP ONLY — user-gated)

**Files:** none (operations).

**Interfaces:**
- Consumes: all resources from Tasks 1–5; user approval of the final plan output.
- Produces: live guardrails; deletion of the redundant manual budget.

- [ ] **Step 1: Full plan, shown to the user**

Run: `cd infra/environments/guardrails && terraform plan -no-color`
Expected: `Plan: 1 to import, 7 to add, 1 to change, 0 to destroy.` Present the complete output to the user and get an explicit "go" before Step 2. **Do not proceed on silence.**

- [ ] **Step 2: Apply**

Run: `terraform apply -auto-approve=false` (interactive confirm) — or `terraform apply tfplan` from a saved plan.
Expected: `Apply complete! Resources: 1 imported, 7 added, 1 changed, 0 destroyed.`

- [ ] **Step 3: Post-apply verification (spec's checklist)**

```bash
ACCT=123456789012
# 1. Both TF budgets exist
aws budgets describe-budgets --account-id $ACCT --region us-east-1 \
  --query 'Budgets[].BudgetName'
# Expected: includes "vigil-guardrails-monthly-20usd" and "vigil-guardrails-daily-2usd"

# 2. Action exists; under MANUAL + saturated ActualSpend its status may
#    be PENDING/EXECUTION_IN_PROGRESS (approval email sent) — that is
#    the designed Temps-1 behaviour, not an error. Record the status.
aws budgets describe-budget-actions-for-budget --account-id $ACCT --region us-east-1 \
  --budget-name vigil-guardrails-monthly-20usd \
  --query 'Actions[].{id:ActionId,status:Status,approval:ApprovalModel}'

# 3. Brake policy exists but is NOT attached at rest
aws iam list-attached-role-policies \
  --role-name AWSReservedSSO_AdministratorAccess_<suffix> \
  --query 'AttachedPolicies[].PolicyName'
# Expected: does NOT include "vigil-deny-expensive-creates"

# 4. Anomaly subscription re-pointed
aws ce get-anomaly-subscriptions --region us-east-1 \
  --query 'AnomalySubscriptions[].Subscribers[].Address'
# Expected: ["alerts@example.com"] only
```

- [ ] **Step 4: Delete the now-redundant manual budget (with user confirmation)**

Only after Step 3 confirms both Terraform budgets are live — the account must never have zero budgets:

```bash
aws budgets delete-budget --account-id 123456789012 --region us-east-1 \
  --budget-name "My Monthly Cost Budget"
# Verify: describe-budgets now lists exactly the two vigil-guardrails-* budgets
```

- [ ] **Step 5: Update the incident memory + commit nothing**

Update `<claude-memory>/aws-s3-billing-anomaly-2026-07.md`: guardrails deployed (date), action in MANUAL, reminder to flip to AUTOMATIC after the phantom charge reverses. No repo commit in this step (Task 6 already committed the docs).
