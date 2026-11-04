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

  # Deleting a public access block (bucket or account level) is denied outright. Writing one is
  # allowed: Terraform creates the bucket blocks with every flag true, and the owner may still
  # turn on the account-level block (there is no condition key for the flag values, so the
  # alternative would be to deny enabling it as well). Object ownership is not guarded here:
  # with BlockPublicAcls and IgnorePublicAcls in force, ACLs cannot grant public access whatever
  # the ownership setting, and a tag-conditioned deny would break every bucket (re)creation.
  statement {
    sid    = "DenyRemovingPublicAccessBlocks"
    effect = "Deny"
    actions = [
      "s3:DeleteBucketPublicAccessBlock",
      "s3:DeleteAccountPublicAccessBlock",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "deny_s3_public" {
  name        = "${var.name}-deny-s3-public"
  description = "docmatch guardrail: no public ACLs and no removal of public access blocks"
  policy      = data.aws_iam_policy_document.deny_s3_public.json
  tags        = var.tags
}

# ------------------------------------------------------------- deny destroying protected data
# count derives from the length of the two lists, so callers must build them with a length known
# at plan time (a known condition around an unknown ARN is fine; compact() over one is not).
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

# Attachment maps are keyed by a static name: a for_each key must be known at plan time and the
# policy ARNs only exist after the first apply, so the ARNs travel in the values.
locals {
  role_policy_arns = {
    cloudtrail = aws_iam_policy.deny_cloudtrail_stop.arn
    regions    = aws_iam_policy.deny_other_regions.arn
    s3_public  = aws_iam_policy.deny_s3_public.arn
  }
  all_policy_arns = merge(
    local.role_policy_arns,
    { for p in aws_iam_policy.deny_protected_deletion : "protected_deletion" => p.arn },
  )
}

resource "aws_iam_group_policy_attachment" "operators" {
  for_each = { for k, arn in local.all_policy_arns : k => arn if var.create_operators_group }

  group      = aws_iam_group.operators[0].name
  policy_arn = each.value
}

# Operators without a fresh MFA can only manage their own password and MFA device.
data "aws_iam_policy_document" "require_mfa" {
  statement {
    sid    = "AllowViewAccountInfo"
    effect = "Allow"
    actions = [
      "iam:GetAccountPasswordPolicy",
      "iam:ListVirtualMFADevices",
    ]
    resources = ["*"]
  }

  statement {
    sid    = "AllowManageOwnPasswordAndMfa"
    effect = "Allow"
    actions = [
      "iam:ChangePassword",
      "iam:GetUser",
      "iam:CreateVirtualMFADevice",
      "iam:EnableMFADevice",
      "iam:ListMFADevices",
      "iam:ResyncMFADevice",
    ]
    resources = [
      "arn:aws:iam::*:user/$${aws:username}",
      "arn:aws:iam::*:mfa/$${aws:username}",
    ]
  }

  statement {
    sid    = "DenyEverythingElseWithoutMfa"
    effect = "Deny"
    not_actions = [
      "iam:ChangePassword",
      "iam:GetUser",
      "iam:CreateVirtualMFADevice",
      "iam:EnableMFADevice",
      "iam:ListMFADevices",
      "iam:ListVirtualMFADevices",
      "iam:ResyncMFADevice",
      "iam:GetAccountPasswordPolicy",
      "sts:GetSessionToken",
    ]
    resources = ["*"]
    condition {
      test     = "BoolIfExists"
      variable = "aws:MultiFactorAuthPresent"
      values   = ["false"]
    }
  }
}

resource "aws_iam_group_policy" "require_mfa" {
  count = var.create_operators_group ? 1 : 0

  name   = "require-mfa"
  group  = aws_iam_group.operators[0].name
  policy = data.aws_iam_policy_document.require_mfa.json
}
