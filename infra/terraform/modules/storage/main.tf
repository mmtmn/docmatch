# Storage: five private buckets, all with public access blocked, bucket-owner-enforced ownership
# (no ACLs), SSE-S3 and a TLS-only policy. Bucket names embed the account id to be unique.
#
#   uploads     quarantine/ (fresh uploads, scanned by the sandbox) · clean/ · derived/ ·
#               verdicts/ (one JSON per scan, written by the scanner, consumed by the worker)
#   backups     pgBackRest repository (versioned)
#   site        static site served by CloudFront through an origin access control (policy in edge)
#   signatures  ClamAV signature bundles written by the updater Lambda, read by the scanner
#   logs        S3 access logs, CloudTrail, CloudFront standard logs (v2) and VPC flow logs

locals {
  buckets = {
    uploads    = { versioning = "Disabled", access_logs = true }
    backups    = { versioning = "Enabled", access_logs = true }
    site       = { versioning = "Disabled", access_logs = true }
    signatures = { versioning = "Disabled", access_logs = true }
    logs       = { versioning = "Disabled", access_logs = false }
  }
  bucket_names = { for k, v in local.buckets : k => "${var.name}-${k}-${var.account_id}" }
}

resource "aws_s3_bucket" "this" {
  for_each = local.buckets

  bucket        = local.bucket_names[each.key]
  force_destroy = var.force_destroy

  tags = merge(var.tags, { Name = local.bucket_names[each.key], "docmatch:bucket" = each.key })
}

resource "aws_s3_bucket_ownership_controls" "this" {
  for_each = aws_s3_bucket.this

  bucket = each.value.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  for_each = aws_s3_bucket.this

  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "this" {
  for_each = aws_s3_bucket.this

  bucket = each.value.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_versioning" "this" {
  for_each = aws_s3_bucket.this

  bucket = each.value.id

  versioning_configuration {
    status = local.buckets[each.key].versioning
  }
}

# Server access logging into the logs bucket (free; only storage is billed).
resource "aws_s3_bucket_logging" "this" {
  for_each = { for k, v in local.buckets : k => v if v.access_logs }

  bucket        = aws_s3_bucket.this[each.key].id
  target_bucket = aws_s3_bucket.this["logs"].id
  target_prefix = "s3-access/${each.key}/"

  target_object_key_format {
    partitioned_prefix {
      partition_date_source = "EventTime"
    }
  }

  depends_on = [aws_s3_bucket_policy.logs]
}

# ---------------------------------------------------------------------------------- lifecycle
resource "aws_s3_bucket_lifecycle_configuration" "uploads" {
  bucket = aws_s3_bucket.this["uploads"].id

  rule {
    id     = "expire-quarantine"
    status = "Enabled"
    filter {
      prefix = "quarantine/"
    }
    expiration {
      days = var.uploads_expiration_days
    }
  }

  rule {
    id     = "expire-clean"
    status = "Enabled"
    filter {
      prefix = "clean/"
    }
    expiration {
      days = var.uploads_expiration_days
    }
  }

  rule {
    id     = "expire-derived"
    status = "Enabled"
    filter {
      prefix = "derived/"
    }
    expiration {
      days = var.uploads_expiration_days
    }
  }

  # The worker deletes each verdict after acting on it; this catches the ones it never saw.
  rule {
    id     = "expire-verdicts"
    status = "Enabled"
    filter {
      prefix = "verdicts/"
    }
    expiration {
      days = var.verdicts_expiration_days
    }
  }

  rule {
    id     = "abort-multipart"
    status = "Enabled"
    filter {
      prefix = ""
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 2
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.this["backups"].id

  rule {
    id     = "noncurrent-versions"
    status = "Enabled"
    filter {
      prefix = ""
    }
    noncurrent_version_expiration {
      noncurrent_days = var.backups_noncurrent_days
    }
    expiration {
      expired_object_delete_marker = true
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "site" {
  bucket = aws_s3_bucket.this["site"].id

  rule {
    id     = "abort-multipart"
    status = "Enabled"
    filter {
      prefix = ""
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 2
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "signatures" {
  bucket = aws_s3_bucket.this["signatures"].id

  rule {
    id     = "expire-stale-bundles"
    status = "Enabled"
    filter {
      prefix = ""
    }
    expiration {
      days = var.signatures_expiration_days
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "logs" {
  bucket = aws_s3_bucket.this["logs"].id

  rule {
    id     = "expire-logs"
    status = "Enabled"
    filter {
      prefix = ""
    }
    expiration {
      days = var.logs_expiration_days
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

# ----------------------------------------------------------------------------------- policies
# TLS-only for every bucket. The site bucket policy is owned by the edge module (it needs the
# distribution ARN) and the logs bucket adds the log delivery principals below.
data "aws_iam_policy_document" "tls_only" {
  for_each = { for k, v in local.buckets : k => v if k != "site" && k != "logs" }

  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.this[each.key].arn,
      "${aws_s3_bucket.this[each.key].arn}/*",
    ]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "tls_only" {
  for_each = data.aws_iam_policy_document.tls_only

  bucket = aws_s3_bucket.this[each.key].id
  policy = each.value.json

  depends_on = [aws_s3_bucket_public_access_block.this]
}

data "aws_iam_policy_document" "logs" {
  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.this["logs"].arn, "${aws_s3_bucket.this["logs"].arn}/*"]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  # S3 server access logs from the other buckets of this account.
  statement {
    sid    = "S3ServerAccessLogs"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["logging.s3.amazonaws.com"]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.this["logs"].arn}/s3-access/*"]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.account_id]
    }
  }

  # CloudTrail: the trail ARN is deterministic (name is an input), which avoids a cycle with the
  # observability module.
  statement {
    sid    = "CloudTrailAclCheck"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
    actions   = ["s3:GetBucketAcl"]
    resources = [aws_s3_bucket.this["logs"].arn]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceArn"
      values   = ["arn:aws:cloudtrail:${var.region}:${var.account_id}:trail/${var.cloudtrail_name}"]
    }
  }

  statement {
    sid    = "CloudTrailWrite"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.this["logs"].arn}/cloudtrail/AWSLogs/${var.account_id}/*"]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceArn"
      values   = ["arn:aws:cloudtrail:${var.region}:${var.account_id}:trail/${var.cloudtrail_name}"]
    }
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["bucket-owner-full-control"]
    }
  }

  # Vended logs delivery: CloudFront standard logs (v2) and VPC flow logs share this principal.
  statement {
    sid    = "LogDeliveryAclCheck"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    actions   = ["s3:GetBucketAcl", "s3:ListBucket"]
    resources = [aws_s3_bucket.this["logs"].arn]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.account_id]
    }
  }

  statement {
    sid    = "LogDeliveryWrite"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.this["logs"].arn}/*"]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.account_id]
    }
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["bucket-owner-full-control"]
    }
  }
}

resource "aws_s3_bucket_policy" "logs" {
  bucket = aws_s3_bucket.this["logs"].id
  policy = data.aws_iam_policy_document.logs.json

  depends_on = [aws_s3_bucket_public_access_block.this]
}
