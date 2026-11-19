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
