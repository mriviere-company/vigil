variable "aws_region" {
  type        = string
  description = "AWS region every resource in this environment is created in."
  default     = "ca-central-1"
}

variable "name_prefix" {
  type        = string
  description = "Prefix applied to resource names/tags across this environment."
  default     = "vigil-dev"
}

variable "vpc_cidr" {
  type        = string
  description = "CIDR block for the VPC."
  default     = "10.0.0.0/16"
}

variable "public_subnet_cidrs" {
  type        = list(string)
  description = "CIDR blocks for the two public (ALB + NAT gateway) subnets, one per AZ."
  default     = ["10.0.0.0/24", "10.0.1.0/24"]
}

variable "private_subnet_cidrs" {
  type        = list(string)
  description = "CIDR blocks for the two private (Fargate task) subnets, one per AZ."
  default     = ["10.0.10.0/24", "10.0.11.0/24"]
}

variable "capacity_provider" {
  type        = string
  description = <<-EOT
    Fargate capacity provider used by all three services: "FARGATE"
    (on-demand) or "FARGATE_SPOT" (~70% cheaper, subject to 2-minute-notice
    interruption). See the ecs-service module's capacity_provider
    docstring for the full cost/interruption trade-off. This variable
    defaults to the safe "FARGATE" choice; dev.auto.tfvars is the place
    that actually opts this environment into FARGATE_SPOT, with the
    reasoning documented right there.
  EOT
  default     = "FARGATE"

  validation {
    condition     = contains(["FARGATE", "FARGATE_SPOT"], var.capacity_provider)
    error_message = "capacity_provider must be either \"FARGATE\" or \"FARGATE_SPOT\"."
  }
}

variable "image_tag" {
  type        = string
  description = "Image tag to deploy for all three services (e.g. \"latest\" for a manual push, a commit SHA from the CI deploy workflow)."
  default     = "latest"
}

variable "model_backend" {
  type        = string
  description = <<-EOT
    MODEL_BACKEND for both inference services (inference/common's
    CommonSettings.model_backend, Literal["stub", "onnx"]). "stub" needs no
    model artifacts at all and is the only mode this dev environment
    exercises on Fargate — see the head comment above the inference
    ecs-service module calls in main.tf for why "onnx" is out of scope here.
  EOT
  default     = "stub"

  validation {
    condition     = contains(["stub", "onnx"], var.model_backend)
    error_message = "model_backend must be either \"stub\" or \"onnx\" (matches inference/common/vigil_common/settings.py's Literal type)."
  }
}

variable "api_keys" {
  type        = string
  description = <<-EOT
    Raw API_KEYS value for the gateway, "key1:tier1,key2:tier2" form
    (gateway/internal/config.Config.APIKeysRaw). No default on purpose —
    it must come from dev.auto.tfvars (dev placeholder keys only) or
    -var/TF_VAR_api_keys for anything beyond this dev environment. Marked
    sensitive so it's redacted from plan/apply CLI output; it is NOT
    redacted from Terraform state or the ECS task definition itself — see
    the ecs-service module's environment_sensitive variable docstring for
    why that's the documented scope decision here (no Secrets Manager, no
    DB, per PLAN.md).
  EOT
  sensitive   = true
}
