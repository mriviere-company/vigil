terraform {
  required_version = ">= 1.11.0" # use_lockfile backend needs 1.11+

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project     = "vigil-api"
      Environment = "guardrails"
      ManagedBy   = "terraform"
    }
  }
}
