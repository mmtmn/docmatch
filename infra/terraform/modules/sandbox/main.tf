# Sandbox: five Lambda functions built from ECR images (arm64), each with its own role and
# reserved concurrency. scanner, mediarunner and toolrunner live in the isolated subnet and can
# only talk to S3 through the gateway endpoint. fetcher and signature-updater need the internet
# and therefore run outside the VPC, where they cannot see anything private.
#
# Flow: upload lands in uploads/quarantine/ -> S3 event -> scanner -> verdict on SQS; clean
# objects are copied to clean/ by the scanner, the worker on the instance consumes verdicts.

locals {
  enabled       = var.image_tag != ""
  function_name = { for k, _ in var.functions : k => "${var.name}-${k}" }
  functions_on  = local.enabled ? var.functions : {}
}

# -------------------------------------------------------------------------------------- ECR
resource "aws_ecr_repository" "this" {
  for_each = var.functions

  name                 = "${var.name}/${each.key}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = var.ecr_force_delete

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }

  tags = merge(var.tags, { Name = "${var.name}-${each.key}" })
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each = aws_ecr_repository.this

  repository = each.value.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "expire untagged images after one day"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = 1
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "keep only the last ${var.ecr_keep_images} tagged images"
        selection = {
          tagStatus      = "tagged"
          tagPatternList = ["*"]
          countType      = "imageCountMoreThan"
          countNumber    = var.ecr_keep_images
        }
        action = { type = "expire" }
      },
    ]
  })
}

# -------------------------------------------------------------------------------------- SQS
resource "aws_sqs_queue" "verdicts_dlq" {
  name                      = "${var.name}-scan-verdicts-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true

  tags = merge(var.tags, { Name = "${var.name}-scan-verdicts-dlq" })
}

resource "aws_sqs_queue" "verdicts" {
  name                       = "${var.name}-scan-verdicts"
  message_retention_seconds  = 345600
  visibility_timeout_seconds = 60
  receive_wait_time_seconds  = 20
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.verdicts_dlq.arn
    maxReceiveCount     = 5
  })

  tags = merge(var.tags, { Name = "${var.name}-scan-verdicts" })
}

resource "aws_sqs_queue_redrive_allow_policy" "verdicts_dlq" {
  queue_url = aws_sqs_queue.verdicts_dlq.id

  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.verdicts.arn]
  })
}

# Asynchronous invocations (S3 events, schedules) that fail after retries land here.
resource "aws_sqs_queue" "lambda_dlq" {
  name                      = "${var.name}-lambda-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true

  tags = merge(var.tags, { Name = "${var.name}-lambda-dlq" })
}

data "aws_iam_policy_document" "queue_tls_only" {
  for_each = {
    verdicts     = aws_sqs_queue.verdicts.arn
    verdicts_dlq = aws_sqs_queue.verdicts_dlq.arn
    lambda_dlq   = aws_sqs_queue.lambda_dlq.arn
  }

  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    actions   = ["sqs:*"]
    resources = [each.value]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_sqs_queue_policy" "tls_only" {
  for_each = {
    verdicts     = aws_sqs_queue.verdicts.id
    verdicts_dlq = aws_sqs_queue.verdicts_dlq.id
    lambda_dlq   = aws_sqs_queue.lambda_dlq.id
  }

  queue_url = each.value
  policy    = data.aws_iam_policy_document.queue_tls_only[each.key].json
}

# -------------------------------------------------------------------------------- log groups
resource "aws_cloudwatch_log_group" "fn" {
  for_each = var.functions

  name              = "/aws/lambda/${local.function_name[each.key]}"
  retention_in_days = var.log_retention_days

  tags = merge(var.tags, { Name = "${var.name}-${each.key}-logs" })
}

# --------------------------------------------------------------------------------- IAM roles
data "aws_iam_policy_document" "assume_lambda" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.account_id]
    }
  }
}

resource "aws_iam_role" "fn" {
  for_each = var.functions

  name               = "${var.name}-lambda-${each.key}"
  path               = "/docmatch/"
  assume_role_policy = data.aws_iam_policy_document.assume_lambda.json

  tags = merge(var.tags, { Name = "${var.name}-lambda-${each.key}" })
}

# Logs for every function, ENI management only for the in-VPC ones.
data "aws_iam_policy_document" "fn_base" {
  for_each = var.functions

  statement {
    sid    = "WriteOwnLogs"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["${aws_cloudwatch_log_group.fn[each.key].arn}:*"]
  }

  dynamic "statement" {
    for_each = each.value.in_vpc ? [1] : []
    content {
      sid    = "ManageOwnNetworkInterfaces"
      effect = "Allow"
      actions = [
        "ec2:CreateNetworkInterface",
        "ec2:DeleteNetworkInterface",
        "ec2:AssignPrivateIpAddresses",
        "ec2:UnassignPrivateIpAddresses",
      ]
      resources = [
        "arn:aws:ec2:${var.region}:${var.account_id}:network-interface/*",
        "arn:aws:ec2:${var.region}:${var.account_id}:subnet/${var.subnet_id}",
        "arn:aws:ec2:${var.region}:${var.account_id}:security-group/${var.security_group_id}",
      ]
    }
  }

  dynamic "statement" {
    for_each = each.value.in_vpc ? [1] : []
    content {
      sid       = "DescribeNetworkInterfaces"
      effect    = "Allow"
      actions   = ["ec2:DescribeNetworkInterfaces", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups"]
      resources = ["*"]
    }
  }

  statement {
    sid       = "DeadLetterQueue"
    effect    = "Allow"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.lambda_dlq.arn]
  }
}

resource "aws_iam_role_policy" "fn_base" {
  for_each = var.functions

  name   = "base"
  role   = aws_iam_role.fn[each.key].id
  policy = data.aws_iam_policy_document.fn_base[each.key].json
}

# Per-function data access. Everything is prefix-scoped inside the two buckets.
data "aws_iam_policy_document" "scanner" {
  statement {
    sid       = "ReadQuarantine"
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:GetObjectTagging", "s3:DeleteObject"]
    resources = ["${var.uploads_bucket_arn}/quarantine/*"]
  }
  statement {
    sid       = "WriteClean"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:PutObjectTagging"]
    resources = ["${var.uploads_bucket_arn}/clean/*"]
  }
  statement {
    sid       = "ReadSignatures"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${var.signatures_bucket_arn}/*"]
  }
  statement {
    sid       = "ListSignatures"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [var.signatures_bucket_arn]
  }
  statement {
    sid       = "PublishVerdict"
    effect    = "Allow"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.verdicts.arn]
  }
}

data "aws_iam_policy_document" "mediarunner" {
  statement {
    sid       = "ReadClean"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${var.uploads_bucket_arn}/clean/*"]
  }
  statement {
    sid       = "WriteDerived"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:AbortMultipartUpload"]
    resources = ["${var.uploads_bucket_arn}/derived/*"]
  }
}

data "aws_iam_policy_document" "toolrunner" {
  statement {
    sid       = "ReadClean"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${var.uploads_bucket_arn}/clean/*", "${var.uploads_bucket_arn}/derived/*"]
  }
  statement {
    sid       = "WriteDerived"
    effect    = "Allow"
    actions   = ["s3:PutObject"]
    resources = ["${var.uploads_bucket_arn}/derived/*"]
  }
}

data "aws_iam_policy_document" "fetcher" {
  statement {
    sid       = "WriteQuarantine"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:AbortMultipartUpload"]
    resources = ["${var.uploads_bucket_arn}/quarantine/*"]
  }
}

data "aws_iam_policy_document" "signature_updater" {
  statement {
    sid       = "WriteSignatures"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"]
    resources = ["${var.signatures_bucket_arn}/*"]
  }
  statement {
    sid       = "ListSignatures"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [var.signatures_bucket_arn]
  }
}

locals {
  data_policies = {
    scanner             = data.aws_iam_policy_document.scanner.json
    mediarunner         = data.aws_iam_policy_document.mediarunner.json
    toolrunner          = data.aws_iam_policy_document.toolrunner.json
    fetcher             = data.aws_iam_policy_document.fetcher.json
    "signature-updater" = data.aws_iam_policy_document.signature_updater.json
  }
}

resource "aws_iam_role_policy" "fn_data" {
  for_each = var.functions

  name   = "data-access"
  role   = aws_iam_role.fn[each.key].id
  policy = local.data_policies[each.key]
}

resource "aws_iam_role_policy_attachment" "guardrails" {
  for_each = { for pair in setproduct(keys(var.functions), var.guardrail_policy_arns) : "${pair[0]}:${pair[1]}" => pair }

  role       = aws_iam_role.fn[each.value[0]].name
  policy_arn = each.value[1]
}

# ----------------------------------------------------------------------------------- functions
resource "aws_lambda_function" "this" {
  for_each = local.functions_on

  function_name = local.function_name[each.key]
  description   = "docmatch ${var.env} sandbox: ${each.key}"
  role          = aws_iam_role.fn[each.key].arn
  package_type  = "Image"
  image_uri     = "${aws_ecr_repository.this[each.key].repository_url}:${var.image_tag}"
  architectures = ["arm64"]

  memory_size                    = each.value.memory
  timeout                        = each.value.timeout
  reserved_concurrent_executions = each.value.reserved

  ephemeral_storage {
    size = each.value.ephemeral
  }

  dynamic "vpc_config" {
    for_each = each.value.in_vpc ? [1] : []
    content {
      subnet_ids                  = [var.subnet_id]
      security_group_ids          = [var.security_group_id]
      ipv6_allowed_for_dual_stack = false
    }
  }

  dead_letter_config {
    target_arn = aws_sqs_queue.lambda_dlq.arn
  }

  logging_config {
    log_format            = "JSON"
    log_group             = aws_cloudwatch_log_group.fn[each.key].name
    application_log_level = "INFO"
    system_log_level      = "WARN"
  }

  # No secrets here: the sandbox never holds credentials beyond its role.
  environment {
    variables = {
      DM_ENV                 = var.env
      DM_SANDBOX_FUNCTION    = each.key
      DM_S3_UPLOADS_BUCKET   = var.uploads_bucket_name
      DM_SIGNATURES_BUCKET   = var.signatures_bucket_name
      DM_SCAN_VERDICTS_QUEUE = aws_sqs_queue.verdicts.url
    }
  }

  tracing_config {
    mode = "PassThrough"
  }

  tags = merge(var.tags, { Name = local.function_name[each.key], "docmatch:sandbox" = each.key })

  depends_on = [aws_iam_role_policy.fn_base, aws_iam_role_policy.fn_data]
}

# Async invocations (S3 events, schedules): one retry, then the dead-letter queue.
resource "aws_lambda_function_event_invoke_config" "async" {
  for_each = { for k, f in local.functions_on : k => f if contains(["scanner", "signature-updater"], k) }

  function_name                = aws_lambda_function.this[each.key].function_name
  maximum_retry_attempts       = 1
  maximum_event_age_in_seconds = 3600
}

# --------------------------------------------------------------------- S3 event -> scanner
resource "aws_lambda_permission" "s3_scanner" {
  count = local.enabled ? 1 : 0

  statement_id   = "AllowUploadsBucketInvoke"
  action         = "lambda:InvokeFunction"
  function_name  = aws_lambda_function.this["scanner"].function_name
  principal      = "s3.amazonaws.com"
  source_arn     = var.uploads_bucket_arn
  source_account = var.account_id
}

resource "aws_s3_bucket_notification" "uploads" {
  count = local.enabled ? 1 : 0

  bucket = var.uploads_bucket_name

  lambda_function {
    id                  = "quarantine-to-scanner"
    lambda_function_arn = aws_lambda_function.this["scanner"].arn
    events              = ["s3:ObjectCreated:*"]
    filter_prefix       = "quarantine/"
  }

  depends_on = [aws_lambda_permission.s3_scanner]
}

# ------------------------------------------------------- EventBridge -> signature updater
resource "aws_cloudwatch_event_rule" "signature_update" {
  count = local.enabled ? 1 : 0

  name                = "${var.name}-signature-update"
  description         = "docmatch ${var.env}: refresh ClamAV signatures"
  schedule_expression = var.signature_update_schedule
  state               = "ENABLED"

  tags = merge(var.tags, { Name = "${var.name}-signature-update" })
}

resource "aws_cloudwatch_event_target" "signature_update" {
  count = local.enabled ? 1 : 0

  rule      = aws_cloudwatch_event_rule.signature_update[0].name
  target_id = "signature-updater"
  arn       = aws_lambda_function.this["signature-updater"].arn

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 2
  }

  dead_letter_config {
    arn = aws_sqs_queue.lambda_dlq.arn
  }
}

resource "aws_lambda_permission" "events_signature_update" {
  count = local.enabled ? 1 : 0

  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.this["signature-updater"].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.signature_update[0].arn
}

# EventBridge needs to be allowed to write to the dead-letter queue.
data "aws_iam_policy_document" "lambda_dlq_events" {
  count = local.enabled ? 1 : 0

  source_policy_documents = [data.aws_iam_policy_document.queue_tls_only["lambda_dlq"].json]

  statement {
    sid    = "EventBridgeDeadLetter"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com"]
    }
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.lambda_dlq.arn]
    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_cloudwatch_event_rule.signature_update[0].arn]
    }
  }
}

resource "aws_sqs_queue_policy" "lambda_dlq_events" {
  count = local.enabled ? 1 : 0

  queue_url = aws_sqs_queue.lambda_dlq.id
  policy    = data.aws_iam_policy_document.lambda_dlq_events[0].json

  # The generic TLS-only policy is replaced by this superset when functions exist.
  depends_on = [aws_sqs_queue_policy.tls_only]
}
