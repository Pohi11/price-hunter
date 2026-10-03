# Dashboard, SLO alarm, symptom alarms and saved Logs Insights queries.
# Every alarm has a matching section in docs/runbook.md.

variable "observed_retailers" {
  description = "Retailer profile IDs that get a per-retailer extraction-failure alarm"
  type        = list(string)
  default     = ["generic", "demostore", "books-toscrape"]
}

locals {
  ns  = "PriceHunter"
  env = var.env

  # Metric helpers for dashboard widgets: [namespace, metric, dim, value, ...]
  checks_by_result = [for r in ["success", "extraction_failure", "retailer_error", "skipped", "duplicate"] :
  [local.ns, "ChecksCompleted", "Env", local.env, "Result", r, { stat = "Sum", label = r }]]
}

# --- SLO: 95% of due checks start within 10 minutes of their due time ---

resource "aws_cloudwatch_metric_alarm" "schedule_lag_slo" {
  alarm_name          = "${local.name}-schedule-lag-slo"
  alarm_description   = "p95 schedule lag over the last hour exceeds 10 minutes (SLO). Checks are running late. docs/runbook.md#schedule-lag"
  namespace           = local.ns
  metric_name         = "ScheduleLagSeconds"
  dimensions          = { Env = local.env }
  extended_statistic  = "p95"
  period              = 3600
  evaluation_periods  = 1
  threshold           = 600
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
  ok_actions          = [aws_sns_topic.alarms.arn]
}

# Work is waiting in SQS longer than any healthy backlog would.
resource "aws_cloudwatch_metric_alarm" "queue_age" {
  alarm_name          = "${local.name}-checks-queue-backlog"
  alarm_description   = "Oldest check message is older than 15 minutes. Workers are failing, throttled or capped. docs/runbook.md#queue-backlog"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateAgeOfOldestMessage"
  dimensions          = { QueueName = aws_sqs_queue.checks.name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 3
  threshold           = 900
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
  ok_actions          = [aws_sns_topic.alarms.arn]
}

# A spike for one retailer means its page layout changed (parser drift).
resource "aws_cloudwatch_metric_alarm" "extraction_failures" {
  for_each            = toset(var.observed_retailers)
  alarm_name          = "${local.name}-extraction-failures-${each.key}"
  alarm_description   = "Price extraction is failing for ${each.key}. The page layout probably changed. Snapshots are in S3. docs/runbook.md#parser-drift"
  namespace           = local.ns
  metric_name         = "ExtractionFailures"
  dimensions          = { Env = local.env, Retailer = each.key }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  threshold           = 5
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
}

resource "aws_cloudwatch_metric_alarm" "notification_failures" {
  alarm_name          = "${local.name}-notification-failures"
  alarm_description   = "An alert email permanently failed (bounce, unverified recipient in the SES sandbox, ...). docs/runbook.md#notification-failures"
  namespace           = local.ns
  metric_name         = "Notifications"
  dimensions          = { Env = local.env, Status = "FAILED" }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alarms.arn]
}

# --- Dashboard ---

resource "aws_cloudwatch_dashboard" "main" {
  dashboard_name = local.name
  dashboard_body = jsonencode({
    widgets = [
      {
        type       = "text", x = 0, y = 0, width = 24, height = 2,
        properties = { markdown = "## Price Hunter (${var.env})\nSLO: 95% of due checks start within 10 minutes. Alarms → SNS `${aws_sns_topic.alarms.name}`. Runbook: `docs/runbook.md`." }
      },
      {
        type = "metric", x = 0, y = 2, width = 12, height = 6,
        properties = {
          title   = "Checks by result (per 5 min)", region = var.region, view = "timeSeries", stacked = true, period = 300,
          metrics = local.checks_by_result
        }
      },
      {
        type = "metric", x = 12, y = 2, width = 12, height = 6,
        properties = {
          title = "Schedule lag (SLI)", region = var.region, view = "timeSeries", period = 300,
          metrics = [
            [local.ns, "ScheduleLagSeconds", "Env", local.env, { stat = "p50", label = "p50" }],
            ["...", { stat = "p95", label = "p95" }],
            ["...", { stat = "Maximum", label = "max" }],
          ],
          annotations = { horizontal = [{ label = "SLO 10 min", value = 600 }] }
        }
      },
      {
        type = "metric", x = 0, y = 8, width = 8, height = 6,
        properties = {
          title = "Check queue", region = var.region, view = "timeSeries", period = 60,
          metrics = [
            ["AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", aws_sqs_queue.checks.name, { stat = "Maximum", label = "visible" }],
            [".", "ApproximateNumberOfMessagesNotVisible", ".", ".", { stat = "Maximum", label = "in flight" }],
            [".", "ApproximateAgeOfOldestMessage", ".", ".", { stat = "Maximum", label = "oldest (s)", yAxis = "right" }],
          ]
        }
      },
      {
        type = "metric", x = 8, y = 8, width = 8, height = 6,
        properties = {
          title = "Dead-letter queues (should be 0)", region = var.region, view = "timeSeries", period = 300,
          metrics = [
            ["AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", aws_sqs_queue.checks_dlq.name, { stat = "Maximum", label = "checks DLQ" }],
            ["...", aws_sqs_queue.streams_dlq.name, { stat = "Maximum", label = "streams DLQ" }],
            ["...", aws_sqs_queue.scheduler_dlq.name, { stat = "Maximum", label = "scheduler DLQ" }],
          ]
        }
      },
      {
        type = "metric", x = 16, y = 8, width = 8, height = 6,
        properties = {
          title = "Worker utilization", region = var.region, view = "timeSeries", period = 60,
          metrics = [
            ["AWS/Lambda", "ConcurrentExecutions", "FunctionName", module.worker.function_name, { stat = "Maximum", label = "concurrent workers" }],
          ],
          annotations = { horizontal = [{ label = "maximum_concurrency", value = var.worker_max_concurrency }] }
        }
      },
      {
        type = "metric", x = 0, y = 14, width = 8, height = 6,
        properties = {
          title = "Retailer fetch latency (ms)", region = var.region, view = "timeSeries", period = 300,
          metrics = [
            [local.ns, "FetchLatencyMs", "Env", local.env, { stat = "p50", label = "p50" }],
            ["...", { stat = "p95", label = "p95" }],
            [local.ns, "CheckDurationMs", "Env", local.env, { stat = "p95", label = "check p95 (total)" }],
          ]
        }
      },
      {
        type = "metric", x = 8, y = 14, width = 8, height = 6,
        properties = {
          title = "Alerts and email", region = var.region, view = "timeSeries", period = 3600, stat = "Sum",
          metrics = [
            [local.ns, "AlertsTriggered", "Env", local.env, { label = "alerts triggered" }],
            [local.ns, "Notifications", "Env", local.env, "Status", "SENT", { label = "sent" }],
            ["...", "FAILED", { label = "failed" }],
            ["...", "SKIPPED", { label = "skipped (alerts off)" }],
          ]
        }
      },
      {
        type = "metric", x = 16, y = 14, width = 8, height = 6,
        properties = {
          title = "Protection mechanisms", region = var.region, view = "timeSeries", period = 3600, stat = "Sum",
          metrics = [
            [local.ns, "CircuitOpened", "Env", local.env, { label = "circuits opened" }],
            [local.ns, "SuspectPrices", "Env", local.env, { label = "suspect prices held" }],
            [local.ns, "StrategyDisagreements", "Env", local.env, { label = "strategy disagreements" }],
            [local.ns, "SchedulerBackpressureSkips", "Env", local.env, { label = "backpressure skips" }],
          ]
        }
      },
      {
        type = "metric", x = 0, y = 20, width = 12, height = 6,
        properties = {
          title = "Lambda errors and throttles", region = var.region, view = "timeSeries", period = 300, stat = "Sum",
          metrics = concat(
            [for k, fn in { api = module.api.function_name, scheduler = module.scheduler.function_name, worker = module.worker.function_name, streams = module.streams.function_name } :
            ["AWS/Lambda", "Errors", "FunctionName", fn, { label = "${k} errors" }]],
            [["AWS/Lambda", "Throttles", "FunctionName", module.worker.function_name, { label = "worker throttles" }]]
          )
        }
      },
      {
        type = "metric", x = 12, y = 20, width = 12, height = 6,
        properties = {
          title = "API", region = var.region, view = "timeSeries", period = 300,
          metrics = [
            ["AWS/ApiGateway", "Count", "ApiId", aws_apigatewayv2_api.main.id, "Stage", "$default", { stat = "Sum", label = "requests" }],
            [".", "4xx", ".", ".", ".", ".", { stat = "Sum", label = "4xx" }],
            [".", "5xx", ".", ".", ".", ".", { stat = "Sum", label = "5xx" }],
            [".", "Latency", ".", ".", ".", ".", { stat = "p95", label = "latency p95 (ms)", yAxis = "right" }],
          ]
        }
      },
    ]
  })
}

# --- Saved Logs Insights queries (CloudWatch → Logs Insights → Queries) ---

resource "aws_cloudwatch_query_definition" "failed_checks" {
  name            = "${local.name}/failed-checks-by-outcome"
  log_group_names = [module.worker.log_group_name]
  query_string    = <<-EOT
    fields @timestamp, outcome, retailer, product_id, check_id
    | filter msg = "check completed" and outcome != "OK"
    | stats count(*) as failures by outcome, retailer
    | sort failures desc
  EOT
}

resource "aws_cloudwatch_query_definition" "slow_checks" {
  name            = "${local.name}/slowest-checks"
  log_group_names = [module.worker.log_group_name]
  query_string    = <<-EOT
    fields @timestamp, total_ms, fetch_ms, retailer, outcome, product_id
    | filter msg = "check completed"
    | sort total_ms desc
    | limit 25
  EOT
}

resource "aws_cloudwatch_query_definition" "trace_check" {
  name            = "${local.name}/trace-one-check"
  log_group_names = [module.api.log_group_name, module.scheduler.log_group_name, module.worker.log_group_name, module.streams.log_group_name]
  query_string    = <<-EOT
    # Replace the ID, then run: every log line for one check, across functions.
    fields @timestamp, @log, level, msg, outcome, err
    | filter check_id = "REPLACE_WITH_CHECK_ID"
    | sort @timestamp asc
  EOT
}

resource "aws_cloudwatch_query_definition" "errors" {
  name            = "${local.name}/errors"
  log_group_names = [module.api.log_group_name, module.scheduler.log_group_name, module.worker.log_group_name, module.streams.log_group_name]
  query_string    = <<-EOT
    fields @timestamp, @log, msg, err, request_id, check_id
    | filter level = "ERROR"
    | sort @timestamp desc
    | limit 50
  EOT
}

output "dashboard_url" {
  value = "https://${var.region}.console.aws.amazon.com/cloudwatch/home?region=${var.region}#dashboards/dashboard/${aws_cloudwatch_dashboard.main.dashboard_name}"
}
