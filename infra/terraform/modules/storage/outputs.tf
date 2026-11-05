output "bucket_names" {
  description = "Map of bucket key (uploads, backups, site, signatures, releases, logs) to bucket name."
  value       = { for k, b in aws_s3_bucket.this : k => b.bucket }
}

output "bucket_arns" {
  description = "Map of bucket key to bucket ARN."
  value       = { for k, b in aws_s3_bucket.this : k => b.arn }
}

output "bucket_regional_domain_names" {
  description = "Map of bucket key to regional domain name (CloudFront origins)."
  value       = { for k, b in aws_s3_bucket.this : k => b.bucket_regional_domain_name }
}

# Taken from the policy resource on purpose: anything that consumes the logs bucket (CloudTrail,
# log deliveries, flow logs) must be created after its bucket policy exists.
output "logs_bucket_name" {
  description = "Name of the logs bucket, resolved after its bucket policy is in place."
  value       = aws_s3_bucket_policy.logs.bucket
}

output "logs_bucket_arn" {
  description = "ARN of the logs bucket, resolved after its bucket policy is in place."
  value       = "arn:aws:s3:::${aws_s3_bucket_policy.logs.bucket}"
}

output "uploads_origin" {
  description = "Origin the browser posts uploads to (virtual-hosted style), for the site's CSP connect-src."
  value       = "https://${aws_s3_bucket.this["uploads"].bucket_regional_domain_name}"
}
