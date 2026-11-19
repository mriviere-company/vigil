variable "name_prefix" {
  type        = string
  description = "Prefix applied to every resource name/tag created by this module (e.g. \"vigil-dev\")."
}

variable "vpc_id" {
  type        = string
  description = "VPC ID the ALB and its target group live in."
}

variable "public_subnet_ids" {
  type        = list(string)
  description = "Public subnet IDs (one per AZ) the ALB is deployed into."
}

variable "alb_security_group_id" {
  type        = string
  description = "Security group ID attached to the ALB (from the network module)."
}

variable "target_port" {
  type        = number
  description = "Port the gateway container listens on; the target group forwards to this port."
  default     = 8080
}

variable "health_check_path" {
  type        = string
  description = "Path the target group health check requests (matches the gateway's GET /v1/health endpoint)."
  default     = "/v1/health"
}

variable "alarm_response_time_threshold_seconds" {
  type        = number
  description = "p95 TargetResponseTime threshold (seconds) above which the CloudWatch alarm fires. 0.2 = 200ms, the p95 latency target from PLAN.md §1/§3."
  default     = 0.2
}

variable "tags" {
  type        = map(string)
  description = "Common tags applied to every resource this module creates."
  default     = {}
}
