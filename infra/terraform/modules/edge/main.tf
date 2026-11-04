# Edge: one CloudFront distribution in front of the static site (S3, origin access control) and
# the application (Elastic IP origin over HTTPS), a viewer-request function, a response headers
# policy and the key-value store that holds the origin secret.

# CloudFront certificates and Route 53 DNSSEC keys only exist in us-east-1.
data "aws_region" "current" {}

# Managed policies are looked up by name instead of pasting their ids.
data "aws_cloudfront_cache_policy" "caching_optimized" {
  name = "Managed-CachingOptimized"
}

data "aws_cloudfront_cache_policy" "caching_disabled" {
  name = "Managed-CachingDisabled"
}

data "aws_cloudfront_origin_request_policy" "all_viewer_except_host" {
  name = "Managed-AllViewerExceptHostHeader"
}

data "aws_cloudfront_origin_request_policy" "cors_s3" {
  name = "Managed-CORS-S3Origin"
}

locals {
  site_origin_id = "site-s3"
  app_origin_id  = "app-origin"
}

# ------------------------------------------------------------------------- origin secret
resource "aws_cloudfront_key_value_store" "edge" {
  name    = "${var.name}-edge"
  comment = "docmatch ${var.env}: origin_secret written by the owner with the CLI, never by Terraform"
}

# ----------------------------------------------------------------------- viewer request
resource "aws_cloudfront_function" "viewer_request" {
  name    = "${var.name}-viewer-request"
  runtime = "cloudfront-js-2.0"
  comment = "docmatch ${var.env}: drop scanners, www redirect, directory index, origin secret"
  publish = true

  key_value_store_associations = [aws_cloudfront_key_value_store.edge.arn]

  code = templatefile("${path.module}/templates/viewer-request.js.tftpl", {
    apex           = jsonencode(var.domain)
    secret_header  = jsonencode(var.origin_secret_header_name)
    scanner_regex  = jsonencode(var.scanner_user_agent_regex)
    probe_prefixes = jsonencode([for p in var.probe_path_prefixes : lower(p)])
    probe_suffixes = jsonencode([for s in var.probe_path_suffixes : lower(s)])
    api_prefixes   = jsonencode([for p in var.api_path_patterns : lower(trimsuffix(p, "*"))])
  })
}

# ---------------------------------------------------------------------- response headers
# The CSP here covers static responses and is not overriding: dynamic pages from the API carry
# their own policy with a per-request nonce (a static nonce in a shared header would be a known
# value, i.e. no nonce at all). Everything else overrides whatever the origin sends.
resource "aws_cloudfront_response_headers_policy" "this" {
  name    = "${var.name}-security-headers"
  comment = "docmatch ${var.env}: strict security headers"

  security_headers_config {
    content_security_policy {
      content_security_policy = var.content_security_policy
      override                = false
    }
    content_type_options {
      override = true
    }
    frame_options {
      frame_option = "DENY"
      override     = true
    }
    referrer_policy {
      referrer_policy = "strict-origin-when-cross-origin"
      override        = true
    }
    strict_transport_security {
      access_control_max_age_sec = var.hsts_max_age_seconds
      include_subdomains         = true
      preload                    = true
      override                   = true
    }
    xss_protection {
      protection = false
      mode_block = false
      override   = true
    }
  }

  custom_headers_config {
    items {
      header   = "Cross-Origin-Opener-Policy"
      value    = "same-origin"
      override = true
    }
    items {
      header   = "Cross-Origin-Resource-Policy"
      value    = "same-origin"
      override = true
    }
    items {
      header   = "Permissions-Policy"
      value    = var.permissions_policy
      override = true
    }
    items {
      header   = "X-Permitted-Cross-Domain-Policies"
      value    = "none"
      override = true
    }
    items {
      header   = "X-DNS-Prefetch-Control"
      value    = "off"
      override = true
    }
  }

  remove_headers_config {
    items {
      header = "Server"
    }
    items {
      header = "X-Powered-By"
    }
    items {
      header = "X-Runtime"
    }
    items {
      header = "X-AspNet-Version"
    }
  }

  lifecycle {
    create_before_destroy = true
  }
}

# ------------------------------------------------------------------------- distribution
resource "aws_cloudfront_origin_access_control" "site" {
  name                              = "${var.name}-site"
  description                       = "docmatch ${var.env}: static site bucket"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_distribution" "this" {
  enabled             = true
  comment             = "docmatch ${var.env}: ${var.domain}"
  aliases             = [var.domain, "www.${var.domain}"]
  default_root_object = "index.html"
  http_version        = "http2and3"
  is_ipv6_enabled     = true
  price_class         = var.price_class
  wait_for_deployment = true

  origin {
    origin_id                = local.site_origin_id
    domain_name              = var.site_bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.site.id
  }

  origin {
    origin_id   = local.app_origin_id
    domain_name = var.origin_hostname

    custom_origin_config {
      http_port                = 80
      https_port               = 443
      origin_protocol_policy   = "https-only"
      origin_ssl_protocols     = ["TLSv1.2"]
      origin_read_timeout      = 60
      origin_keepalive_timeout = 60
    }

    custom_header {
      name  = "x-docmatch-edge"
      value = "cloudfront"
    }
  }

  default_cache_behavior {
    target_origin_id           = local.site_origin_id
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_optimized.id
    origin_request_policy_id   = data.aws_cloudfront_origin_request_policy.cors_s3.id
    response_headers_policy_id = aws_cloudfront_response_headers_policy.this.id

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.viewer_request.arn
    }
  }

  dynamic "ordered_cache_behavior" {
    for_each = var.api_path_patterns
    content {
      path_pattern               = ordered_cache_behavior.value
      target_origin_id           = local.app_origin_id
      viewer_protocol_policy     = "https-only"
      allowed_methods            = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
      cached_methods             = ["GET", "HEAD"]
      compress                   = true
      cache_policy_id            = data.aws_cloudfront_cache_policy.caching_disabled.id
      origin_request_policy_id   = data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id
      response_headers_policy_id = aws_cloudfront_response_headers_policy.this.id

      function_association {
        event_type   = "viewer-request"
        function_arn = aws_cloudfront_function.viewer_request.arn
      }
    }
  }

  # A missing object in S3 is a 403 for CloudFront; show the site's 404 page instead.
  custom_error_response {
    error_code            = 403
    response_code         = 404
    response_page_path    = "/404.html"
    error_caching_min_ttl = 60
  }

  custom_error_response {
    error_code            = 404
    response_code         = 404
    response_page_path    = "/404.html"
    error_caching_min_ttl = 60
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = local.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }

  tags = merge(var.tags, { Name = "${var.name}-edge" })

  lifecycle {
    precondition {
      condition     = data.aws_region.current.region == "us-east-1"
      error_message = "The edge module must run with a us-east-1 provider (ACM for CloudFront and Route 53 DNSSEC keys)."
    }
  }
}

# ---------------------------------------------------------------------- site bucket policy
data "aws_iam_policy_document" "site_bucket" {
  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    actions   = ["s3:*"]
    resources = [var.site_bucket_arn, "${var.site_bucket_arn}/*"]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  statement {
    sid    = "CloudFrontRead"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    actions   = ["s3:GetObject", "s3:ListBucket"]
    resources = [var.site_bucket_arn, "${var.site_bucket_arn}/*"]
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "site" {
  bucket = var.site_bucket_name
  policy = data.aws_iam_policy_document.site_bucket.json
}

# ---------------------------------------------------------------- standard logs (v2)
resource "aws_cloudwatch_log_delivery_source" "access_logs" {
  count = var.enable_access_logs ? 1 : 0

  name         = "${var.name}-cloudfront-access-logs"
  log_type     = "ACCESS_LOGS"
  resource_arn = aws_cloudfront_distribution.this.arn

  tags = merge(var.tags, { Name = "${var.name}-cloudfront-access-logs" })
}

resource "aws_cloudwatch_log_delivery_destination" "access_logs" {
  count = var.enable_access_logs ? 1 : 0

  name          = "${var.name}-cloudfront-access-logs"
  output_format = "json"

  delivery_destination_configuration {
    destination_resource_arn = var.logs_bucket_arn
  }

  tags = merge(var.tags, { Name = "${var.name}-cloudfront-access-logs" })
}

resource "aws_cloudwatch_log_delivery" "access_logs" {
  count = var.enable_access_logs ? 1 : 0

  delivery_source_name     = aws_cloudwatch_log_delivery_source.access_logs[0].name
  delivery_destination_arn = aws_cloudwatch_log_delivery_destination.access_logs[0].arn

  record_fields = [
    "timestamp", "DistributionId", "date", "time", "x-edge-location", "sc-bytes", "c-ip", "cs-method",
    "cs(Host)", "cs-uri-stem", "sc-status", "cs(Referer)", "cs(User-Agent)", "cs-uri-query",
    "x-edge-result-type", "x-edge-request-id", "x-host-header", "cs-protocol", "cs-bytes",
    "time-taken", "ssl-protocol", "ssl-cipher", "x-edge-response-result-type",
    "cs-protocol-version", "c-country", "sc-content-type", "x-edge-detailed-result-type",
  ]

  s3_delivery_configuration {
    suffix_path                 = "cloudfront/{DistributionId}/{yyyy}/{MM}/{dd}/{HH}"
    enable_hive_compatible_path = false
  }

  tags = merge(var.tags, { Name = "${var.name}-cloudfront-access-logs" })
}
