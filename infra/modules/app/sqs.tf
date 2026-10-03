# Check queue + DLQ. Standard (not FIFO): ordering doesn't matter and the
# consumer is idempotent (ADR-006).

resource "aws_sqs_queue" "checks_dlq" {
  name                      = "${local.name}-checks-dlq"
  message_retention_seconds = 1209600 # 14 days to investigate and redrive
  sqs_managed_sse_enabled   = true
}

resource "aws_sqs_queue" "checks" {
  name = "${local.name}-checks"
  # AWS guidance: at least 6x the consumer function timeout, so a message
  # isn't redelivered while a slow batch is still running.
  visibility_timeout_seconds = 6 * var.worker_timeout
  message_retention_seconds  = 345600 # 4 days
  receive_wait_time_seconds  = 20
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.checks_dlq.arn
    maxReceiveCount     = 5
  })
}

resource "aws_sqs_queue_redrive_allow_policy" "checks_dlq" {
  queue_url = aws_sqs_queue.checks_dlq.id
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.checks.arn]
  })
}

# Failed scheduler invocations (after EventBridge Scheduler's retries).
resource "aws_sqs_queue" "scheduler_dlq" {
  name                      = "${local.name}-scheduler-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
}

# A message in any DLQ means a bug or an outage, never a retailer hiccup
# (those are recorded as check outcomes), so any depth > 0 pages.
resource "aws_cloudwatch_metric_alarm" "dlq" {
  for_each = {
    checks    = aws_sqs_queue.checks_dlq.name
    scheduler = aws_sqs_queue.scheduler_dlq.name
  }
  alarm_name          = "${local.name}-${each.key}-dlq-not-empty"
  alarm_description   = "Messages in ${each.value}. Inspect, fix, then redrive (docs/runbook.md#dlq)."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = each.value }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
  ok_actions          = [aws_sns_topic.alarms.arn]
}
