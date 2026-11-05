output "role_policy_arns" {
  description = "Deny policies to attach to every role the project creates (instance and Lambda roles), keyed by a static name (cloudtrail, regions, s3_public) so that attachments can use for_each before the policies exist."
  value       = local.role_policy_arns
}

output "all_policy_arns" {
  description = "Every guardrail policy by static key, including protected_deletion when enabled."
  value       = local.all_policy_arns
}

output "operators_group_name" {
  description = "IAM group for human operators (empty when not created)."
  value       = var.create_operators_group ? aws_iam_group.operators[0].name : ""
}
