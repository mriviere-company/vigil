# IAM half of the circuit breaker (spec Composant 2). Deny-only policy:
# it blocks *creating* new billable resources but deliberately never
# touches iam:*, reads, deletes, budgets/ce/support or s3 — so a tripped
# brake can always be released by hand (detach the policy) and a
# `terraform destroy` of the dev stack still works while the brake is on.
# Lockout-proofing is the design invariant here; review any edit to this
# file against it.
#
# Caveat (documented, accepted): IAM Identity Center owns the
# AWSReservedSSO_* role. Re-provisioning the permission set strips
# manually-attached policies — i.e. re-provisioning releases the brake.
# Acceptable for a single-user portfolio account.

locals {
  # Account-specific values come from variables (see guardrails.auto.tfvars.example);
  # resolve the role name with: aws iam list-roles --path-prefix /aws-reserved/
  sso_admin_role_name = var.sso_admin_role_name
  sso_admin_role_arn  = "arn:aws:iam::${var.aws_account_id}:role/aws-reserved/sso.amazonaws.com/${var.aws_region}/${var.sso_admin_role_name}"
}

data "aws_iam_policy_document" "deny_expensive_creates" {
  statement {
    sid    = "DenyExpensiveResourceCreation"
    effect = "Deny"
    actions = [
      # Compute / containers
      "ec2:RunInstances",
      "ecs:CreateService",
      "ecs:RunTask",
      "eks:CreateCluster",
      "eks:CreateNodegroup",
      # Billed networking
      "ec2:CreateNatGateway",
      "ec2:AllocateAddress",
      "elasticloadbalancing:CreateLoadBalancer",
      # Data / ML
      "rds:CreateDBInstance",
      "rds:CreateDBCluster",
      "sagemaker:Create*",
      "elasticache:CreateCacheCluster",
      "redshift:CreateCluster",
      "elasticmapreduce:RunJobFlow",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "deny_expensive_creates" {
  name        = "vigil-deny-expensive-creates"
  description = "Circuit-breaker payload: attached to the SSO admin role by the guardrails budget action when monthly spend exceeds 20 USD. Deny-only on resource creation; never blocks IAM, reads, or deletes."
  policy      = data.aws_iam_policy_document.deny_expensive_creates.json

  # See budgets.tf for the prevent_destroy rationale.
  lifecycle {
    prevent_destroy = true
  }
}

# Service-linked role Budgets needs to evaluate/execute actions at all.
# Absent on this account as of 2026-07-17 (verified: NoSuchEntity).
resource "aws_iam_service_linked_role" "budgets" {
  aws_service_name = "budgets.amazonaws.com"
}

data "aws_iam_policy_document" "budgets_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["budgets.amazonaws.com"]
    }
    # Confused-deputy guard: only Budgets acting for *this* account may assume.
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.aws_account_id]
    }
  }
}

data "aws_iam_policy_document" "budgets_execution" {
  statement {
    sid = "AttachDetachTheBrakeOnly"
    actions = [
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
    ]
    resources = [local.sso_admin_role_arn]
    # Scoped twice: only this role, and only this one policy document.
    condition {
      test     = "ArnEquals"
      variable = "iam:PolicyARN"
      values   = [aws_iam_policy.deny_expensive_creates.arn]
    }
  }
}

resource "aws_iam_role" "budgets_action_execution" {
  name               = "vigil-budgets-action-execution"
  description        = "Assumed by budgets.amazonaws.com to attach/detach vigil-deny-expensive-creates on the SSO admin role. Scope: that one policy on that one role, nothing else."
  assume_role_policy = data.aws_iam_policy_document.budgets_assume.json
}

resource "aws_iam_role_policy" "budgets_action_execution" {
  name   = "attach-detach-brake"
  role   = aws_iam_role.budgets_action_execution.id
  policy = data.aws_iam_policy_document.budgets_execution.json
}
