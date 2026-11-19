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
  id = var.anomaly_subscription_arn
}

resource "aws_ce_anomaly_subscription" "default" {
  name      = "Default-Services-Subscription"
  frequency = "DAILY"

  monitor_arn_list = [var.anomaly_monitor_arn]

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

  # See budgets.tf for the prevent_destroy rationale.
  lifecycle {
    prevent_destroy = true
  }
}
