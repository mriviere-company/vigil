variable "name_prefix" {
  type        = string
  description = "Prefix applied to every resource name/tag created by this module (e.g. \"vigil-dev\")."
}

variable "vpc_cidr" {
  type        = string
  description = "CIDR block for the VPC."
  default     = "10.0.0.0/16"
}

variable "public_subnet_cidrs" {
  type        = list(string)
  description = "CIDR blocks for the public subnets (one per AZ, ALB + NAT gateway live here). Must be the same length as private_subnet_cidrs."
  default     = ["10.0.0.0/24", "10.0.1.0/24"]
}

variable "private_subnet_cidrs" {
  type        = list(string)
  description = "CIDR blocks for the private subnets (one per AZ, Fargate tasks live here). Must be the same length as public_subnet_cidrs."
  default     = ["10.0.10.0/24", "10.0.11.0/24"]

  validation {
    # The subnet count (local.az_count in main.tf) is derived from
    # public_subnet_cidrs alone; if the two lists silently disagreed in
    # length, the private list would just get truncated/short-indexed
    # instead of erroring, so this cross-check catches that at plan time.
    condition     = length(var.private_subnet_cidrs) == length(var.public_subnet_cidrs)
    error_message = "private_subnet_cidrs and public_subnet_cidrs must have the same length (one pair of CIDRs per AZ)."
  }
}

variable "alb_ingress_port" {
  type        = number
  description = "Port the ALB security group accepts inbound traffic on. HTTP only in this scope (no ACM/domain) — see infra/README.md limitations."
  default     = 80
}

variable "gateway_port" {
  type        = number
  description = "Port the gateway container listens on; the gateway security group only accepts inbound traffic on this port, and only from the ALB security group."
  default     = 8080
}

variable "inference_ports" {
  type        = list(number)
  description = "Ports the inference containers listen on (text, image); the inference security group only accepts inbound traffic on these ports, and only from the gateway security group."
  default     = [8001, 8002]
}

variable "tags" {
  type        = map(string)
  description = "Common tags applied to every resource this module creates."
  default     = {}
}
