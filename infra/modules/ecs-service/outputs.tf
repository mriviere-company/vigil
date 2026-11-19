output "service_name" {
  description = "Name of the ECS service."
  value       = aws_ecs_service.this.name
}

output "task_definition_arn" {
  description = "ARN of the (latest) task definition revision."
  value       = aws_ecs_task_definition.this.arn
}

output "log_group_name" {
  description = "CloudWatch Logs group name for this service."
  value       = aws_cloudwatch_log_group.this.name
}

output "execution_role_arn" {
  description = "ARN of the task execution IAM role."
  value       = aws_iam_role.execution.arn
}

output "service_discovery_arn" {
  description = "ARN of the Cloud Map service, if enable_service_discovery is true (null otherwise)."
  value       = try(aws_service_discovery_service.this[0].arn, null)
}
