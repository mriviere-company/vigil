# Shared TFLint config for every module and environment under infra/.
# Invoke as: tflint --chdir=<module-or-env-dir> --config=<repo-root>/infra/.tflint.hcl
#
# The aws ruleset plugin catches AWS-schema-specific issues (invalid
# instance types, deprecated arguments, etc.) on top of the bundled core
# terraform ruleset. It requires one `tflint --init` (downloads the plugin
# binary from GitHub releases) before first use.

plugin "aws" {
  enabled = true
  version = "0.35.0"
  source  = "github.com/terraform-linters/tflint-ruleset-aws"
}

plugin "terraform" {
  enabled = true
  preset  = "recommended"
}
