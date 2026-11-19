variable "aws_region" {
  type        = string
  description = "AWS region for the provider. Budgets/CE are global services routed via us-east-1 internally; this only anchors the provider itself."
  default     = "ca-central-1"
}

variable "alert_email" {
  type        = string
  description = "Single destination for every cost alert (budgets, budget-action approvals, anomaly detection). One address on purpose — decision of 2026-07-17."
}

variable "aws_account_id" {
  type        = string
  description = "ID of the AWS account the guardrails protect (confused-deputy guard on the Budgets role, SSO role ARN)."

  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "aws_account_id must be a 12-digit AWS account ID."
  }
}

variable "sso_admin_role_name" {
  type        = string
  description = "Name of the IAM Identity Center admin role the circuit breaker attaches its Deny policy to (AWSReservedSSO_AdministratorAccess_<suffix>)."
}

variable "anomaly_subscription_arn" {
  type        = string
  description = "ARN of the account's existing default Cost Anomaly Detection subscription, imported and reshaped by anomaly.tf."
}

variable "anomaly_monitor_arn" {
  type        = string
  description = "ARN of the account's default Cost Anomaly Detection monitor (left unmanaged, only referenced)."
}

variable "action_approval_model" {
  type        = string
  description = <<-EOT
    Approval model for the circuit-breaker budget action. MANUAL means a
    triggered action emails an approval request instead of attaching the
    Deny policy; AUTOMATIC attaches it unattended. Starts MANUAL because
    the July 2026 phantom S3 charge saturates ActualSpend (16.27 M$ > 20 $),
    which would trip an AUTOMATIC action immediately for a cost that does
    not exist. Flip to AUTOMATIC (one-line tfvars/default change + apply)
    once ActualSpend is sane again — see the spec's Séquencement section.

    Only flip after the billing period resets (Aug 1+) or the phantom
    charge is reversed. Before applying the flip, check the action's
    status is STANDBY (`aws budgets describe-budget-actions-for-budget`);
    if a pending approval request from the saturated month still exists,
    decline/dismiss it first — a triggered action can make
    UpdateBudgetAction fail with ResourceLockedException.
  EOT
  default     = "MANUAL"

  validation {
    condition     = contains(["AUTOMATIC", "MANUAL"], var.action_approval_model)
    error_message = "action_approval_model must be either \"AUTOMATIC\" or \"MANUAL\"."
  }
}
