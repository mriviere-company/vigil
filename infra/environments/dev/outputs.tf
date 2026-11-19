output "alb_url" {
  description = "Base URL of the ALB — the acceptance target: curl this after apply (e.g. curl -H \"X-API-Key: dev-free\" <this>/v1/health)."
  value       = "http://${module.alb.alb_dns_name}"
}

output "ecr_repository_urls" {
  description = "Map of service name -> ECR repository URL. Use with `aws ecr get-login-password` + `docker push` before the first apply (see infra/README.md)."
  value       = module.ecr.repository_urls
}

output "cluster_name" {
  description = "ECS cluster name."
  value       = aws_ecs_cluster.this.name
}
