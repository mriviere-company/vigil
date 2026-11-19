#!/usr/bin/env bash
#
# One-time (idempotent) setup of the Terraform remote state backend:
# an S3 bucket for state files. State locking uses Terraform's native S3
# lockfile (`use_lockfile` in backend.tf) — no DynamoDB table needed (the
# classic `dynamodb_table` setup was migrated away 2026-07-11 after
# Terraform 1.15 deprecated the parameter).
# Run this ONCE, before the first `terraform init` in
# infra/environments/dev/ (or any future environment sharing this
# backend). Re-running it is safe: every step checks whether its
# resource already exists before creating it.
#
# Defaults match the literals hardcoded in
# infra/environments/dev/backend.tf exactly (Terraform backend blocks
# can't reference variables, so those values have to be literal — see
# that file's header comment). Override via env vars if you need a
# different bucket/region, and pass the matching values to
# `terraform init -backend-config=...` to match.
#
# Requires: aws cli v2, configured credentials with permission to create
# S3 buckets. Run it once from a machine with real credentials, before
# the first `terraform init`/`apply` (first executed 2026-07-11,
# ca-central-1).

set -euo pipefail

BUCKET="${TF_STATE_BUCKET:-vigil-api-tfstate-dev}"
REGION="${AWS_REGION:-ca-central-1}"

log() {
  printf '[bootstrap-backend] %s\n' "$1"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: '$1' is required but not found on PATH" >&2
    exit 1
  fi
}

require_cmd aws

log "bucket=${BUCKET} region=${REGION}"

# --- S3 state bucket -------------------------------------------------

if aws s3api head-bucket --bucket "$BUCKET" 2>/dev/null; then
  log "S3 bucket ${BUCKET} already exists — skipping creation"
else
  log "creating S3 bucket ${BUCKET}"
  if [ "$REGION" = "us-east-1" ]; then
    # us-east-1 is the one region that rejects an explicit
    # LocationConstraint matching itself.
    aws s3api create-bucket --bucket "$BUCKET" --region "$REGION"
  else
    aws s3api create-bucket \
      --bucket "$BUCKET" \
      --region "$REGION" \
      --create-bucket-configuration "LocationConstraint=${REGION}"
  fi
fi

log "ensuring versioning is enabled (state history / recovery from a bad apply)"
aws s3api put-bucket-versioning \
  --bucket "$BUCKET" \
  --versioning-configuration Status=Enabled

log "ensuring default encryption (SSE-S3/AES256) is set"
aws s3api put-bucket-encryption \
  --bucket "$BUCKET" \
  --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'

log "ensuring public access is fully blocked (state may contain sensitive values)"
aws s3api put-public-access-block \
  --bucket "$BUCKET" \
  --public-access-block-configuration \
  BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

log "done — backend ready. Run: terraform init (from infra/environments/dev)"
