# IAM guardrails: explicit-deny policies standing in for service control policies. The account
# has no AWS Organizations, so these are attached to the operators group and to every role this
# project creates (instance roles, Lambda roles). They cannot restrict the root user; the README
# tells the owner to keep root with MFA and no access keys.

locals {
  # Global services whose calls carry no region (or always us-east-1) and must not be blocked.
  global_services_not_action = [
    "iam:*",
    "sts:*",
    "organizations:*",
    "cloudfront:*",
    "cloudfront-keyvaluestore:*",
    "route53:*",
    "route53domains:*",
    "budgets:*",
    "ce:*",
    "cur:*",
    "support:*",
    "trustedadvisor:*",
    "health:*",
    "account:*",
    "billing:*",
    "tax:*",
    "s3:ListAllMyBuckets",
    "s3:GetAccountPublicAccessBlock",
    "s3:PutAccountPublicAccessBlock",
    "kms:ListAliases",
    "ec2:DescribeRegions",
    "waf:*",
    "shield:*",
    "globalaccelerator:*",
    "resource-explorer-2:*",
    "access-analyzer:*",
    "pricing:*",
    "sso:*",
    "sso-directory:*",
  ]
}

# ---------------------------------------------------------------- deny stopping the audit trail
data "aws_iam_policy_document" "deny_cloudtrail_stop" {
  statement {
    sid    = "DenyCloudTrailTampering"
    effect = "Deny"
    actions = [
      "cloudtrail:StopLogging",
      "cloudtrail:DeleteTrail",
      "cloudtrail:UpdateTrail",
      "cloudtrail:PutEventSelectors",
      "cloudtrail:RemoveTags",
    ]
    resources = [var.cloudtrail_arn]
  }
}

resource "aws_iam_policy" "deny_cloudtrail_stop" {
  name        = "${var.name}-deny-cloudtrail-stop"
  description = "docmatch guardrail: nobody stops, deletes or reconfigures the audit trail"
  policy      = data.aws_iam_policy_document.deny_cloudtrail_stop.json
  tags        = var.tags
}

# -------------------------------------------------------------- deny leaving the allowed regions
data "aws_iam_policy_document" "deny_other_regions" {
  statement {
    sid         = "DenyOutsideAllowedRegions"
    effect      = "Deny"
    not_actions = local.global_services_not_action
    resources   = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestedRegion"
      values   = var.allowed_regions
    }
  }
}

resource "aws_iam_policy" "deny_other_regions" {
  name        = "${var.name}-deny-other-regions"
  description = "docmatch guardrail: regional API calls only in ${join(", ", var.allowed_regions)}"
  policy      = data.aws_iam_policy_document.deny_other_regions.json
  tags        = var.tags
}

# ----------------------------------------------------------------------- deny public S3 access
data "aws_iam_policy_document" "deny_s3_public" {
  statement {
    sid    = "DenyPublicCannedAcls"
    effect = "Deny"
    actions = [
      "s3:PutBucketAcl",
      "s3:PutObjectAcl",
      "s3:PutObject",
    ]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["public-read", "public-read-write", "authenticated-read"]
    }
  }

  statement {
    sid    = "DenyPublicGrantHeaders"
    effect = "Deny"
    actions = [
      "s3:PutBucketAcl",
      "s3:PutObjectAcl",
      "s3:PutObject",
    ]
    resources = ["*"]
    condition {
      test     = "StringLike"
      variable = "s3:x-amz-grant-read"
      values   = ["*AllUsers*", "*AuthenticatedUsers*"]
    }
  }

  # Removing a public access block is denied outright; Terraform only ever creates or updates
  # blocks with every flag set to true.
  statement {
    sid    = "DenyRemovingPublicAccessBlocks"
    effect = "Deny"
    actions = [
      "s3:DeleteBucketPublicAccessBlock",
      "s3:PutAccountPublicAccessBlock",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "DenyObjectOwnershipChanges"
    effect    = "Deny"
    actions   = ["s3:PutBucketOwnershipControls"]
    resources = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "aws:PrincipalTag/docmatch-terraform"
      values   = ["true"]
    }
  }
}

resource "aws_iam_policy" "deny_s3_public" {
  name        = "${var.name}-deny-s3-public"
  description = "docmatch guardrail: no public ACLs and no removal of public access blocks"
  policy      = data.aws_iam_policy_document.deny_s3_public.json
  tags        = var.tags
}

# ------------------------------------------------------------- deny destroying protected data
data "aws_iam_policy_document" "deny_protected_deletion" {
  count = length(var.protected_bucket_arns) + length(var.protected_kms_key_arns) > 0 ? 1 : 0

  dynamic "statement" {
    for_each = length(var.protected_bucket_arns) > 0 ? [1] : []
    content {
      sid    = "DenyProtectedBucketDeletion"
      effect = "Deny"
      actions = [
        "s3:DeleteBucket",
        "s3:DeleteBucketPolicy",
        "s3:PutBucketVersioning",
        "s3:PutLifecycleConfiguration",
      ]
      resources = var.protected_bucket_arns
    }
  }

  dynamic "statement" {
    for_each = length(var.protected_kms_key_arns) > 0 ? [1] : []
    content {
      sid    = "DenyProtectedKeyDeletion"
      effect = "Deny"
      actions = [
        "kms:ScheduleKeyDeletion",
        "kms:DisableKey",
        "kms:PutKeyPolicy",
      ]
      resources = var.protected_kms_key_arns
    }
  }
}

resource "aws_iam_policy" "deny_protected_deletion" {
  count = length(data.aws_iam_policy_document.deny_protected_deletion)

  name        = "${var.name}-deny-protected-deletion"
  description = "docmatch guardrail: backups, state and the DNSSEC key cannot be deleted"
  policy      = data.aws_iam_policy_document.deny_protected_deletion[0].json
  tags        = var.tags
}

# ------------------------------------------------------------------------------- operators
resource "aws_iam_group" "operators" {
  count = var.create_operators_group ? 1 : 0

  name = "${var.name}-operators"
  path = "/docmatch/"
}

locals {
  all_policy_arns = concat(
    [
      aws_iam_policy.deny_cloudtrail_stop.arn,
      aws_iam_policy.deny_other_regions.arn,
      aws_iam_policy.deny_s3_public.arn,
    ],
    [for p in aws_iam_policy.deny_protected_deletion : p.arn],
  )
}

resource "aws_iam_group_policy_attachment" "operators" {
  for_each = var.create_operators_group ? toset(local.all_policy_arns) : toset([])

  group      = aws_iam_group.operators[0].name
  policy_arn = each.value
}
