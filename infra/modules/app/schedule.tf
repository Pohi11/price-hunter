# EventBridge Scheduler invokes the scheduler Lambda on a fixed rate.
data "aws_iam_policy_document" "scheduler_invoke_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account]
    }
  }
}

resource "aws_iam_role" "scheduler_invoke" {
  name               = "${local.name}-scheduler-invoke"
  assume_role_policy = data.aws_iam_policy_document.scheduler_invoke_assume.json
}

data "aws_iam_policy_document" "scheduler_invoke" {
  statement {
    actions   = ["lambda:InvokeFunction"]
    resources = [module.scheduler.function_arn]
  }
  statement {
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.scheduler_dlq.arn]
  }
}

resource "aws_iam_role_policy" "scheduler_invoke" {
  name   = "invoke"
  role   = aws_iam_role.scheduler_invoke.id
  policy = data.aws_iam_policy_document.scheduler_invoke.json
}

resource "aws_scheduler_schedule" "scheduling_pass" {
  name                = "${local.name}-scheduling-pass"
  schedule_expression = var.scheduler_rate

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = module.scheduler.function_arn
    role_arn = aws_iam_role.scheduler_invoke.arn

    retry_policy {
      maximum_retry_attempts       = 2
      maximum_event_age_in_seconds = 300 # a stale pass is pointless; the next one is 5 minutes away
    }

    dead_letter_config {
      arn = aws_sqs_queue.scheduler_dlq.arn
    }
  }
}

# Dead man's switch: if the scheduler stops running, no product is ever
# checked again, and nothing else would notice.
resource "aws_cloudwatch_metric_alarm" "scheduler_not_running" {
  alarm_name          = "${local.name}-scheduler-not-running"
  alarm_description   = "The scheduler Lambda has not been invoked in 15 minutes. Checks have stopped."
  namespace           = "AWS/Lambda"
  metric_name         = "Invocations"
  dimensions          = { FunctionName = module.scheduler.function_name }
  statistic           = "Sum"
  period              = 900
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
  ok_actions          = [aws_sns_topic.alarms.arn]
}
