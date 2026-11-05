# Cloudflare R2: media bucket (audio and video uploads, zero egress fees). The S3-compatible
# credentials used by the API are created by the owner in the Cloudflare dashboard and stored in
# SSM (R2_ACCESS_KEY_ID / R2_SECRET_ACCESS_KEY); the provider token only manages buckets.

locals {
  media_bucket = "${var.name}-media"
  retention_s  = var.media_retention_days * 86400
}

resource "cloudflare_r2_bucket" "media" {
  account_id    = var.cloudflare_account_id
  name          = local.media_bucket
  location      = var.location
  storage_class = "Standard"
  jurisdiction  = "default"
}

resource "cloudflare_r2_bucket_lifecycle" "media" {
  account_id  = var.cloudflare_account_id
  bucket_name = cloudflare_r2_bucket.media.name

  rules = [
    {
      id      = "abort-stale-multipart-uploads"
      enabled = true
      conditions = {
        prefix = ""
      }
      abort_multipart_uploads_transition = {
        condition = {
          max_age = 86400
          type    = "Age"
        }
      }
    },
    {
      id      = "expire-media"
      enabled = true
      conditions = {
        prefix = ""
      }
      delete_objects_transition = {
        condition = {
          max_age = local.retention_s
          type    = "Age"
        }
      }
    },
  ]
}

# Browser uploads go straight to R2 through presigned URLs issued by the API.
resource "cloudflare_r2_bucket_cors" "media" {
  account_id  = var.cloudflare_account_id
  bucket_name = cloudflare_r2_bucket.media.name

  rules = [
    {
      id = "web-app-direct-uploads"
      allowed = {
        methods = ["GET", "HEAD", "PUT"]
        origins = [for host in [var.domain, "www.${var.domain}"] : "https://${host}"]
        headers = ["content-type", "content-length", "content-md5", "x-amz-checksum-sha256", "x-amz-content-sha256", "x-amz-date"]
      }
      expose_headers  = ["etag"]
      max_age_seconds = 3600
    },
  ]
}
