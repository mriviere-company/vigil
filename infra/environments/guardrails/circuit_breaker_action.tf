# Wiring half of the circuit breaker: when the monthly budget's ACTUAL
# spend crosses 100 % (20 $), Budgets assumes the execution role and
# attaches the Deny policy to the SSO admin role (or, under MANUAL,
# emails an approval request first — see var.action_approval_model for
# why MANUAL is the launch setting). Reactive by design: Budgets
# evaluates a few times per day, so this brakes runaway spend in hours,
# not instantly — it is a brake, not a hard cap (spec Non-objectifs).

resource "aws_budgets_budget_action" "brake" {
  budget_name        = aws_budgets_budget.monthly.name
  action_type        = "APPLY_IAM_POLICY"
  approval_model     = var.action_approval_model
  notification_type  = "ACTUAL"
  execution_role_arn = aws_iam_role.budgets_action_execution.arn

  action_threshold {
    action_threshold_type  = "PERCENTAGE"
    action_threshold_value = 100
  }

  definition {
    iam_action_definition {
      policy_arn = aws_iam_policy.deny_expensive_creates.arn
      roles      = [local.sso_admin_role_name]
    }
  }

  subscriber {
    subscription_type = "EMAIL"
    address           = var.alert_email
  }

  # The action must not outlive its ability to execute.
  depends_on = [
    aws_iam_service_linked_role.budgets,
    aws_iam_role_policy.budgets_action_execution,
  ]

  # See budgets.tf for the prevent_destroy rationale.
  lifecycle {
    prevent_destroy = true
  }
}
