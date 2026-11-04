# Route 53 public zone for the domain, signed with DNSSEC (KMS key in us-east-1). The registrar
# must point the NS records at this zone and publish the DS record (both are outputs).

resource "aws_route53_zone" "this" {
  name    = var.domain
  comment = "docmatch ${var.env}: managed by Terraform (infra/terraform)"

  tags = merge(var.tags, { Name = "${var.name}-zone" })
}

# ---------------------------------------------------------------------------------- DNSSEC
data "aws_iam_policy_document" "dnssec_key" {
  statement {
    sid    = "AllowRoute53DnssecService"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["dnssec-route53.amazonaws.com"]
    }
    actions   = ["kms:DescribeKey", "kms:GetPublicKey", "kms:Sign"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:route53:::hostedzone/*"]
    }
  }

  statement {
    sid    = "AllowRoute53DnssecServiceGrants"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["dnssec-route53.amazonaws.com"]
    }
    actions   = ["kms:CreateGrant"]
    resources = ["*"]
    condition {
      test     = "Bool"
      variable = "kms:GrantIsForAWSResource"
      values   = ["true"]
    }
  }

  statement {
    sid    = "AccountAdministration"
    effect = "Allow"
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${var.account_id}:root"]
    }
    actions   = ["kms:*"]
    resources = ["*"]
  }
}

resource "aws_kms_key" "dnssec" {
  count = var.dnssec_enabled ? 1 : 0

  description              = "docmatch ${var.env}: Route 53 DNSSEC key-signing key for ${var.domain}"
  customer_master_key_spec = "ECC_NIST_P256"
  key_usage                = "SIGN_VERIFY"
  deletion_window_in_days  = 30
  enable_key_rotation      = false
  policy                   = data.aws_iam_policy_document.dnssec_key.json

  tags = merge(var.tags, { Name = "${var.name}-dnssec" })
}

resource "aws_kms_alias" "dnssec" {
  count = var.dnssec_enabled ? 1 : 0

  name          = "alias/${var.name}-dnssec"
  target_key_id = aws_kms_key.dnssec[0].key_id
}

resource "aws_route53_key_signing_key" "this" {
  count = var.dnssec_enabled ? 1 : 0

  hosted_zone_id             = aws_route53_zone.this.id
  key_management_service_arn = aws_kms_key.dnssec[0].arn
  name                       = "${var.name}-ksk"
  status                     = "ACTIVE"
}

resource "aws_route53_hosted_zone_dnssec" "this" {
  count = var.dnssec_enabled ? 1 : 0

  hosted_zone_id = aws_route53_key_signing_key.this[0].hosted_zone_id
  signing_status = "SIGNING"

  depends_on = [aws_route53_key_signing_key.this]
}

# ----------------------------------------------------------------------------------- records
resource "aws_route53_record" "apex_a" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.this.domain_name
    zone_id                = aws_cloudfront_distribution.this.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "apex_aaaa" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "AAAA"

  alias {
    name                   = aws_cloudfront_distribution.this.domain_name
    zone_id                = aws_cloudfront_distribution.this.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "www_a" {
  zone_id = aws_route53_zone.this.zone_id
  name    = "www.${var.domain}"
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.this.domain_name
    zone_id                = aws_cloudfront_distribution.this.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "www_aaaa" {
  zone_id = aws_route53_zone.this.zone_id
  name    = "www.${var.domain}"
  type    = "AAAA"

  alias {
    name                   = aws_cloudfront_distribution.this.domain_name
    zone_id                = aws_cloudfront_distribution.this.hosted_zone_id
    evaluate_target_health = false
  }
}

locals {
  # Certificate authorities allowed to issue for the domain: ACM (Amazon) for CloudFront and
  # Let's Encrypt for the origin certificate Caddy obtains on the instance.
  caa_issuers = ["amazon.com", "amazontrust.com", "awstrust.com", "amazonaws.com", "letsencrypt.org"]
}

resource "aws_route53_record" "caa" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "CAA"
  ttl     = 3600
  records = concat(
    [for ca in local.caa_issuers : "0 issue \"${ca}\""],
    [for ca in slice(local.caa_issuers, 0, 4) : "0 issuewild \"${ca}\""],
  )
}

resource "aws_route53_record" "spf" {
  count = var.apex_spf != "" ? 1 : 0

  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "TXT"
  ttl     = 3600
  records = [var.apex_spf]
}

resource "aws_route53_record" "dmarc" {
  zone_id = aws_route53_zone.this.zone_id
  name    = "_dmarc.${var.domain}"
  type    = "TXT"
  ttl     = 3600
  records = [
    var.dmarc_rua_email != "" ? "v=DMARC1; p=reject; rua=mailto:${var.dmarc_rua_email}; adkim=s; aspf=s" : "v=DMARC1; p=reject; adkim=s; aspf=s"
  ]
}

resource "aws_route53_record" "extra" {
  for_each = { for r in var.extra_records : "${r.name}/${r.type}" => r }

  zone_id = aws_route53_zone.this.zone_id
  name    = each.value.name == "" ? var.domain : "${each.value.name}.${var.domain}"
  type    = each.value.type
  ttl     = each.value.ttl
  records = each.value.records
}
