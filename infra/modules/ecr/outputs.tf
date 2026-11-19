output "repository_urls" {
  description = "Map of repository name -> full repository URL (use to build \"<url>:<tag>\" image references for the ecs-service module calls)."
  value       = { for name, repo in aws_ecr_repository.this : name => repo.repository_url }
}

output "repository_arns" {
  description = "Map of repository name -> repository ARN."
  value       = { for name, repo in aws_ecr_repository.this : name => repo.arn }
}
