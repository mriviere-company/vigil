# Remote state: S3 for the state file, with Terraform's native S3 locking
# (`use_lockfile`, GA since 1.11) — a lock object in the same bucket, no
# second AWS resource to manage. The bucket must exist *before* the first
# `terraform init` here — run scripts/bootstrap-backend.sh once (idempotent).
#
# Terraform backend configuration blocks cannot reference input variables
# or locals — the backend has to be resolvable before any variable is
# evaluated, so these values are necessarily literal, not var-driven. To
# point this environment at a different bucket (e.g. a second AWS account
# or a renamed bucket), pass `-backend-config` flags or a
# `-backend-config=path/to/override.hcl` file at `terraform init` time
# rather than hand-editing this file. scripts/bootstrap-backend.sh's own
# defaults match the literals below, so the common case needs no override.
#
# History: this originally used the classic DynamoDB lock table
# (`dynamodb_table`), the long-standing standard setup. Terraform 1.15
# deprecates that parameter outright, which tipped the trade-off — migrated
# to the native lockfile on 2026-07-11, before any state was written
# (nothing to migrate). The DynamoDB variant remains a one-line swap if
# ever needed for older Terraform versions.
terraform {
  backend "s3" {
    bucket       = "vigil-api-tfstate-dev"
    key          = "dev/terraform.tfstate"
    region       = "ca-central-1"
    use_lockfile = true
    encrypt      = true
  }
}
