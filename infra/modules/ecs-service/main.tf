# Generic ECS Fargate service module — instantiated 3x from
# environments/dev/main.tf (gateway, inference-text, inference-image) with
# nothing but variables differing between calls. Owns: task execution IAM
# role, CloudWatch log group, task definition, ECS service, target-tracking
# autoscaling, and optional Cloud Map registration / ALB target-group
# attachment. The ECS cluster itself, the Cloud Map namespace, and the ALB
# target group are created once in the environment and passed in — a
# cluster/namespace/target-group per service instantiation would make no
# sense.

data "aws_region" "current" {}

# --- CloudWatch Logs --------------------------------------------------

resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days
  tags              = var.tags
}

# --- IAM: task execution role only ------------------------------------
#
# Only an execution role is defined (used by the ECS agent to pull the
# image from ECR and ship logs to CloudWatch) — none of these services call
# AWS APIs from inside the application itself (env-var config only, no
# Secrets Manager / S3 / DynamoDB in this scope), so there's no task role.
# If MODEL_BACKEND=onnx ever pulls model weights from S3 at startup (see
# the dev environment's head comment on MODEL_BACKEND), that's the point
# where a scoped task role granting s3:GetObject would need to be added.

resource "aws_iam_role" "execution" {
  name_prefix = "${var.name}-exec-"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })

  tags = var.tags
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# --- Task definition ----------------------------------------------------

locals {
  # jsonencode(merge(...)) propagates sensitivity from environment_sensitive
  # onto the whole container_definitions string, which is the point: it
  # keeps API_KEYS (and anything else passed via environment_sensitive) out
  # of plan/apply CLI output. It's still visible in the ECS console/API and
  # in Terraform state, see the environment_sensitive variable's docstring.
  container_env = [
    for k, v in merge(var.environment, var.environment_sensitive) : {
      name  = k
      value = v
    }
  ]

  health_check_block = var.container_health_check == null ? {} : {
    healthCheck = {
      command     = var.container_health_check.command
      interval    = var.container_health_check.interval
      timeout     = var.container_health_check.timeout
      retries     = var.container_health_check.retries
      startPeriod = var.container_health_check.start_period
    }
  }

  container_definition = merge({
    name      = var.name
    image     = var.image
    essential = true

    portMappings = [{
      containerPort = var.container_port
      protocol      = "tcp"
    }]

    environment = local.container_env

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.this.name
        "awslogs-region"        = data.aws_region.current.name
        "awslogs-stream-prefix" = var.name
      }
    }
  }, local.health_check_block)
}

resource "aws_ecs_task_definition" "this" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.cpu)
  memory                   = tostring(var.memory)
  execution_role_arn       = aws_iam_role.execution.arn
  container_definitions    = jsonencode([local.container_definition])

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  tags = var.tags
}

# --- Cloud Map registration (optional) ------------------------------------

resource "aws_service_discovery_service" "this" {
  count = var.enable_service_discovery ? 1 : 0

  name = var.name

  dns_config {
    namespace_id   = var.service_discovery_namespace_id
    routing_policy = "MULTIVALUE"

    dns_records {
      ttl  = 10
      type = "A"
    }
  }

  health_check_custom_config {
    failure_threshold = 1
  }
}

# --- ECS service ----------------------------------------------------------

resource "aws_ecs_service" "this" {
  name            = var.name
  cluster         = var.cluster_name
  task_definition = aws_ecs_task_definition.this.arn
  desired_count   = var.min_capacity

  capacity_provider_strategy {
    capacity_provider = var.capacity_provider
    weight            = 100
    base              = 1
  }

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = [var.security_group_id]
    assign_public_ip = false
  }

  dynamic "load_balancer" {
    for_each = var.target_group_arn == null ? [] : [var.target_group_arn]
    content {
      target_group_arn = load_balancer.value
      container_name   = var.name
      container_port   = var.container_port
    }
  }

  dynamic "service_registries" {
    for_each = var.enable_service_discovery ? [1] : []
    content {
      registry_arn = aws_service_discovery_service.this[0].arn
    }
  }

  # Auto-rollback a bad deployment instead of getting stuck retrying —
  # pairs well with FARGATE_SPOT: both mechanisms lean on ECS reacting to
  # task failures rather than a human watching the rollout.
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  # Autoscaling (below) owns desired_count after the initial apply; without
  # this, every subsequent plan would show a spurious diff fighting the
  # autoscaling policy's last scaling action.
  lifecycle {
    ignore_changes = [desired_count]
  }

  tags = var.tags

  depends_on = [aws_iam_role_policy_attachment.execution]
}

# --- Autoscaling: target CPU 70%, min 1 / max 3 ---------------------------

resource "aws_appautoscaling_target" "this" {
  max_capacity       = var.max_capacity
  min_capacity       = var.min_capacity
  resource_id        = "service/${var.cluster_name}/${aws_ecs_service.this.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "cpu" {
  name               = "${var.name}-cpu-target"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.this.resource_id
  scalable_dimension = aws_appautoscaling_target.this.scalable_dimension
  service_namespace  = aws_appautoscaling_target.this.service_namespace

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value = var.cpu_target_value
  }
}
