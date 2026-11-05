output "path_prefix" {
  description = "SSM path prefix read by the render script on the instance (e.g. /docmatch/prod)."
  value       = local.path_prefix
}

output "parameter_arns" {
  description = "ARNs of every parameter (secrets and configuration)."
  value = concat(
    [for p in aws_ssm_parameter.secret : p.arn],
    [for p in aws_ssm_parameter.config : p.arn],
  )
}

output "secret_names" {
  description = "Names of the secret parameters the owner still has to fill in."
  value       = sort([for p in aws_ssm_parameter.secret : p.name])
}
