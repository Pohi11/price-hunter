# Alert email: SES identity + configuration set, and the DynamoDB Streams
# consumer that delivers outbox notifications (ADR-004).
#
# SES starts in the sandbox: it only delivers to verified addresses, which
# is fine for personal use. Request production access before inviting
# other users. With email_domain set, publish the DKIM CNAMEs from the
# `ses_dkim_records` output to authenticate mail (SPF/DMARC alignment).

locals {
  email_enabled = var.email_from != ""
}

resource "aws_sesv2_email_identity" "sender" {
  count          = local.email_enabled ? 1 : 0
  email_identity = var.email_domain != "" ? var.email_domain : var.email_from
}

resource "aws_sesv2_configuration_set" "main" {
  count                  = local.email_enabled ? 1 : 0
  configuration_set_name = local.name

  reputation_options {
    reputation_metrics_enabled = true
  }
  suppression_options {
    # Never retry addresses that hard-bounced or complained.
    suppressed_reasons = ["BOUNCE", "COMPLAINT"]
  }
  delivery_options {
    tls_policy = "REQUIRE"
  }
}

resource "aws_sesv2_configuration_set_event_destination" "metrics" {
  count                  = local.email_enabled ? 1 : 0
  configuration_set_name = aws_sesv2_configuration_set.main[0].configuration_set_name
  event_destination_name = "cloudwatch"
  event_destination {
    enabled              = true
    matching_event_types = ["SEND", "DELIVERY", "BOUNCE", "COMPLAINT", "REJECT"]
    cloud_watch_destination {
      dimension_configuration {
        default_dimension_value = local.name
        dimension_name          = "ConfigurationSet"
        dimension_value_source  = "MESSAGE_TAG"
      }
    }
  }
}

# Sustained bounces put the SES account at risk of suspension.
resource "aws_cloudwatch_metric_alarm" "ses_bounces" {
  count               = local.email_enabled ? 1 : 0
  alarm_name          = "${local.name}-email-bounces"
  alarm_description   = "Alert emails are bouncing. Check recipient addresses and the SES suppression list."
  namespace           = "AWS/SES"
  metric_name         = "Bounce"
  dimensions          = { ConfigurationSet = local.name }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  threshold           = 3
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
}

# --- Streams consumer ---

resource "aws_sqs_queue" "streams_dlq" {
  name                      = "${local.name}-streams-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
}

resource "aws_cloudwatch_metric_alarm" "streams_dlq" {
  alarm_name          = "${local.name}-streams-dlq-not-empty"
  alarm_description   = "A stream batch exhausted retries: a notification may be undelivered or a deleted product unpurged. The record pointer is in the message (docs/runbook.md#streams-dlq)."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = aws_sqs_queue.streams_dlq.name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
}

data "aws_iam_policy_document" "streams" {
  statement {
    sid       = "ReadStream"
    actions   = ["dynamodb:DescribeStream", "dynamodb:GetRecords", "dynamodb:GetShardIterator", "dynamodb:ListStreams"]
    resources = [aws_dynamodb_table.main.stream_arn]
  }
  statement {
    sid = "OutboxAndPurge"
    actions = [
      "dynamodb:GetItem", "dynamodb:UpdateItem", "dynamodb:Query", "dynamodb:BatchWriteItem", "dynamodb:DeleteItem",
    ]
    resources = [local.table_arn]
  }
  statement {
    sid       = "FailureDestination"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.streams_dlq.arn]
  }
  dynamic "statement" {
    for_each = local.email_enabled ? [1] : []
    content {
      sid       = "SendAlerts"
      actions   = ["ses:SendEmail"]
      resources = [aws_sesv2_email_identity.sender[0].arn, aws_sesv2_configuration_set.main[0].arn]
      condition {
        test     = "StringEquals"
        variable = "ses:FromAddress"
        values   = [var.email_from]
      }
    }
  }
}

module "streams" {
  source           = "../lambda-go"
  function_name    = "${local.name}-streams"
  artifact_name    = "streams"
  description      = "Delivers alert emails from the outbox; purges deleted products"
  artifacts_bucket = var.artifacts_bucket
  app_version      = var.app_version
  timeout          = 30
  memory_size      = 256
  environment = merge(local.common_env, {
    PH_EMAIL_MODE            = local.email_enabled ? "ses" : "log"
    PH_EMAIL_FROM            = var.email_from
    PH_SES_CONFIGURATION_SET = local.email_enabled ? aws_sesv2_configuration_set.main[0].configuration_set_name : ""
  })
  policy_json        = data.aws_iam_policy_document.streams.json
  log_retention_days = var.log_retention_days
  alarm_topic_arn    = aws_sns_topic.alarms.arn
  tracing            = var.tracing
}

resource "aws_lambda_event_source_mapping" "streams" {
  event_source_arn               = aws_dynamodb_table.main.stream_arn
  function_name                  = module.streams.function_arn
  starting_position              = "LATEST"
  batch_size                     = 25
  maximum_retry_attempts         = 3
  maximum_record_age_in_seconds  = 3600
  bisect_batch_on_function_error = true
  function_response_types        = ["ReportBatchItemFailures"]

  # Only invoke for the two changes we react to. TTL expiries, price
  # points, checks and product updates never cost an invocation.
  filter_criteria {
    filter {
      pattern = jsonencode({ eventName = ["INSERT"], dynamodb = { NewImage = { entity = { S = ["Notification"] } } } })
    }
    filter {
      pattern = jsonencode({ eventName = ["REMOVE"], dynamodb = { OldImage = { entity = { S = ["Product"] } } } })
    }
  }

  destination_config {
    on_failure {
      destination_arn = aws_sqs_queue.streams_dlq.arn
    }
  }
}

output "ses_dkim_records" {
  description = "CNAME records to publish when email_domain is set"
  value = local.email_enabled && var.email_domain != "" ? [
    for t in aws_sesv2_email_identity.sender[0].dkim_signing_attributes[0].tokens :
    { name = "${t}._domainkey.${var.email_domain}", type = "CNAME", value = "${t}.dkim.amazonses.com" }
  ] : []
}
