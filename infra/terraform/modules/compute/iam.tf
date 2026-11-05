# Instance roles. The AWS managed policy AmazonSSMManagedInstanceCore is avoided on purpose: it
# grants ssm:GetParameter(s) on every parameter of the account, which hosts other projects. Only
# the Session Manager channel permissions are granted, plus the project's own paths and prefixes.

data "aws_iam_policy_document" "assume_ec2" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.account_id]
    }
  }
}

# Session Manager (no SSH anywhere). These actions do not support resource-level permissions.
data "aws_iam_policy_document" "session_manager" {
  statement {
    sid    = "SessionManagerChannel"
    effect = "Allow"
    actions = [
      "ssm:UpdateInstanceInformation",
      "ssmmessages:CreateControlChannel",
      "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel",
      "ssmmessages:OpenDataChannel",
      "ec2messages:AcknowledgeMessage",
      "ec2messages:DeleteMessage",
      "ec2messages:FailMessage",
      "ec2messages:GetEndpoint",
      "ec2messages:GetMessages",
      "ec2messages:SendReply",
    ]
    resources = ["*"]
  }
}

locals {
  ssm_parameter_arn = "arn:aws:ssm:${var.region}:${var.account_id}:parameter${var.ssm_path_prefix}"
  # The pool's group by name (the ARN embeds a generated id): exactly that group, known before it exists.
  worker_asg_arn  = "arn:aws:autoscaling:${var.region}:${var.account_id}:autoScalingGroup:*:autoScalingGroupName/${local.worker_asg_name}"
  upload_prefixes = ["quarantine", "clean", "derived"]
  eip_arn         = "arn:aws:ec2:${var.region}:${var.account_id}:elastic-ip/${aws_eip.app.allocation_id}"
  instances_arn   = "arn:aws:ec2:${var.region}:${var.account_id}:instance/*"
  enis_arn        = "arn:aws:ec2:${var.region}:${var.account_id}:network-interface/*"
}

# Shared by the application and worker roles: parameters, uploads, verdict queue, sandbox calls.
data "aws_iam_policy_document" "workload" {
  statement {
    sid    = "ReadOwnParameters"
    effect = "Allow"
    actions = [
      "ssm:GetParametersByPath",
      "ssm:GetParameters",
      "ssm:GetParameter",
    ]
    resources = [local.ssm_parameter_arn, "${local.ssm_parameter_arn}/*"]
  }

  statement {
    sid       = "ListUploads"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [var.uploads_bucket_arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = concat([for p in local.upload_prefixes : "${p}/*"], ["${var.verdicts_prefix}*"])
    }
  }

  statement {
    sid    = "ReadWriteUploads"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
      "s3:GetObjectTagging",
      "s3:PutObjectTagging",
    ]
    resources = [for p in local.upload_prefixes : "${var.uploads_bucket_arn}/${p}/*"]
  }

  # Application binaries (infra/scripts/deploy-app.sh): read-only, installed by root over SSM.
  statement {
    sid       = "ReadReleases"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${var.releases_bucket_arn}/releases/*"]
  }

  statement {
    sid       = "ListReleases"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [var.releases_bucket_arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["releases/*"]
    }
  }

  # Verdict objects are written by the scanner only; the worker reads one when its S3 event
  # arrives on the queue and deletes it afterwards.
  statement {
    sid       = "ConsumeVerdictObjects"
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:DeleteObject"]
    resources = ["${var.uploads_bucket_arn}/${var.verdicts_prefix}*"]
  }

  statement {
    sid    = "ConsumeVerdicts"
    effect = "Allow"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
      "sqs:GetQueueUrl",
    ]
    resources = [var.verdict_queue_arn]
  }

  dynamic "statement" {
    for_each = length(var.invokable_function_arns) > 0 ? [1] : []
    content {
      sid       = "InvokeSandbox"
      effect    = "Allow"
      actions   = ["lambda:InvokeFunction"]
      resources = var.invokable_function_arns
    }
  }

  dynamic "statement" {
    for_each = var.log_group_arn != "" ? [1] : []
    content {
      sid       = "ShipLogs"
      effect    = "Allow"
      actions   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"]
      resources = ["${var.log_group_arn}:*"]
    }
  }
}

# Burst workers (docs/escala.md): the named parameters (no DATABASE_URL, no store or provider
# credential beyond the model key) and the release they install at boot. No uploads, no queue,
# no sandbox: the inputs of a run come as presigned URLs in its manifest. No log group either:
# there is no shipper on the box; the worker's last error reaches burst_instances instead.
data "aws_iam_policy_document" "burst" {
  statement {
    sid       = "ReadBurstParameters"
    effect    = "Allow"
    actions   = ["ssm:GetParameters", "ssm:GetParameter"]
    resources = [for n in local.burst_param_names : "${local.ssm_parameter_arn}/${n}"]
  }

  statement {
    sid       = "ReadReleases"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${var.releases_bucket_arn}/releases/*"]
  }

  statement {
    sid       = "ListReleases"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [var.releases_bucket_arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["releases/*"]
    }
  }
}

# Application instance only: data volume, Elastic IP, backups and the ACME DNS-01 challenge.
data "aws_iam_policy_document" "app_only" {
  statement {
    sid     = "AttachOwnDataVolume"
    effect  = "Allow"
    actions = ["ec2:AttachVolume", "ec2:DetachVolume"]
    resources = [
      aws_ebs_volume.data.arn,
      local.instances_arn,
    ]
    condition {
      test     = "StringEquals"
      variable = "ec2:ResourceTag/docmatch:role"
      values   = ["app"]
    }
  }

  statement {
    sid       = "DescribeForBootstrap"
    effect    = "Allow"
    actions   = ["ec2:DescribeVolumes", "ec2:DescribeInstances", "ec2:DescribeAddresses"]
    resources = ["*"]
  }

  statement {
    sid       = "AssociateOwnElasticIp"
    effect    = "Allow"
    actions   = ["ec2:AssociateAddress", "ec2:DisassociateAddress"]
    resources = [local.eip_arn, local.instances_arn, local.enis_arn]
  }

  statement {
    sid       = "ListBackups"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [var.backups_bucket_arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["pgbackrest/*"]
    }
  }

  statement {
    sid    = "ReadWriteBackups"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
    ]
    resources = ["${var.backups_bucket_arn}/pgbackrest/*"]
  }

  # Caddy obtains the origin certificate with DNS-01; only the challenge TXT record is writable.
  statement {
    sid       = "AcmeChallengeRecord"
    effect    = "Allow"
    actions   = ["route53:ChangeResourceRecordSets"]
    resources = ["arn:aws:route53:::hostedzone/${var.hosted_zone_id}"]
    condition {
      test     = "ForAllValues:StringEquals"
      variable = "route53:ChangeResourceRecordSetsRecordTypes"
      values   = ["TXT"]
    }
    condition {
      test     = "ForAllValues:StringEquals"
      variable = "route53:ChangeResourceRecordSetsNormalizedRecordNames"
      values   = ["_acme-challenge.${var.origin_hostname}"]
    }
  }

  statement {
    sid       = "AcmeChallengeRead"
    effect    = "Allow"
    actions   = ["route53:ListResourceRecordSets"]
    resources = ["arn:aws:route53:::hostedzone/${var.hosted_zone_id}"]
  }

  statement {
    sid       = "AcmeChallengeStatus"
    effect    = "Allow"
    actions   = ["route53:GetChange"]
    resources = ["arn:aws:route53:::change/*"]
  }

  # The main worker sizes the pool and protects the busy instances (docs/escala.md): that group only.
  statement {
    sid       = "ScaleBurstPool"
    effect    = "Allow"
    actions   = ["autoscaling:SetDesiredCapacity", "autoscaling:SetInstanceProtection"]
    resources = [local.worker_asg_arn]
  }

  # Describe has no resource-level permission; it reads, never changes.
  statement {
    sid       = "DescribeBurstPool"
    effect    = "Allow"
    actions   = ["autoscaling:DescribeAutoScalingGroups"]
    resources = ["*"]
  }

  # The application publishes its internal endpoint for the burst workers at boot.
  statement {
    sid       = "PublishBurstEndpoint"
    effect    = "Allow"
    actions   = ["ssm:PutParameter"]
    resources = ["${local.ssm_parameter_arn}/DM_BURST_API_URL"]
  }
}

# --------------------------------------------------------------------------------- app role
resource "aws_iam_role" "app" {
  name               = "${var.name}-app"
  path               = "/docmatch/"
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
  tags               = merge(var.tags, { Name = "${var.name}-app" })
}

resource "aws_iam_role_policy" "app_session_manager" {
  name   = "session-manager"
  role   = aws_iam_role.app.id
  policy = data.aws_iam_policy_document.session_manager.json
}

resource "aws_iam_role_policy" "app_workload" {
  name   = "workload"
  role   = aws_iam_role.app.id
  policy = data.aws_iam_policy_document.workload.json
}

resource "aws_iam_role_policy" "app_only" {
  name   = "app-only"
  role   = aws_iam_role.app.id
  policy = data.aws_iam_policy_document.app_only.json
}

# Guardrail attachments iterate over the static keys of the map; the ARNs (values) are only
# known once the policies exist, which is fine for for_each.
resource "aws_iam_role_policy_attachment" "app_guardrails" {
  for_each = var.guardrail_policy_arns

  role       = aws_iam_role.app.name
  policy_arn = each.value
}

resource "aws_iam_instance_profile" "app" {
  name = "${var.name}-app"
  path = "/docmatch/"
  role = aws_iam_role.app.name
  tags = merge(var.tags, { Name = "${var.name}-app" })
}

# ------------------------------------------------------------------------------ worker role
resource "aws_iam_role" "worker" {
  name               = "${var.name}-worker"
  path               = "/docmatch/"
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
  tags               = merge(var.tags, { Name = "${var.name}-worker" })
}

resource "aws_iam_role_policy" "worker_session_manager" {
  name   = "session-manager"
  role   = aws_iam_role.worker.id
  policy = data.aws_iam_policy_document.session_manager.json
}

# The worker role is the burst pool's: the narrow policy above, not the application's workload.
resource "aws_iam_role_policy" "worker_burst" {
  name   = "burst"
  role   = aws_iam_role.worker.id
  policy = data.aws_iam_policy_document.burst.json
}

resource "aws_iam_role_policy_attachment" "worker_guardrails" {
  for_each = var.guardrail_policy_arns

  role       = aws_iam_role.worker.name
  policy_arn = each.value
}

resource "aws_iam_instance_profile" "worker" {
  name = "${var.name}-worker"
  path = "/docmatch/"
  role = aws_iam_role.worker.name
  tags = merge(var.tags, { Name = "${var.name}-worker" })
}

# ------------------------------------------------------------------------------ packer role
# The build instance only needs Session Manager (Packer connects through an SSM tunnel).
resource "aws_iam_role" "packer" {
  count = var.create_packer_profile ? 1 : 0

  name               = "${var.name}-packer"
  path               = "/docmatch/"
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
  tags               = merge(var.tags, { Name = "${var.name}-packer" })
}

resource "aws_iam_role_policy" "packer_session_manager" {
  count = var.create_packer_profile ? 1 : 0

  name   = "session-manager"
  role   = aws_iam_role.packer[0].id
  policy = data.aws_iam_policy_document.session_manager.json
}

resource "aws_iam_role_policy_attachment" "packer_guardrails" {
  for_each = { for k, arn in var.guardrail_policy_arns : k => arn if var.create_packer_profile }

  role       = aws_iam_role.packer[0].name
  policy_arn = each.value
}

resource "aws_iam_instance_profile" "packer" {
  count = var.create_packer_profile ? 1 : 0

  name = "${var.name}-packer"
  path = "/docmatch/"
  role = aws_iam_role.packer[0].name
  tags = merge(var.tags, { Name = "${var.name}-packer" })
}
