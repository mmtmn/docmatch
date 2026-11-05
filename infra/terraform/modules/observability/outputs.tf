output "alerts_topic_arn" {
  description = "ARN of the SNS topic that receives alarms and budget notifications."
  value       = aws_sns_topic.alerts.arn
}

output "cloudtrail_arn" {
  description = "ARN of the trail (protected by the guardrails)."
  value       = aws_cloudtrail.this.arn
}

output "budget_name" {
  description = "Name of the monthly budget."
  value       = aws_budgets_budget.monthly.name
}

output "budget_limit_usd" {
  description = "Monthly budget limit computed from the floor and the MRR."
  value       = local.budget_limit
}

output "app_log_group_arn" {
  description = "ARN of the application log group."
  value       = aws_cloudwatch_log_group.app.arn
}

output "app_log_group_name" {
  description = "Name of the application log group."
  value       = aws_cloudwatch_log_group.app.name
}
