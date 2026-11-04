output "enabled" {
  description = "Whether the functions exist (an image tag was provided)."
  value       = local.enabled
}

output "function_names" {
  description = "Map of function key to Lambda function name (also when not yet created)."
  value       = local.function_name
}

output "function_arns" {
  description = "Map of function key to ARN; empty until an image tag is provided."
  value       = { for k, f in aws_lambda_function.this : k => f.arn }
}

output "invokable_function_arns" {
  description = "ARNs the application instance may invoke (everything except the scanner and the updater)."
  value       = [for k, f in aws_lambda_function.this : f.arn if contains(["mediarunner", "toolrunner", "fetcher"], k)]
}

output "ecr_repository_urls" {
  description = "Map of function key to ECR repository URL."
  value       = { for k, r in aws_ecr_repository.this : k => r.repository_url }
}

output "verdict_queue_url" {
  description = "URL of the scan verdicts queue (DM_SCAN_VERDICTS_QUEUE)."
  value       = aws_sqs_queue.verdicts.url
}

output "verdict_queue_arn" {
  description = "ARN of the scan verdicts queue."
  value       = aws_sqs_queue.verdicts.arn
}

output "verdict_dlq_name" {
  description = "Name of the verdicts dead-letter queue (for alarms)."
  value       = aws_sqs_queue.verdicts_dlq.name
}

output "lambda_dlq_name" {
  description = "Name of the asynchronous invocation dead-letter queue (for alarms)."
  value       = aws_sqs_queue.lambda_dlq.name
}

output "role_arns" {
  description = "Map of function key to execution role ARN."
  value       = { for k, r in aws_iam_role.fn : k => r.arn }
}
