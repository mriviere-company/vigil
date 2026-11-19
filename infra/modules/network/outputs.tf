output "vpc_id" {
  description = "ID of the VPC."
  value       = aws_vpc.this.id
}

output "public_subnet_ids" {
  description = "IDs of the public subnets (ALB, NAT gateway)."
  value       = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  description = "IDs of the private subnets (Fargate tasks)."
  value       = aws_subnet.private[*].id
}

output "availability_zones" {
  description = "Availability zones used, in the same order as the subnet ID lists."
  value       = local.azs
}

output "alb_security_group_id" {
  description = "Security group ID for the ALB."
  value       = aws_security_group.alb.id
}

output "gateway_security_group_id" {
  description = "Security group ID for the gateway Fargate service."
  value       = aws_security_group.gateway.id
}

output "inference_security_group_id" {
  description = "Security group ID shared by the two inference Fargate services."
  value       = aws_security_group.inference.id
}

output "nat_gateway_id" {
  description = "ID of the single NAT gateway."
  value       = aws_nat_gateway.this.id
}
