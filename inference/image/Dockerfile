# syntax=docker/dockerfile:1
#
# Build context is `inference/`, NOT `inference/image/` — image/pyproject.toml
# depends on vigil-common via a path source (`../common`, editable), so
# the build needs both `image/` and `common/` inside the same context to
# resolve it. compose/CI must build this with `context: inference/`,
# `dockerfile: image/Dockerfile` (the text service's Dockerfile mirrors this
# with `dockerfile: text/Dockerfile`).
#
# Multi-stage build, target: runtime image < 800 MB (CLAUDE.md "Containers"
# standard / PLAN.md Phase 2 acceptance criteria). Docker is not available on
# this dev host, so this build/size has NOT been verified locally — CI
# (Phase 6) builds this image and runs Trivy against it; treat that as the
# first real verification.
#
# Model weights are intentionally NOT baked into the image: OnnxModel reads
# MODEL_PATH from a volume mounted (or otherwise provided) at deploy time,
# fetched separately via scripts/fetch_model.py. Baking a ~90MB+ model into
# every image build would blow the size budget and the image cache for no
# benefit.

FROM python:3.12-slim AS builder

# Pin uv's own image (matches the version used in local dev) rather than
# `:latest`, for reproducible builds.
COPY --from=ghcr.io/astral-sh/uv:0.11.28 /uv /uvx /usr/local/bin/

# The venv must be built at the SAME absolute path it occupies in the
# runtime stage: console scripts (uvicorn, ...) hard-code the venv path in
# their shebang at install time, so a venv built in /build and copied to
# /app fails at exec with a misleading "no such file or directory".
WORKDIR /app

# vigil-common first: it's the path dependency, copied to the same
# absolute location (`/common`) that `uv sync` below will record in the
# editable install — the runtime stage re-copies it to that same path.
COPY common /common

# Copy manifests before app code so dependency layers cache independently of
# app code changes.
COPY image/pyproject.toml image/uv.lock ./
# --frozen: install exactly what's in uv.lock, no re-resolution.
# --no-dev: skip pytest/ruff/mypy — dev-only tooling has no place at runtime.
# --no-install-project: there's no project package to install, only its deps
# (vigil-common is a *dependency*, not this project, so it's still
# installed by this step).
RUN uv sync --frozen --no-dev --no-install-project

COPY image/app ./app

FROM python:3.12-slim AS runtime

# Never run the service as root.
RUN groupadd --system app \
    && useradd --system --gid app --home-dir /app --shell /usr/sbin/nologin app

WORKDIR /app

COPY --from=builder --chown=app:app /app/.venv /app/.venv
COPY --from=builder --chown=app:app /app/app /app/app
# vigil-common is installed *editable* (per-service lock/venv, not a uv
# workspace — see image/pyproject.toml) so the venv only points at this path
# rather than embedding a copy; it must exist at the same absolute path here
# too, or imports fail at runtime.
COPY --from=builder --chown=app:app /common /common

ENV PATH="/app/.venv/bin:${PATH}" \
    PYTHONUNBUFFERED=1 \
    MODEL_BACKEND=stub

USER app

EXPOSE 8002

# curl isn't present in python:3.12-slim; use the stdlib instead.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://localhost:8002/health', timeout=2)"]

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8002"]
