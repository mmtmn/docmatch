output "name_servers" {
  description = "NS records to configure at the registrar for the domain."
  value       = module.edge.name_servers
}

output "dnssec_ds_record" {
  description = "DS record to publish at the registrar (after the NS delegation works)."
  value       = module.edge.dnssec_ds_record
}

output "cloudfront_distribution_id" {
  description = "Distribution id (deploy-site.sh invalidates it)."
  value       = module.edge.distribution_id
}

output "cloudfront_domain_name" {
  description = "Distribution domain name."
  value       = module.edge.distribution_domain_name
}

output "key_value_store_arn" {
  description = "Where the owner writes origin_secret (see infra/README.md)."
  value       = module.edge.key_value_store_arn
}

output "site_bucket_name" {
  description = "Static site bucket (deploy-site.sh syncs apps/web/dist here)."
  value       = module.storage.bucket_names["site"]
}

output "bucket_names" {
  description = "Every bucket of the environment."
  value       = module.storage.bucket_names
}

output "ssm_path_prefix" {
  description = "SSM path prefix of the environment."
  value       = module.secrets.path_prefix
}

output "ssm_secret_parameters" {
  description = "Secret parameters the owner must fill in before the services start."
  value       = module.secrets.secret_names
}

output "ecr_repository_urls" {
  description = "ECR repositories for the sandbox images."
  value       = module.sandbox.ecr_repository_urls
}

output "elastic_ip" {
  description = "Public IPv4 of the application instance."
  value       = module.compute.elastic_ip
}

output "origin_fqdn" {
  description = "Origin hostname used by CloudFront."
  value       = module.compute.origin_fqdn
}

output "app_subnet_id" {
  description = "Subnet for the Packer build instance (PKR_VAR_subnet_id)."
  value       = module.network.app_subnet_id
}

output "app_security_group_id" {
  description = "Security group of the application instance."
  value       = module.network.app_security_group_id
}

output "alerts_topic_arn" {
  description = "SNS topic for alarms; confirm the e-mail subscription."
  value       = module.observability.alerts_topic_arn
}

output "budget_limit_usd" {
  description = "Monthly budget limit."
  value       = module.observability.budget_limit_usd
}

output "operators_group_name" {
  description = "IAM group human operators must join."
  value       = module.iam_guardrails.operators_group_name
}
