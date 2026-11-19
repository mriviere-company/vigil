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

  # prevent_destroy is the only mechanical enforcement of this root's
  # "never destroyed" invariant (docs alone don't stop an accidental
  # destroy). Trade-off: rename-replacements need the block lifted first
  # — acceptable here.
  lifecycle {
    prevent_destroy = true
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
  # ~1–1.5 $/day plus the NAT gateway's ~1.07 $/day, i.e. ~2.1–2.6 $/day —
  # that reaches or exceeds the 100 % threshold on every demo day, not
  # just brushes 50 %. Acceptable awareness noise; the spec allows
  # raising this to ~3 $ if it turns out too chatty.
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

  # See budgets.tf for the prevent_destroy rationale.
  lifecycle {
    prevent_destroy = true
  }
}
