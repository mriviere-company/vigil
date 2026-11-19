output "alb_dns_name" {
  description = "Public DNS name of the ALB (the acceptance curl target: http://<this>)."
  value       = aws_lb.this.dns_name
}

output "alb_arn" {
  description = "ARN of the ALB."
  value       = aws_lb.this.arn
}

output "listener_arn" {
  description = "ARN of the HTTP listener."
  value       = aws_lb_listener.http.arn
}

output "target_group_arn" {
  description = "ARN of the gateway target group — pass this into the gateway ecs-service module call's target_group_arn variable."
  value       = aws_lb_target_group.gateway.arn
}
