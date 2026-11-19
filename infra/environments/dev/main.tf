# Dev environment: composes the four modules into the three-service
# vigil stack (PLAN.md §2) — ALB -> gateway -> {inference-text,
# inference-image}, all on ECS Fargate in private subnets behind a single
# NAT gateway. This file is the only place service-specific differences
# (image, size, port, env, health check, discovery) show up; the
# ecs-service module itself is identical across all three calls.

locals {
  tags = {
    Project     = "vigil-api"
    Environment = "dev"
    ManagedBy   = "terraform"
  }

  # Fargate sizing (PLAN.md §7 cost guardrails) — fixed decisions, not
  # meant to be tuned per apply, so plain locals rather than variables.
  gateway_cpu    = 256 # 0.25 vCPU
  gateway_memory = 512 # MB

  inference_cpu    = 512  # 0.5 vCPU
  inference_memory = 1024 # 1 GB

  service_discovery_namespace = "vigil.local"

  # Cloud Map FQDNs the gateway calls — must match gateway/internal/config's
  # TEXT_SERVICE_URL / IMAGE_SERVICE_URL env vars exactly, including the
  # inference containers' listening ports (8001/8002, matching their
  # Dockerfiles' EXPOSE and `uvicorn --port` args).
  text_service_url  = "http://text.${local.service_discovery_namespace}:8001"
  image_service_url = "http://image.${local.service_discovery_namespace}:8002"

  ecr_repo_names = {
    gateway = "vigil-gateway"
    text    = "vigil-text"
    image   = "vigil-image"
  }
}

module "network" {
  source = "../../modules/network"

  name_prefix          = var.name_prefix
  vpc_cidr             = var.vpc_cidr
  public_subnet_cidrs  = var.public_subnet_cidrs
  private_subnet_cidrs = var.private_subnet_cidrs
  tags                 = local.tags
}

module "ecr" {
  source = "../../modules/ecr"

  repository_names = toset(values(local.ecr_repo_names))
  tags             = local.tags
}

module "alb" {
  source = "../../modules/alb"

  name_prefix           = var.name_prefix
  vpc_id                = module.network.vpc_id
  public_subnet_ids     = module.network.public_subnet_ids
  alb_security_group_id = module.network.alb_security_group_id
  target_port           = 8080
  health_check_path     = "/v1/health"

  tags = local.tags
}

# ECS cluster with both capacity providers attached — the FARGATE/
# FARGATE_SPOT choice (var.capacity_provider) is made per-service in each
# ecs-service module call below, not at the cluster level; this default
# strategy only matters as a fallback for a service that doesn't specify
# its own (none do here).
resource "aws_ecs_cluster" "this" {
  name = "${var.name_prefix}-cluster"

  setting {
    name  = "containerInsights"
    value = "disabled" # extra CloudWatch cost, not needed for a dev/portfolio environment
  }

  tags = local.tags
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name = aws_ecs_cluster.this.name

  capacity_providers = ["FARGATE", "FARGATE_SPOT"]

  default_capacity_provider_strategy {
    capacity_provider = var.capacity_provider
    weight            = 100
    base              = 1
  }
}

# Cloud Map namespace the two inference services register into; the
# gateway reaches them at "text.vigil.local"/"image.vigil.local"
# (local.text_service_url / local.image_service_url above) instead of an
# ALB — inference is never reachable from outside the VPC (PLAN.md §2).
resource "aws_service_discovery_private_dns_namespace" "this" {
  name        = local.service_discovery_namespace
  description = "Cloud Map namespace for gateway -> inference service discovery."
  vpc         = module.network.vpc_id

  tags = local.tags
}

# --- Gateway ----------------------------------------------------------
#
# Runtime is gcr.io/distroless/static-debian12:nonroot (gateway/Dockerfile)
# — no shell, so no exec-form container health check is possible.
# container_health_check = null means health is checked solely by the ALB
# target group against GET /v1/health (module.alb's target group).
module "gateway" {
  source = "../../modules/ecs-service"

  name           = "gateway"
  cluster_name   = aws_ecs_cluster.this.name
  image          = "${module.ecr.repository_urls[local.ecr_repo_names.gateway]}:${var.image_tag}"
  cpu            = local.gateway_cpu
  memory         = local.gateway_memory
  container_port = 8080

  environment = {
    TEXT_SERVICE_URL  = local.text_service_url
    IMAGE_SERVICE_URL = local.image_service_url
  }
  environment_sensitive = {
    API_KEYS = var.api_keys
  }

  container_health_check = null

  capacity_provider = var.capacity_provider
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = module.network.gateway_security_group_id
  target_group_arn  = module.alb.target_group_arn

  tags = local.tags

  # capacity_provider_strategy on the ECS service requires the cluster's
  # capacity providers to already be associated — cluster_name alone
  # doesn't create an implicit dependency on that association.
  depends_on = [aws_ecs_cluster_capacity_providers.this]
}

# --- Inference: text ----------------------------------------------------
#
# MODEL_BACKEND=stub is the dev default and, in practice, the only mode
# this Fargate environment exercises: real-model serving
# (MODEL_BACKEND=onnx) needs MODEL_PATH/TOKENIZER_PATH resolvable at
# container startup, which on Fargate means either baking ~100MB+ of ONNX
# weights into the image (blows the <800MB budget and the layer cache) or
# fetching them from S3 at boot (needs its own task IAM role + a startup
# latency budget). Both are out of scope for this dev environment — see
# the ecs-service module's comment on why no task role exists yet.
module "inference_text" {
  source = "../../modules/ecs-service"

  name           = "text"
  cluster_name   = aws_ecs_cluster.this.name
  image          = "${module.ecr.repository_urls[local.ecr_repo_names.text]}:${var.image_tag}"
  cpu            = local.inference_cpu
  memory         = local.inference_memory
  container_port = 8001

  environment = {
    MODEL_BACKEND = var.model_backend
  }

  # Mirrors the HEALTHCHECK already baked into inference/text/Dockerfile
  # (python:3.12-slim has no curl, so python's stdlib urllib is used
  # instead) — exec form, matching the Dockerfile exactly.
  container_health_check = {
    command = [
      "CMD", "python", "-c",
      "import urllib.request; urllib.request.urlopen('http://localhost:8001/health', timeout=2)"
    ]
  }

  capacity_provider = var.capacity_provider
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = module.network.inference_security_group_id

  enable_service_discovery       = true
  service_discovery_namespace_id = aws_service_discovery_private_dns_namespace.this.id

  tags = local.tags

  depends_on = [aws_ecs_cluster_capacity_providers.this]
}

# --- Inference: image -----------------------------------------------------
# Same MODEL_BACKEND=stub scope decision as inference-text above.
module "inference_image" {
  source = "../../modules/ecs-service"

  name           = "image"
  cluster_name   = aws_ecs_cluster.this.name
  image          = "${module.ecr.repository_urls[local.ecr_repo_names.image]}:${var.image_tag}"
  cpu            = local.inference_cpu
  memory         = local.inference_memory
  container_port = 8002

  environment = {
    MODEL_BACKEND = var.model_backend
  }

  # Mirrors the HEALTHCHECK baked into inference/image/Dockerfile.
  container_health_check = {
    command = [
      "CMD", "python", "-c",
      "import urllib.request; urllib.request.urlopen('http://localhost:8002/health', timeout=2)"
    ]
  }

  capacity_provider = var.capacity_provider
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = module.network.inference_security_group_id

  enable_service_discovery       = true
  service_discovery_namespace_id = aws_service_discovery_private_dns_namespace.this.id

  tags = local.tags

  depends_on = [aws_ecs_cluster_capacity_providers.this]
}
