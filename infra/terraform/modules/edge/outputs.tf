output "hosted_zone_id" {
  description = "Id of the Route 53 zone."
  value       = aws_route53_zone.this.zone_id
}

output "hosted_zone_arn" {
  description = "ARN of the Route 53 zone."
  value       = aws_route53_zone.this.arn
}

output "name_servers" {
  description = "NS records to configure at the registrar."
  value       = aws_route53_zone.this.name_servers
}

output "dnssec_ds_record" {
  description = "DS record to publish at the registrar (empty when DNSSEC is disabled)."
  value       = var.dnssec_enabled ? aws_route53_key_signing_key.this[0].ds_record : ""
}

output "dnssec_kms_key_arn" {
  description = "ARN of the DNSSEC KMS key (protected by the guardrails), empty when disabled."
  value       = var.dnssec_enabled ? aws_kms_key.dnssec[0].arn : ""
}

output "distribution_id" {
  description = "Id of the CloudFront distribution (invalidations)."
  value       = aws_cloudfront_distribution.this.id
}

output "distribution_arn" {
  description = "ARN of the CloudFront distribution."
  value       = aws_cloudfront_distribution.this.arn
}

output "distribution_domain_name" {
  description = "Domain name of the distribution."
  value       = aws_cloudfront_distribution.this.domain_name
}

output "certificate_arn" {
  description = "ARN of the ACM certificate."
  value       = aws_acm_certificate.this.arn
}

output "key_value_store_arn" {
  description = "ARN of the key-value store where the owner writes origin_secret."
  value       = aws_cloudfront_key_value_store.edge.arn
}

output "function_arn" {
  description = "ARN of the viewer-request function."
  value       = aws_cloudfront_function.viewer_request.arn
}

output "response_headers_policy_id" {
  description = "Id of the security headers policy."
  value       = aws_cloudfront_response_headers_policy.this.id
}

output "origin_secret_header_name" {
  description = "Header name Caddy must require (DM_ORIGIN_SECRET_HEADER)."
  value       = var.origin_secret_header_name
}
