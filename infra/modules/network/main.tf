# Network module: VPC, 2-AZ public/private subnets, a single NAT gateway,
# route tables, and the three security groups that enforce the
# ALB -> gateway -> inference call chain (PLAN.md §2/§7).
#
# Cost decision — single NAT gateway (not one per AZ): ~$32/month for one
# NAT gateway (plus data processing) vs ~$64/month for two. The trade-off is
# an availability one, not a functional one: if the AZ hosting the NAT
# gateway has an outage, tasks in the *other* AZ lose egress (ECR pulls,
# CloudWatch Logs, Cloud Map DNS) until it recovers. That's an acceptable
# trade for a dev/portfolio environment that's destroyed at the end of every
# session (see infra/README.md); a one-per-AZ topology is listed as a
# documented future option in the README cost table, not implemented here.
data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  az_count = length(var.public_subnet_cidrs)
  azs      = slice(data.aws_availability_zones.available.names, 0, local.az_count)
}

resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-vpc"
  })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-igw"
  })
}

# --- Subnets -----------------------------------------------------------

resource "aws_subnet" "public" {
  count = local.az_count

  vpc_id                  = aws_vpc.this.id
  cidr_block              = var.public_subnet_cidrs[count.index]
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = true

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-public-${local.azs[count.index]}"
    Tier = "public"
  })
}

resource "aws_subnet" "private" {
  count = local.az_count

  vpc_id            = aws_vpc.this.id
  cidr_block        = var.private_subnet_cidrs[count.index]
  availability_zone = local.azs[count.index]

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-private-${local.azs[count.index]}"
    Tier = "private"
  })
}

# --- NAT gateway (single, see cost comment above) -----------------------

resource "aws_eip" "nat" {
  domain = "vpc"

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-nat-eip"
  })

  depends_on = [aws_internet_gateway.this]
}

resource "aws_nat_gateway" "this" {
  allocation_id = aws_eip.nat.id
  # Placed in the first public subnet — the only NAT gateway in the
  # topology, shared by every private subnet's route table.
  subnet_id = aws_subnet.public[0].id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-nat"
  })

  depends_on = [aws_internet_gateway.this]
}

# --- Route tables ---------------------------------------------------------

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-public-rt"
  })
}

resource "aws_route_table_association" "public" {
  count = local.az_count

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# Single shared private route table: with only one NAT gateway, per-AZ
# private route tables would all point at the same target anyway.
resource "aws_route_table" "private" {
  vpc_id = aws_vpc.this.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.this.id
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-private-rt"
  })
}

resource "aws_route_table_association" "private" {
  count = local.az_count

  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

# --- Security groups ------------------------------------------------------
#
# Ingress is scoped exactly to the call chain client -> ALB -> gateway ->
# inference (PLAN.md §2). Egress is scoped per hop to what each task
# actually initiates (every rule below carries its purpose):
#
#   - tcp/443 to 0.0.0.0/0: Fargate tasks pull their image from ECR and
#     ship logs to CloudWatch through the task ENI (so through this SG),
#     out via the single NAT gateway. The *destinations* are AWS endpoints,
#     but their IPs aren't stable/enumerable without paying for VPC
#     interface endpoints — so 443 is port-scoped, not IP-scoped.
#   - tcp+udp/53 to the VPC CIDR: Cloud Map service-discovery names
#     (text.vigil.local, ...) resolve via the VPC's Route 53 resolver
#     (VPC base +2). AWS documents that traffic to the Amazon-provided
#     resolver bypasses security-group evaluation, but the rules are kept
#     anyway: they cost nothing, keep working if anyone points resolution
#     at a resolver endpoint that *is* SG-evaluated, and make the DNS
#     dependency visible in the topology instead of implicit.
#   - gateway only: tcp/8001+8002 to the inference SG (SG-referenced, not
#     CIDR) — the calls the gateway exists to make.
#
# The ALB's egress is scoped even tighter (only to the gateway SG on its
# port) since its one and only job is forwarding to the gateway target
# group.

resource "aws_security_group" "alb" {
  name_prefix = "${var.name_prefix}-alb-"
  description = "ALB: public HTTP ingress, egress restricted to the gateway service."
  vpc_id      = aws_vpc.this.id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-alb-sg"
  })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_security_group_rule" "alb_ingress_http" {
  type              = "ingress"
  security_group_id = aws_security_group.alb.id
  from_port         = var.alb_ingress_port
  to_port           = var.alb_ingress_port
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"]
  # HTTP only — no ACM certificate/domain in scope for this dev environment;
  # see infra/README.md "Limitations".
  description = "Public HTTP ingress (no domain/ACM in scope)"
}

resource "aws_security_group_rule" "alb_egress_to_gateway" {
  type                     = "egress"
  security_group_id        = aws_security_group.alb.id
  from_port                = var.gateway_port
  to_port                  = var.gateway_port
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.gateway.id
  description              = "ALB only ever forwards to the gateway target group"
}

resource "aws_security_group" "gateway" {
  name_prefix = "${var.name_prefix}-gateway-"
  description = "Gateway Fargate tasks: ingress only from the ALB; egress scoped to 443 (ECR/CloudWatch), DNS, and the inference ports."
  vpc_id      = aws_vpc.this.id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-gateway-sg"
  })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_security_group_rule" "gateway_ingress_from_alb" {
  type                     = "ingress"
  security_group_id        = aws_security_group.gateway.id
  from_port                = var.gateway_port
  to_port                  = var.gateway_port
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.alb.id
  description              = "Only the ALB may reach the gateway container port"
}

# Trivy's AWS-0104 keys on the 0.0.0.0/0 destination alone (verified on
# trivy 0.72.0), so it flags even this single-port tcp/443 rule. The
# check's substance — unrestricted all-protocol egress — is what the rules
# in this file deliberately do NOT have; 443's *destination* can't be
# narrowed further without paying for VPC interface endpoints (ECR, S3,
# CloudWatch Logs — out of scope, see infra/README.md). Suppressed inline,
# next to the rule it excuses, with this justification.
#trivy:ignore:AVD-AWS-0104
resource "aws_security_group_rule" "gateway_egress_https" {
  type              = "egress"
  security_group_id = aws_security_group.gateway.id
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "HTTPS out via NAT: ECR image pull + CloudWatch Logs delivery through the task ENI (AWS endpoint IPs are not enumerable without VPC endpoints; port-scoped, not IP-scoped)"
}

resource "aws_security_group_rule" "gateway_egress_to_inference" {
  for_each = toset([for p in var.inference_ports : tostring(p)])

  type                     = "egress"
  security_group_id        = aws_security_group.gateway.id
  from_port                = tonumber(each.value)
  to_port                  = tonumber(each.value)
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.inference.id
  # NB: SG rule descriptions have a restricted charset (no '>', apostrophes,
  # or em dashes) enforced by the provider; keep these plain-ASCII prose.
  description = "Gateway to inference service on port ${each.value} (the proxy calls the gateway exists to make)"
}

resource "aws_security_group_rule" "gateway_egress_dns" {
  for_each = toset(["tcp", "udp"])

  type              = "egress"
  security_group_id = aws_security_group.gateway.id
  from_port         = 53
  to_port           = 53
  protocol          = each.value
  cidr_blocks       = [aws_vpc.this.cidr_block]
  description       = "DNS (${each.value}) to the VPC resolver: Cloud Map lookups of text/image.vigil.local (see SG section header comment)"
}

resource "aws_security_group" "inference" {
  name_prefix = "${var.name_prefix}-inference-"
  description = "Inference Fargate tasks (text + image): ingress only from the gateway; egress scoped to 443 (ECR/CloudWatch) and DNS."
  vpc_id      = aws_vpc.this.id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-inference-sg"
  })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_security_group_rule" "inference_ingress_from_gateway" {
  for_each = toset([for p in var.inference_ports : tostring(p)])

  type                     = "ingress"
  security_group_id        = aws_security_group.inference.id
  from_port                = tonumber(each.value)
  to_port                  = tonumber(each.value)
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.gateway.id
  description              = "Only the gateway may reach inference port ${each.value}"
}

# Same AWS-0104 rationale as aws_security_group_rule.gateway_egress_https
# above: single-port 443, destination not narrowable without VPC endpoints.
#trivy:ignore:AVD-AWS-0104
resource "aws_security_group_rule" "inference_egress_https" {
  type              = "egress"
  security_group_id = aws_security_group.inference.id
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "HTTPS out via NAT: ECR image pull + CloudWatch Logs delivery through the task ENI (port-scoped, not IP-scoped; see SG section header comment)"
}

resource "aws_security_group_rule" "inference_egress_dns" {
  for_each = toset(["tcp", "udp"])

  type              = "egress"
  security_group_id = aws_security_group.inference.id
  from_port         = 53
  to_port           = 53
  protocol          = each.value
  cidr_blocks       = [aws_vpc.this.cidr_block]
  description       = "DNS (${each.value}) to the VPC resolver: ECR/CloudWatch endpoint hostname resolution (see SG section header comment)"
}

# Inference tasks initiate nothing else: no calls back to the gateway, no
# cross-talk between text and image — so no other egress rules exist.
