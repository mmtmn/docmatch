# Network: one VPC, a public subnet for the application instance and an isolated subnet for the
# sandbox Lambda functions. Dual-stack. No NAT gateway (the instance has a public address and the
# sandbox subnet reaches only S3, through a gateway endpoint).

data "aws_availability_zones" "available" {
  state = "available"

  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}

data "aws_ec2_managed_prefix_list" "cloudfront" {
  name = var.cloudfront_prefix_list_name
}

data "aws_region" "current" {}

locals {
  az = var.availability_zone != "" ? var.availability_zone : data.aws_availability_zones.available.names[0]
}

# ----------------------------------------------------------------------------------------- VPC
resource "aws_vpc" "this" {
  cidr_block                       = var.vpc_cidr
  assign_generated_ipv6_cidr_block = true
  enable_dns_support               = true
  enable_dns_hostnames             = true

  tags = merge(var.tags, { Name = "${var.name}-vpc" })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-igw" })
}

# The default security group of a VPC allows all traffic between its members; we manage it so it
# ends up with no rules at all (CIS 5.4).
resource "aws_default_security_group" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-default-locked" })
}

# ------------------------------------------------------------------------------------- subnets
resource "aws_subnet" "app" {
  vpc_id                                         = aws_vpc.this.id
  cidr_block                                     = var.app_subnet_cidr
  ipv6_cidr_block                                = cidrsubnet(aws_vpc.this.ipv6_cidr_block, 8, 0)
  availability_zone                              = local.az
  map_public_ip_on_launch                        = true
  assign_ipv6_address_on_creation                = true
  enable_resource_name_dns_a_record_on_launch    = true
  enable_resource_name_dns_aaaa_record_on_launch = true

  tags = merge(var.tags, { Name = "${var.name}-app", "docmatch:tier" = "app" })
}

resource "aws_subnet" "sandbox" {
  vpc_id                          = aws_vpc.this.id
  cidr_block                      = var.sandbox_subnet_cidr
  ipv6_cidr_block                 = cidrsubnet(aws_vpc.this.ipv6_cidr_block, 8, 16)
  availability_zone               = local.az
  map_public_ip_on_launch         = false
  assign_ipv6_address_on_creation = true

  tags = merge(var.tags, { Name = "${var.name}-sandbox", "docmatch:tier" = "sandbox" })
}

# ------------------------------------------------------------------------------------- routing
resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-public" })
}

resource "aws_route" "public_ipv4" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this.id
}

resource "aws_route" "public_ipv6" {
  route_table_id              = aws_route_table.public.id
  destination_ipv6_cidr_block = "::/0"
  gateway_id                  = aws_internet_gateway.this.id
}

resource "aws_route_table_association" "app" {
  subnet_id      = aws_subnet.app.id
  route_table_id = aws_route_table.public.id
}

# Isolated: only the VPC-local route plus the S3 gateway endpoint prefix list. No IGW, no NAT.
resource "aws_route_table" "sandbox" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name}-sandbox-isolated" })
}

resource "aws_route_table_association" "sandbox" {
  subnet_id      = aws_subnet.sandbox.id
  route_table_id = aws_route_table.sandbox.id
}

# ------------------------------------------------------------------------- S3 gateway endpoint
# Free. The isolated route table always uses it (it is the sandbox's only way to S3). The policy
# only admits requests signed by this account against project buckets: anonymous or foreign
# credentials cannot push data out through the endpoint. That same policy is why the public
# route table does not use it by default: once associated, every S3-bound packet from the
# instance goes through the endpoint, and third-party artifacts served straight from S3 in the
# region (apt mirrors, release downloads) would be denied with a bare 403 during the Packer
# build and at runtime. In-region EC2 to S3 traffic is free either way.
data "aws_iam_policy_document" "s3_endpoint" {
  statement {
    sid    = "ProjectBucketsFromThisAccountOnly"
    effect = "Allow"
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    actions = ["s3:*"]
    resources = [
      "arn:aws:s3:::${var.s3_bucket_name_prefix}*",
      "arn:aws:s3:::${var.s3_bucket_name_prefix}*/*",
    ]
    condition {
      test     = "StringEquals"
      variable = "aws:PrincipalAccount"
      values   = [var.account_id]
    }
  }
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${data.aws_region.current.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = concat([aws_route_table.sandbox.id], var.s3_endpoint_on_app_route_table ? [aws_route_table.public.id] : [])
  policy            = data.aws_iam_policy_document.s3_endpoint.json

  tags = merge(var.tags, { Name = "${var.name}-s3-endpoint" })
}

# ----------------------------------------------------------------------------- security groups
# Application instance: 443 only from CloudFront's origin-facing ranges. No SSH (SSM Session
# Manager). Egress is coarse here and tight on the box (nftables, see infra/ansible).
resource "aws_security_group" "app" {
  name        = "${var.name}-app"
  description = "docmatch app instance: HTTPS from CloudFront only, no SSH"
  vpc_id      = aws_vpc.this.id

  tags = merge(var.tags, { Name = "${var.name}-app" })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "app_https_from_cloudfront" {
  security_group_id = aws_security_group.app.id
  description       = "HTTPS from the CloudFront origin-facing prefix list"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  prefix_list_id    = data.aws_ec2_managed_prefix_list.cloudfront.id

  tags = merge(var.tags, { Name = "${var.name}-app-https-cloudfront" })
}

resource "aws_vpc_security_group_egress_rule" "app_https_v4" {
  security_group_id = aws_security_group.app.id
  description       = "HTTPS egress (AWS APIs, egress proxy targets, ACME)"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"

  tags = merge(var.tags, { Name = "${var.name}-app-egress-https-v4" })
}

resource "aws_vpc_security_group_egress_rule" "app_https_v6" {
  security_group_id = aws_security_group.app.id
  description       = "HTTPS egress over IPv6"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv6         = "::/0"

  tags = merge(var.tags, { Name = "${var.name}-app-egress-https-v6" })
}

resource "aws_vpc_security_group_egress_rule" "app_http_v4" {
  security_group_id = aws_security_group.app.id
  description       = "HTTP egress for apt mirrors and OCSP (through the egress proxy only, enforced by nftables)"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = "0.0.0.0/0"

  tags = merge(var.tags, { Name = "${var.name}-app-egress-http-v4" })
}

resource "aws_vpc_security_group_egress_rule" "app_http_v6" {
  security_group_id = aws_security_group.app.id
  description       = "HTTP egress over IPv6 (apt mirrors through the egress proxy)"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv6         = "::/0"

  tags = merge(var.tags, { Name = "${var.name}-app-egress-http-v6" })
}

# Pool of on-demand (burst) workers (docs/escala.md): no ingress at all; egress like the
# application (HTTPS and HTTP, tight on the box through nftables) plus the internal endpoint of the
# application instance, which admits exactly this group on that port.
resource "aws_security_group" "burst" {
  name        = "${var.name}-burst"
  description = "docmatch burst workers: no ingress, HTTPS out, the app internal endpoint"
  vpc_id      = aws_vpc.this.id

  tags = merge(var.tags, { Name = "${var.name}-burst" })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_egress_rule" "burst_https_v4" {
  security_group_id = aws_security_group.burst.id
  description       = "HTTPS egress (S3 presigned downloads, AWS APIs, egress proxy targets)"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"

  tags = merge(var.tags, { Name = "${var.name}-burst-egress-https-v4" })
}

resource "aws_vpc_security_group_egress_rule" "burst_https_v6" {
  security_group_id = aws_security_group.burst.id
  description       = "HTTPS egress over IPv6"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv6         = "::/0"

  tags = merge(var.tags, { Name = "${var.name}-burst-egress-https-v6" })
}

resource "aws_vpc_security_group_egress_rule" "burst_http_v4" {
  security_group_id = aws_security_group.burst.id
  description       = "HTTP egress for apt mirrors and OCSP (through the egress proxy only, enforced by nftables)"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = "0.0.0.0/0"

  tags = merge(var.tags, { Name = "${var.name}-burst-egress-http-v4" })
}

resource "aws_vpc_security_group_egress_rule" "burst_to_app_internal" {
  security_group_id            = aws_security_group.burst.id
  description                  = "Internal endpoint of the application instance (job manifests, events)"
  ip_protocol                  = "tcp"
  from_port                    = var.burst_internal_port
  to_port                      = var.burst_internal_port
  referenced_security_group_id = aws_security_group.app.id

  tags = merge(var.tags, { Name = "${var.name}-burst-egress-internal" })
}

resource "aws_vpc_security_group_ingress_rule" "app_internal_from_burst" {
  security_group_id            = aws_security_group.app.id
  description                  = "Internal endpoint for the burst workers only"
  ip_protocol                  = "tcp"
  from_port                    = var.burst_internal_port
  to_port                      = var.burst_internal_port
  referenced_security_group_id = aws_security_group.burst.id

  tags = merge(var.tags, { Name = "${var.name}-app-internal-burst" })
}

# Sandbox Lambda functions: no ingress at all, egress only to the S3 gateway endpoint.
resource "aws_security_group" "sandbox" {
  name        = "${var.name}-sandbox"
  description = "docmatch sandbox Lambdas: no ingress, egress only to the S3 gateway endpoint"
  vpc_id      = aws_vpc.this.id

  tags = merge(var.tags, { Name = "${var.name}-sandbox" })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_egress_rule" "sandbox_s3" {
  security_group_id = aws_security_group.sandbox.id
  description       = "HTTPS to the S3 gateway endpoint prefix list only"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  prefix_list_id    = aws_vpc_endpoint.s3.prefix_list_id

  tags = merge(var.tags, { Name = "${var.name}-sandbox-egress-s3" })
}

# ----------------------------------------------------------------------------------- flow logs
resource "aws_flow_log" "reject" {
  count = var.enable_flow_logs ? 1 : 0

  vpc_id                   = aws_vpc.this.id
  traffic_type             = "REJECT"
  log_destination_type     = "s3"
  log_destination          = "${var.flow_logs_bucket_arn}/vpc-flow-logs/"
  max_aggregation_interval = 600

  destination_options {
    file_format        = "parquet"
    per_hour_partition = true
  }

  tags = merge(var.tags, { Name = "${var.name}-flow-logs-reject" })
}
