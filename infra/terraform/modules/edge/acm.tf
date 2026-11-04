# Public certificate for CloudFront: apex plus wildcard, validated through DNS records in the
# zone created next to it. Validation only completes once the registrar delegates the zone.

resource "aws_acm_certificate" "this" {
  domain_name               = var.domain
  subject_alternative_names = ["*.${var.domain}"]
  validation_method         = "DNS"
  key_algorithm             = var.certificate_key_algorithm

  options {
    certificate_transparency_logging_preference = "ENABLED"
  }

  tags = merge(var.tags, { Name = "${var.name}-edge" })

  lifecycle {
    create_before_destroy = true
  }
}

# The apex and the wildcard share one validation record; allow_overwrite handles the duplicate.
resource "aws_route53_record" "acm_validation" {
  for_each = {
    for dvo in aws_acm_certificate.this.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      record = dvo.resource_record_value
      type   = dvo.resource_record_type
    }
  }

  zone_id         = aws_route53_zone.this.zone_id
  name            = each.value.name
  type            = each.value.type
  ttl             = 60
  records         = [each.value.record]
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "this" {
  count = var.wait_for_certificate_validation ? 1 : 0

  certificate_arn         = aws_acm_certificate.this.arn
  validation_record_fqdns = [for r in aws_route53_record.acm_validation : r.fqdn]

  timeouts {
    create = "75m"
  }
}

locals {
  certificate_arn = var.wait_for_certificate_validation ? aws_acm_certificate_validation.this[0].certificate_arn : aws_acm_certificate.this.arn
}
