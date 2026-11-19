# infra/ — Terraform (AWS ECS Fargate)

IaC for the vigil stack (PLAN.md §2): an ALB in front of a Go gateway,
which calls two Python inference services (text, image) over Cloud Map
service discovery. Everything runs on ECS Fargate in private subnets behind
a single NAT gateway, in one VPC across two AZs.

```
infra/
├── modules/
│   ├── network/       # VPC, subnets, NAT, route tables, security groups
│   ├── ecs-service/   # generic Fargate service — instantiated 3x
│   ├── alb/           # ALB, HTTP listener, /metrics block rule, p95 alarm
│   └── ecr/           # ECR repo(s) + keep-last-5 lifecycle policy
├── environments/dev/  # composes the four modules into the dev stack
├── scripts/bootstrap-backend.sh
└── README.md
```

`modules/ecs-service` is the reusable piece: `environments/dev/main.tf` calls
it three times (gateway, inference-text, inference-image) with nothing but
variables differing — image, cpu/mem, port, env vars, health-check shape,
and whether it attaches to the ALB target group or registers in Cloud Map.
No service-specific resource blocks are copy-pasted between the three.

## Architecture at a glance

```
client --HTTP:80--> ALB --:8080--> gateway (Fargate, private subnet)
                                      |  Cloud Map: text.vigil.local:8001
                                      |  Cloud Map: image.vigil.local:8002
                                      v
                          inference-text / inference-image (Fargate, private subnet)
```

- **/metrics is blocked at the ALB** (a listener rule returns a fixed 403
  ahead of the default forward) — Prometheus metrics are never reachable
  from outside the VPC (PLAN.md §3).
- **Security groups** are the real segmentation boundary: ALB SG accepts
  80 from anywhere; gateway SG accepts 8080 *only* from the ALB SG;
  inference SG accepts 8001/8002 *only* from the gateway SG. Egress is
  scoped per hop too (each rule commented in `modules/network/main.tf`):
  tcp/443 out for ECR pulls + CloudWatch log delivery, DNS to the VPC
  resolver for Cloud Map lookups, and — gateway only — tcp/8001/8002 to
  the inference SG. The 443 rules' *destination* stays 0.0.0.0/0: pinning
  it to AWS endpoint IPs needs VPC interface endpoints, out of scope here
  (that residual carries an inline, justified `trivy:ignore` for
  AVD-AWS-0104).
- **Single NAT gateway**, not one per AZ: ~$32/month vs ~$64/month, at the
  cost of both AZs sharing one failure domain for egress. Acceptable for an
  environment that's destroyed after every session (see below).

## Cost estimate (approximate; line items priced at us-east-1 rates, ca-central-1 — this repo's deploy region — runs ~5–10% higher)

| Scenario | NAT GW | ALB | Fargate compute (3 tasks, min capacity) | Logs + ECR | **Total/mo** | **Total/hr** |
|---|---|---|---|---|---|---|
| On-demand FARGATE, 24/7 (baseline, not this repo's default) | ~$33 | ~$18 | ~$45 | ~$3 | **~$99** (~$100) | **~$0.13** |
| **FARGATE_SPOT + NAT (this repo's dev default)** | ~$33 | ~$18 | ~$13 | ~$3 | **~$67** | **~$0.09** |
| FARGATE_SPOT + public subnets, no NAT (documented future option, **not implemented**) | $0 | ~$18 | ~$13 | ~$3 | **~$34** (~$33) | **~$0.05** |

Compute assumes min capacity (1 task per service, no autoscale-out): gateway
0.25 vCPU/512MB + 2× inference 0.5 vCPU/1GB, at Fargate on-demand pricing
($0.04048/vCPU-hr + $0.004445/GB-hr) or its ~30%-of-on-demand Spot price.
Scaling out (up to 3 tasks/service under load) raises the compute line
proportionally — the NAT/ALB fixed costs don't change.

**Why FARGATE_SPOT is the dev default** (see
`modules/ecs-service`'s `capacity_provider` variable): ~70% cheaper compute,
and this workload already tolerates a task disappearing without warning —
the gateway's circuit breaker treats a downstream vanishing as a normal
failure mode (opens, degrades to `202 pending`) and ECS autoscaling + the
service's deployment circuit breaker replace an interrupted task
automatically. Nothing here needs on-demand's placement guarantee.

**Not built, listed for completeness**: a no-NAT / public-subnet variant
(Fargate tasks get public IPs directly, no NAT gateway) would drop another
~$33/month but removes a real defense-in-depth layer (tasks would need
security-group-only protection with no network-layer egress control) — the
brief for this phase explicitly scopes it out as a future option rather
than something to build now.

## Backend: S3 with native locking

`environments/dev/backend.tf` points at a fixed S3 bucket (backend blocks
can't reference variables, so the values are literal, not tfvars-driven)
and uses Terraform's native S3 lockfile (`use_lockfile`, GA since 1.11) for
state locking. **Run `scripts/bootstrap-backend.sh` once, before the first
`terraform init`** — it creates the bucket idempotently (checks before
creating, safe to re-run) and needs real AWS credentials.

This project originally used the classic S3 + DynamoDB lock-table setup;
Terraform 1.15 deprecates `dynamodb_table`, so it migrated to the native
lockfile before the first state write (2026-07-11 — nothing to migrate).
If you're here for the interview-classic pattern: it's a one-line swap back
(`dynamodb_table = "<table>"` in place of `use_lockfile = true`) plus a
lock table with a `LockID` string hash key.

## Deploy / destroy procedure

**Prerequisite — build and push the three images.** The ECR repositories
are created by this same Terraform run, so bootstrap them first with a
scoped apply before the full one:

```bash
# 1. Bootstrap the backend (once, ever)
./infra/scripts/bootstrap-backend.sh

# 2. Init + create just the ECR repos first (they must exist before images
#    can be pushed, and before the ECS services in the full apply can pull)
cd infra/environments/dev
export TF_VAR_api_keys="<free-key>:free,<pro-key>:pro"   # your own keys, never committed
terraform init
terraform apply -target=module.ecr

# 3. Build and push the three images (build contexts matter — see each
#    Dockerfile's header comment for why). --platform linux/amd64 is
#    required, not optional: the ECS task definitions pin
#    runtime_platform.cpu_architecture = "X86_64" (modules/ecs-service),
#    so an image built natively on an ARM host (e.g. Apple Silicon) would
#    push fine but fail to run at the first apply.
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
REGION=ca-central-1
aws ecr get-login-password --region "$REGION" \
  | docker login --username AWS --password-stdin "$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com"

# gateway: build context is gateway/ itself (its go.mod lives there)
docker buildx build --platform linux/amd64 \
  -t "$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com/vigil-gateway:latest" --load gateway
docker push "$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com/vigil-gateway:latest"

# inference-text: build context is inference/ (path dependency on ../common)
docker buildx build --platform linux/amd64 -f inference/text/Dockerfile \
  -t "$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com/vigil-text:latest" --load inference
docker push "$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com/vigil-text:latest"

# inference-image: same pattern, dockerfile image/Dockerfile
docker buildx build --platform linux/amd64 -f inference/image/Dockerfile \
  -t "$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com/vigil-image:latest" --load inference
docker push "$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com/vigil-image:latest"

# 4. Full apply — now the images exist for the ECS services to pull
terraform apply

# 5. Smoke-test via the ALB
curl "$(terraform output -raw alb_url)/v1/health"
curl -H "X-API-Key: <free-key>" -X POST "$(terraform output -raw alb_url)/v1/moderate/text" \
  -d '{"content": "hello world"}'

# 6. ALWAYS destroy when you're done for the session
terraform destroy
```

**`terraform destroy` must run at the end of every work session.** The
single NAT gateway alone costs ~$32/month if left running, and the point of
this environment is that rebuilding it is trivial — there is no reason to
ever leave it up between sessions.

The account-level safety net (PLAN.md §7) is now provisioned by
Terraform in `environments/guardrails/` — applied once, persistent,
never destroyed with the dev stack (see "Cost guardrails" below). The
historical advice to set up a Budget alert manually in the console
before the first real `apply` is superseded: that manual budget gets
deleted once the guardrails budgets are live.

**Deploy timing** (measured at the first real deploy, 2026-07-11,
ca-central-1): bootstrap script: **< 1 min**; `terraform apply` (full
stack, 61 resources): **4 min 20 s** — measured before the images were
pushed, which doesn't change the apply itself (Terraform doesn't wait for
ECS steady state); tasks reached healthy behind the ALB **~2 min after
the image push** completed. `terraform destroy`: **3 min 02 s**
(the first run also surfaced the ECR not-empty failure since fixed with
`force_delete = true` in the ecr module — repos are never empty under this
project's destroy-after-session discipline).

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

Only do that flip after the billing period resets (Aug 1+) or the
phantom charge is reversed, and check the action is `STANDBY`
(`aws budgets describe-budget-actions-for-budget`) first — dismiss any
pending approval request from the saturated month, or `UpdateBudgetAction`
fails with `ResourceLockedException`. Separately, expect an email burst
on the day this root is first applied during the saturated month: every
monthly ACTUAL threshold is already crossed, so all of them notify at
once, plus one approval-request email for the budget action — that's
designed behavior, not a failure.

## Variables worth knowing about

| Variable | Default | Notes |
|---|---|---|
| `capacity_provider` | `FARGATE` (module) / `FARGATE_SPOT` (`dev.auto.tfvars`) | Cost lever, see above. |
| `api_keys` | none (required) | Passed from the environment (`export TF_VAR_api_keys="<key>:free,<key>:pro"`) or `-var`, never committed (PLAN.md scope decision: keys live in config/env). `deploy.yml` reads them from the `VIGIL_API_KEYS` secret. |
| `model_backend` | `stub` | The only mode exercised on this Fargate dev environment — see the head comment above the inference `ecs-service` calls in `environments/dev/main.tf` for why `onnx` (real models) is out of scope here. |
| `image_tag` | `latest` | Set to a commit SHA for CI-driven deploys (Phase 6). |

## Limitations

- **HTTP only, no ACM/domain.** No TLS in scope — see `modules/alb`'s
  header comment. Don't send real secrets through this ALB.
- **API keys in plaintext env vars / Terraform state**, not Secrets
  Manager — documented scope decision (PLAN.md), see
  `modules/ecs-service`'s `environment_sensitive` variable.
- **No task IAM role.** None of the three services call AWS APIs directly
  today; only the task *execution* role exists (ECR pull + log shipping).
  Adding real ONNX model loading via S3 would need a scoped task role — see
  the comment in `modules/ecs-service/main.tf`.
- **Real `terraform plan`/`apply`, ECR pushes, and backend bootstrap have been
  executed.** The first real deploy ran 2026-07-11 in ca-central-1 (61
  resources, `apply` 4 min 20 s — see §Deploy timing above); the three images
  were built and pushed, the ECS services reached healthy behind the ALB, and
  the API was validated live (`GET /v1/health` and a moderation `POST`) before
  `terraform destroy` (3 min 02 s) per the cost-guardrail rule (PLAN.md §7).
  The environment is intentionally ephemeral — it is not left running between
  sessions, so there is no persistent stack to check against; a fresh `apply`
  is the way to see it live again.
