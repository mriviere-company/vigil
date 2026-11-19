# ALB module: public HTTP listener -> gateway target group only. The two
# inference services are never reachable from the ALB (PLAN.md §2) — they
# sit behind Cloud Map / the gateway's security group instead (see the
# ecs-service module's enable_service_discovery option).
#
# HTTP only, no ACM/listener on 443: there's no domain in scope for this
# dev/portfolio environment. Documented as a limitation in infra/README.md
# rather than worked around with a self-signed cert, which would just move
# the "not really secure" problem without adding anything real.

resource "aws_lb" "this" {
  name               = "${var.name_prefix}-alb"
  internal           = false
  load_balancer_type = "application"
  subnets            = var.public_subnet_ids
  security_groups    = [var.alb_security_group_id]

  # Dev environment gets destroyed at the end of every session (PLAN.md
  # §7 / infra/README.md) — deletion protection would turn that into a
  # two-step manual process, so it's left off here.
  enable_deletion_protection = false

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-alb"
  })
}

resource "aws_lb_target_group" "gateway" {
  name        = "${var.name_prefix}-gateway-tg"
  port        = var.target_port
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip" # required: Fargate awsvpc-mode tasks register by IP, not instance ID

  health_check {
    path                = var.health_check_path
    matcher             = "200"
    protocol            = "HTTP"
    interval            = 30
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-gateway-tg"
  })
}

# Plain-HTTP listener is a documented, accepted limitation — HTTPS needs a
# domain + ACM certificate, both explicitly out of scope for this dev
# environment (PLAN.md Phase 5: "HTTP documenté comme limitation"; see
# infra/README.md "Limitations"). Suppressed inline, next to the resource
# it excuses, rather than via a repo-wide .trivyignore.
#trivy:ignore:AVD-AWS-0054
resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  # Default action = catch-all forward to the gateway. ALB evaluates
  # listener *rules* (below) before falling back to this default action
  # regardless of resource declaration order, so the /metrics block still
  # applies first for matching paths — see aws_lb_listener_rule.block_metrics.
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.gateway.arn
  }
}

# /metrics must never be reachable through the public ALB (PLAN.md §3:
# Prometheus /metrics is "non exposé publiquement via ALB") — matched and
# short-circuited with a fixed 403 ahead of the default forward action.
resource "aws_lb_listener_rule" "block_metrics" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 1 # low number = evaluated before the listener's default forward

  action {
    type = "fixed-response"

    fixed_response {
      content_type = "text/plain"
      message_body = "403 Forbidden"
      status_code  = "403"
    }
  }

  condition {
    path_pattern {
      values = ["/metrics*"]
    }
  }
}

# p95 latency alarm — no SNS topic/action wired up (kept out of scope per
# the brief; see infra/README.md). Still useful on its own: visible ALARM
# state in the CloudWatch console/API without needing to build a
# notification pipeline for a dev environment nobody's paging on.
resource "aws_cloudwatch_metric_alarm" "p95_latency" {
  alarm_name          = "${var.name_prefix}-alb-p95-latency"
  alarm_description   = "ALB p95 TargetResponseTime above ${var.alarm_response_time_threshold_seconds}s (PLAN.md p95 < 200ms target). SNS notification intentionally not wired up — see infra/README.md."
  namespace           = "AWS/ApplicationELB"
  metric_name         = "TargetResponseTime"
  extended_statistic  = "p95"
  period              = 60
  evaluation_periods  = 3
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.alarm_response_time_threshold_seconds
  treat_missing_data  = "notBreaching"

  dimensions = {
    LoadBalancer = aws_lb.this.arn_suffix
  }

  alarm_actions = []
  ok_actions    = []

  tags = var.tags
}
