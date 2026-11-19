variable "name" {
  type        = string
  description = "Service name — used as the ECS service/task-family name, CloudWatch log group suffix, and (when enable_service_discovery is true) the Cloud Map service name."
}

variable "cluster_name" {
  type        = string
  description = "Name of the ECS cluster this service is deployed into."
}

variable "image" {
  type        = string
  description = "Full container image URI, including tag (e.g. \"<account>.dkr.ecr.<region>.amazonaws.com/vigil-gateway:latest\")."
}

variable "cpu" {
  type        = number
  description = "Fargate task vCPU units (256 = 0.25 vCPU, 512 = 0.5 vCPU, ...). Must be a value Fargate accepts for the given memory."
}

variable "memory" {
  type        = number
  description = "Fargate task memory in MB (must pair validly with `cpu` per Fargate's supported combinations)."
}

variable "container_port" {
  type        = number
  description = "Port the container listens on; used for the container's port mapping and, when target_group_arn is set, the ALB target group registration."
}

variable "environment" {
  type        = map(string)
  description = "Plain (non-sensitive) environment variables passed to the container."
  default     = {}
}

variable "environment_sensitive" {
  type        = map(string)
  description = <<-EOT
    Sensitive environment variables (e.g. API_KEYS) passed to the container.
    Marked sensitive so Terraform redacts them from plan/apply CLI output;
    this does NOT encrypt them at rest — they still land in the task
    definition (visible via the ECS console/API) and in Terraform state
    in plaintext. That's the documented scope decision for this project
    (PLAN.md: API keys live in config/env, no secrets manager, no DB) — a
    production-grade follow-up would move this to AWS Secrets Manager /
    SSM Parameter Store and reference it via the task definition's
    `secrets` block instead of `environment`.
  EOT
  default     = {}
  sensitive   = true
}

variable "container_health_check" {
  type = object({
    command      = list(string)
    interval     = optional(number, 30)
    timeout      = optional(number, 3)
    retries      = optional(number, 3)
    start_period = optional(number, 5)
  })
  description = <<-EOT
    Container-level ECS health check. Pass null to skip it (e.g. the gateway
    task: gcr.io/distroless/static-debian12:nonroot has no shell, so no
    exec-form health-check command can run inside the container — the
    gateway relies solely on the ALB target-group health check against
    GET /v1/health instead). Inference services use python's stdlib
    (python:3.12-slim has no curl) mirroring the HEALTHCHECK already baked
    into their Dockerfiles exactly (interval=30s/timeout=3s/retries=3/
    start_period=5s are this object's defaults for that reason), e.g.:
    ["CMD", "python", "-c", "import urllib.request; urllib.request.urlopen('http://localhost:8001/health', timeout=2)"]
  EOT
  default     = null
}

variable "capacity_provider" {
  type        = string
  description = <<-EOT
    Fargate capacity provider for this service: "FARGATE" (on-demand) or
    "FARGATE_SPOT" (~70% cheaper, subject to 2-minute-notice interruption).
    The dev environment defaults to FARGATE_SPOT (see dev.auto.tfvars) — an
    acceptable trade for this workload because the gateway's circuit
    breaker already treats a downstream going away as a normal failure mode
    (opens the circuit, degrades to 202 pending) and ECS autoscaling +
    the deployment circuit breaker replace an interrupted task
    automatically. Switch to "FARGATE" for anything latency/availability
    sensitive enough that a Spot reclaim is unacceptable.
  EOT

  validation {
    condition     = contains(["FARGATE", "FARGATE_SPOT"], var.capacity_provider)
    error_message = "capacity_provider must be either \"FARGATE\" or \"FARGATE_SPOT\"."
  }
}

variable "min_capacity" {
  type        = number
  description = "Minimum number of tasks (also the initial desired_count; autoscaling owns desired_count after that)."
  default     = 1
}

variable "max_capacity" {
  type        = number
  description = "Maximum number of tasks the CPU-target autoscaling policy may scale out to."
  default     = 3
}

variable "cpu_target_value" {
  type        = number
  description = "Target average CPU utilization (%) for the autoscaling target-tracking policy."
  default     = 70
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnet IDs the task's ENI is placed in."
}

variable "security_group_id" {
  type        = string
  description = "Security group ID attached to the task's ENI."
}

variable "target_group_arn" {
  type        = string
  description = "ALB target group ARN to register this service with. Pass null for services that aren't reachable from the ALB (the inference services — only the gateway is)."
  default     = null
}

variable "enable_service_discovery" {
  type        = bool
  description = "Register this service in Cloud Map so it's reachable at \"<name>.<namespace>\" from inside the VPC (used by the inference services; the gateway doesn't need to be discovered since the ALB is its only caller)."
  default     = false
}

variable "service_discovery_namespace_id" {
  type        = string
  description = "Cloud Map private DNS namespace ID (e.g. for \"vigil.local\"). Required when enable_service_discovery is true."
  default     = null

  validation {
    condition     = !var.enable_service_discovery || var.service_discovery_namespace_id != null
    error_message = "service_discovery_namespace_id is required when enable_service_discovery = true."
  }
}

variable "log_retention_days" {
  type        = number
  description = "CloudWatch Logs retention, in days, for this service's log group."
  default     = 7
}

variable "tags" {
  type        = map(string)
  description = "Common tags applied to every resource this module creates."
  default     = {}
}
