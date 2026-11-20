# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

All eight phases of `PLAN.md` (0 → 7) are delivered: the gateway, both inference services, the local stack with load tests, the Terraform infrastructure, CI/CD and the portfolio README. **`README.md` describes what ships and what was measured; `PLAN.md` (in French) stays the design record** — the original execution plan, its acceptance criteria and its decisions. New work follows the same discipline: plan first, then verify acceptance criteria before moving on.

Naming note: the product and this public repository are `vigil`. It was developed in a private repository (`vigil-api`) and published here as a sanitized single-commit snapshot; "ModGuard" was the working codename until 2026-07-12.

## Model/effort routing (required rule)

- Every implementation plan written for this repo (PLAN.md phases, plan-mode plans, task breakdowns) must annotate each phase/step with the best-fit Claude model (Haiku 4.5 → Sonnet 5 → Opus 4.8 → Fable 5) and reasoning effort (low/medium/high). Match the tier to the difficulty of the step — the goal is to not waste tokens on work a cheaper tier handles.
- When executing a plan, apply the annotations yourself at each step: run the step's implementation through subagents on the matching tier (Agent tool `model` param; Workflow `agent()` `model`/`effort` opts). Claude cannot switch the main conversation's own model — if a step must run in the main loop on a different tier, pause and ask the user to switch with `/model`.
- If a step resists (repeated test failures, subtle concurrency bugs), escalate one tier — effort first, then model — and say so; de-escalate for mechanical follow-ups.

Current per-phase assignments are annotated under each phase heading in PLAN.md §5.
- When writing a plan, every transverse standard (like PLAN.md §6) must be attached to an owning phase's checklist — a standard with no owning phase is invisible to per-phase reviews and only surfaces at the final whole-branch review (this is how `.env.example` was missed for eight phases).

## What this is

A portfolio project demonstrating AI Infrastructure Engineering: a content-moderation REST API where clients submit text or images and get a verdict (`approved` / `flagged` / `rejected`) in < 200ms p95. **The ML is not the point; production serving is** — resilience, observability, IaC, cost control.

## Target architecture

Three services behind an ALB on AWS ECS Fargate (private subnets), provisioned with Terraform:

- **`gateway/`** — Go 1.22+. API-key auth with tiers (free/pro), per-key token-bucket rate limiting, HTTP proxying to inference services (explicit timeouts, retries with backoff+jitter), **hand-written circuit breaker** (do not import sony/gobreaker — implementing it with tests is a deliberate choice; document the alternative), backpressure via semaphore, verdict aggregation from scores (configurable thresholds: rejected > 0.85, flagged > 0.5), Prometheus metrics, slog JSON logging.
- **`inference/text/` and `inference/image/`** — Python 3.12 / FastAPI / Pydantic v2, ONNX Runtime on CPU (toxicity/spam and NSFW respectively). Model behind a `ModelBackend` protocol with a deterministic `StubModel` for tests, injected via env var. Shared code goes in `inference/common/` — no copy-paste between text and image.
- **`infra/`** — Terraform modules (network, generic ecs-service instantiated 3×, alb, ecr) with S3 remote state and native lockfile locking (`use_lockfile`; migrated from S3+DynamoDB on 2026-07-11). Two environments: `dev` (the on-demand stack) and `guardrails` (persistent budgets + cost circuit breaker).

Degraded mode: when an inference service's circuit is open, the gateway returns `202 { "status": "pending", "retry_after": 30 }` rather than failing. API keys live in config/env, not a DB (documented scope decision).

The full API contract is in PLAN.md §3 and gets frozen into `docs/api.md` in Phase 0 — that file becomes the source of truth for endpoints and response schemas.

## Commands

The Makefile is the entry point for everything: `make build`, `make test`, `make lint`, `make fmt`, `make up`, `make down`, `make load-test`, `make load-test-burst` (`make help` lists them). The local stack runs via `docker-compose` (3 services + Prometheus + Grafana).

Testing requirements:
- Go: `go test -race ./...` — race detector always on; coverage > 80% on `gateway/internal/`. Circuit breaker state transitions tested with an injected `Clock` interface; end-to-end gateway tests via `httptest` with mocked inference servers.
- Python: `pytest` with StubModel fixtures; lint with `ruff` (strict config).
- Terraform: `terraform fmt` / `validate` / `tflint` must be clean.
- CI also enforces: Go coverage gate, mypy, gitleaks (full history), Trivy on images and IaC — keep these clean locally before pushing.

## Standards (PLAN.md §6 — apply in every phase)

- Commits: conventional (`feat:`, `fix:`, `test:`, `infra:`), small, message explains the *why*.
- Go: no `utils` package; wrap errors (`fmt.Errorf("...: %w", err)`); propagate `context.Context` everywhere; no mutable globals.
- Python: type hints everywhere; Pydantic at every I/O boundary.
- Config: 12-factor, everything via env vars with sane local defaults; `.env.example` provided.
- Containers: multi-stage builds, non-root. Gateway targets `gcr.io/distroless/static` with a static binary < 20 MB; Python images < 800 MB with HEALTHCHECK.
- Each implemented design pattern (circuit breaker, token bucket, backpressure, graceful shutdown) gets a short header comment explaining the choice — it's interview material.

## AWS cost guardrails (PLAN.md §7)

- **Run `terraform destroy` at the end of every work session** (in `infra/environments/dev/`) — a single NAT gateway costs ~$32/month if left running. **Exception: never destroy `infra/environments/guardrails/`** — it holds the persistent budgets + circuit breaker (see `infra/README.md`, "Cost guardrails").
- Minimal Fargate sizing: 0.25 vCPU / 512 MB (gateway), 0.5 vCPU / 1 GB (inference); min 1 / max 3 tasks.
- One NAT gateway total (documented cost trade-off vs one per AZ).

## Secrets and account-specific values (public repository)

- **Never commit API keys.** `api_keys` has no default: pass it with `export TF_VAR_api_keys=...` or `-var` (`deploy.yml` reads the `VIGIL_API_KEYS` secret). A value in a committed `*.auto.tfvars` would silently override `TF_VAR_*` and be readable by anyone.
- **No AWS account ID, role name, ARN or personal e-mail in the code.** `guardrails` takes them as variables: copy `guardrails.auto.tfvars.example` to `guardrails.auto.tfvars` (git-ignored). Docs use the `123456789012` placeholder.
- The local defaults `dev-free` / `dev-pro` in `docker-compose.yml` and `.env.example` are for the local stack only, never for a deployed environment.
