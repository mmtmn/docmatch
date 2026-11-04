output "role_policy_arns" {
  description = "Deny policies to attach to every role the project creates (instance and Lambda roles)."
  value = [
    aws_iam_policy.deny_cloudtrail_stop.arn,
    aws_iam_policy.deny_other_regions.arn,
    aws_iam_policy.deny_s3_public.arn,
  ]
}

output "all_policy_arns" {
  description = "Every guardrail policy, including the protected-deletion one when enabled."
  value       = local.all_policy_arns
}

output "operators_group_name" {
  description = "IAM group for human operators (empty when not created)."
  value       = var.create_operators_group ? aws_iam_group.operators[0].name : ""
}
