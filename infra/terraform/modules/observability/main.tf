# Observability within the free tier: one multi-region CloudTrail trail to S3, one AWS Budget
# with 80%/100% notifications, a handful of CloudWatch alarms (the free tier covers ten) on an
# SNS topic with an e-mail subscription, and the application log group.

locals {
  budget_limit = max(var.budget_floor_usd, var.budget_fraction * var.mrr_usd)
}

# -------------------------------------------------------------------------------- alerts
resource "aws_sns_topic" "alerts" {
  name = "${var.name}-alerts"
  tags = merge(var.tags, { Name = "${var.name}-alerts" })
}

# Budgets and CloudWatch publish from their service principals. The topic is not KMS-encrypted on
# purpose: CloudWatch alarms cannot publish to topics encrypted with the AWS managed key, and a
# customer key would add a monthly charge for notifications that carry no secret.
data "aws_iam_policy_document" "alerts" {
  statement {
    sid    = "AwsServicesPublish"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["budgets.amazonaws.com", "cloudwatch.amazonaws.com"]
    }
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.alerts.arn]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.account_id]
    }
  }
}

resource "aws_sns_topic_policy" "alerts" {
  arn    = aws_sns_topic.alerts.arn
  policy = data.aws_iam_policy_document.alerts.json
}

resource "aws_sns_topic_subscription" "email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# ----------------------------------------------------------------------------- CloudTrail
resource "aws_cloudtrail" "this" {
  name                          = var.cloudtrail_name
  s3_bucket_name                = var.logs_bucket_name
  s3_key_prefix                 = "cloudtrail"
  is_multi_region_trail         = true
  include_global_service_events = true
  enable_log_file_validation    = true
  enable_logging                = true

  event_selector {
    read_write_type           = "All"
    include_management_events = true
  }

  tags = merge(var.tags, { Name = var.cloudtrail_name })
}

# -------------------------------------------------------------------------------- budget
resource "aws_budgets_budget" "monthly" {
  name         = "${var.name}-mensal"
  budget_type  = "COST"
  time_unit    = "MONTHLY"
  limit_amount = format("%.2f", local.budget_limit)
  limit_unit   = "USD"

  dynamic "cost_filter" {
    for_each = var.budget_project_tag != "" ? [1] : []
    content {
      name   = "TagKeyValue"
      values = [format("user:Project$%s", var.budget_project_tag)]
    }
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
    subscriber_sns_topic_arns  = [aws_sns_topic.alerts.arn]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
    subscriber_sns_topic_arns  = [aws_sns_topic.alerts.arn]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
    subscriber_sns_topic_arns  = [aws_sns_topic.alerts.arn]
  }

  tags = merge(var.tags, { Name = "${var.name}-mensal" })

  depends_on = [aws_sns_topic_policy.alerts]
}

# Pool of on-demand workers (docs/escala.md): its own monthly budget, on the instances, volumes
# and addresses tagged docmatch:pool=burst. The pool also caps itself (DM_BURST_MONTHLY_HOURS,
# counted in the database); this alarm is the account's view of the same money. The tag key must
# be activated once as a cost allocation tag (infra/README.md, docs/runbooks/burst.md).
resource "aws_budgets_budget" "burst" {
  count = var.burst_budget_usd > 0 ? 1 : 0

  name         = "${var.name}-pool-sob-demanda"
  budget_type  = "COST"
  time_unit    = "MONTHLY"
  limit_amount = format("%.2f", var.burst_budget_usd)
  limit_unit   = "USD"

  cost_filter {
    name   = "TagKeyValue"
    values = [format("user:%s$%s", var.burst_tag_key, var.burst_tag_value)]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
    subscriber_sns_topic_arns  = [aws_sns_topic.alerts.arn]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
    subscriber_sns_topic_arns  = [aws_sns_topic.alerts.arn]
  }

  tags = merge(var.tags, { Name = "${var.name}-pool-sob-demanda" })

  depends_on = [aws_sns_topic_policy.alerts]
}

# ------------------------------------------------------------------------------- alarms
resource "aws_cloudwatch_metric_alarm" "app_status_check" {
  count = var.app_alarms_enabled ? 1 : 0

  alarm_name          = "${var.name}-app-status-check"
  alarm_description   = "docmatch ${var.env}: instance or system status check failing"
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed"
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 3
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "breaching"

  dimensions = {
    AutoScalingGroupName = var.app_autoscaling_group_name
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
  tags          = merge(var.tags, { Name = "${var.name}-app-status-check" })
}

resource "aws_cloudwatch_metric_alarm" "app_cpu" {
  count = var.app_alarms_enabled ? 1 : 0

  alarm_name          = "${var.name}-app-cpu-high"
  alarm_description   = "docmatch ${var.env}: CPU above 90% for 15 minutes"
  namespace           = "AWS/EC2"
  metric_name         = "CPUUtilization"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 3
  threshold           = 90
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    AutoScalingGroupName = var.app_autoscaling_group_name
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
  tags          = merge(var.tags, { Name = "${var.name}-app-cpu-high" })
}

resource "aws_cloudwatch_metric_alarm" "app_in_service" {
  count = var.app_alarms_enabled ? 1 : 0

  alarm_name          = "${var.name}-app-not-in-service"
  alarm_description   = "docmatch ${var.env}: no healthy application instance in service"
  namespace           = "AWS/AutoScaling"
  metric_name         = "GroupInServiceInstances"
  statistic           = "Minimum"
  period              = 60
  evaluation_periods  = 5
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "breaching"

  dimensions = {
    AutoScalingGroupName = var.app_autoscaling_group_name
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
  tags          = merge(var.tags, { Name = "${var.name}-app-not-in-service" })
}

# One alarm for every sandbox function: the sum of their errors over five minutes.
resource "aws_cloudwatch_metric_alarm" "lambda_errors" {
  count = length(var.lambda_function_names) > 0 ? 1 : 0

  alarm_name          = "${var.name}-sandbox-errors"
  alarm_description   = "docmatch ${var.env}: sandbox function invocations failing"
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  dynamic "metric_query" {
    for_each = { for i, fn in var.lambda_function_names : "e${i}" => fn }
    content {
      id          = metric_query.key
      return_data = false
      metric {
        namespace   = "AWS/Lambda"
        metric_name = "Errors"
        stat        = "Sum"
        period      = 300
        dimensions = {
          FunctionName = metric_query.value
        }
      }
    }
  }

  metric_query {
    id          = "total"
    expression  = "SUM([${join(",", [for i, _ in var.lambda_function_names : "e${i}"])}])"
    label       = "sandbox errors"
    return_data = true
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
  tags          = merge(var.tags, { Name = "${var.name}-sandbox-errors" })
}

# Keyed by the static map key, not by the queue name (a resource attribute).
resource "aws_cloudwatch_metric_alarm" "dead_letter" {
  for_each = var.dead_letter_queue_names

  alarm_name          = "${var.name}-dlq-${each.key}"
  alarm_description   = "docmatch ${var.env}: messages in dead-letter queue ${each.value}"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    QueueName = each.value
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
  tags          = merge(var.tags, { Name = "${var.name}-dlq-${each.key}" })
}

resource "aws_cloudwatch_metric_alarm" "cloudfront_5xx" {
  count = var.cloudfront_alarm_enabled ? 1 : 0

  alarm_name          = "${var.name}-edge-5xx-rate"
  alarm_description   = "docmatch ${var.env}: more than 5% of edge responses are 5xx"
  namespace           = "AWS/CloudFront"
  metric_name         = "5xxErrorRate"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  threshold           = 5
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    DistributionId = var.cloudfront_distribution_id
    Region         = "Global"
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
  tags          = merge(var.tags, { Name = "${var.name}-edge-5xx-rate" })
}

# ----------------------------------------------------------------------------- log groups
resource "aws_cloudwatch_log_group" "app" {
  name              = "/docmatch/${var.env}/app"
  retention_in_days = var.log_retention_days

  tags = merge(var.tags, { Name = "${var.name}-app-logs" })
}
