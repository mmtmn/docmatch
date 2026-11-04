output "media_bucket_name" {
  description = "Name of the R2 media bucket (DM_R2_MEDIA_BUCKET)."
  value       = cloudflare_r2_bucket.media.name
}

output "s3_endpoint_host" {
  description = "Host of the S3-compatible endpoint for this account (the API prefixes https://)."
  value       = "${var.cloudflare_account_id}.r2.cloudflarestorage.com"
}
