# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

Greenfield repository — no code exists yet. **`PLAN.md` is the authoritative execution plan** (written in French): follow its phases in order (Phase 0 → 7), and verify each phase's acceptance criteria before moving to the next. Naming note: the product is vigil (repo `vigil-api`); "ModGuard" was the working codename until 2026-07-12 (git history keeps it).

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
- **`infra/`** — Terraform modules (network, generic ecs-service instantiated 3×, alb, ecr) with S3+DynamoDB remote state.

Degraded mode: when an inference service's circuit is open, the gateway returns `202 { "status": "pending", "retry_after": 30 }` rather than failing. API keys live in config/env, not a DB (documented scope decision).

The full API contract is in PLAN.md §3 and gets frozen into `docs/api.md` in Phase 0 — that file becomes the source of truth for endpoints and response schemas.

## Commands

A Makefile is the entry point for everything (created in Phase 0). Planned targets: `make build`, `make test`, `make lint`, `make up`, `make down`, `make load-test`. Local stack runs via `docker-compose` (3 services + Prometheus + Grafana).

Testing requirements:
- Go: `go test -race ./...` — race detector always on; coverage > 80% on `gateway/internal/`. Circuit breaker state transitions tested with an injected `Clock` interface; end-to-end gateway tests via `httptest` with mocked inference servers.
- Python: `pytest` with StubModel fixtures; lint with `ruff` (strict config).
- Terraform: `terraform fmt` / `validate` / `tflint` must be clean.
- CI (Phase 6) also enforces: Go coverage gate, mypy, gitleaks (full history), Trivy on images and IaC — keep these clean locally before pushing.

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
