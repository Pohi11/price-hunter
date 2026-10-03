# Each function gets only the actions it uses, on only its resources.
# DynamoDB transactions need no separate IAM action: TransactWriteItems is
# authorized by the Put/Update/Delete/ConditionCheck permissions it uses.

locals {
  table_arn = aws_dynamodb_table.main.arn
  gsi_arn   = "${aws_dynamodb_table.main.arn}/index/GSI1"
}

# --- API ---

data "aws_iam_policy_document" "api" {
  statement {
    sid = "Table"
    actions = [
      "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem",
      "dynamodb:Query", "dynamodb:ConditionCheckItem",
    ]
    resources = [local.table_arn]
  }
  statement {
    sid       = "EnqueueManualChecks"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.checks.arn]
  }
}

module "api" {
  source             = "../lambda-go"
  function_name      = "${local.name}-api"
  artifact_name      = "api"
  description        = "Price Hunter REST API"
  artifacts_bucket   = var.artifacts_bucket
  app_version        = var.app_version
  timeout            = 10
  memory_size        = 256
  environment        = local.common_env
  policy_json        = data.aws_iam_policy_document.api.json
  log_retention_days = var.log_retention_days
  alarm_topic_arn    = aws_sns_topic.alarms.arn
  tracing            = var.tracing
}

# --- Scheduler ---

data "aws_iam_policy_document" "scheduler" {
  statement {
    sid       = "QueryDue"
    actions   = ["dynamodb:Query"]
    resources = [local.gsi_arn]
  }
  statement {
    sid       = "Lease"
    actions   = ["dynamodb:UpdateItem"]
    resources = [local.table_arn]
  }
  statement {
    sid       = "Enqueue"
    actions   = ["sqs:SendMessage", "sqs:GetQueueAttributes"]
    resources = [aws_sqs_queue.checks.arn]
  }
}

module "scheduler" {
  source             = "../lambda-go"
  function_name      = "${local.name}-scheduler"
  artifact_name      = "scheduler"
  description        = "Finds due products and enqueues checks"
  artifacts_bucket   = var.artifacts_bucket
  app_version        = var.app_version
  timeout            = 60
  memory_size        = 256
  environment        = local.common_env
  policy_json        = data.aws_iam_policy_document.scheduler.json
  log_retention_days = var.log_retention_days
  alarm_topic_arn    = aws_sns_topic.alarms.arn
  # Only one scheduling pass should ever run at a time (leases make
  # overlap safe, but there's no reason to allow it).
  reserved_concurrency = 1
  tracing              = var.tracing
}

# --- Worker ---

data "aws_iam_policy_document" "worker" {
  statement {
    sid = "Table"
    actions = [
      "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:ConditionCheckItem",
    ]
    resources = [local.table_arn]
  }
  statement {
    sid       = "ConsumeChecks"
    actions   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes", "sqs:ChangeMessageVisibility"]
    resources = [aws_sqs_queue.checks.arn]
  }
  statement {
    sid       = "Snapshots"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.snapshots.arn}/snapshots/*"]
  }
}

module "worker" {
  source           = "../lambda-go"
  function_name    = "${local.name}-worker"
  artifact_name    = "worker"
  description      = "Fetches product pages and records prices"
  artifacts_bucket = var.artifacts_bucket
  app_version      = var.app_version
  timeout          = var.worker_timeout
  memory_size      = 512 # network-bound; more memory also means more CPU for HTML parsing
  environment = merge(local.common_env, {
    PH_WORKER_CONCURRENCY = tostring(var.worker_batch_concurrency)
    PH_CHECK_TIMEOUT      = "25s"
  })
  policy_json        = data.aws_iam_policy_document.worker.json
  log_retention_days = var.log_retention_days
  alarm_topic_arn    = aws_sns_topic.alarms.arn
  # Individual check failures are outcomes, not Lambda errors; any Lambda
  # error here is a crash or timeout.
  error_alarm_threshold = 3
  tracing               = var.tracing
}

resource "aws_lambda_event_source_mapping" "checks" {
  event_source_arn                   = aws_sqs_queue.checks.arn
  function_name                      = module.worker.function_arn
  batch_size                         = 10
  maximum_batching_window_in_seconds = 5
  function_response_types            = ["ReportBatchItemFailures"]

  scaling_config {
    # The global concurrency cap: protects retailers and DynamoDB, and
    # bounds per-host request rate (ADR-006).
    maximum_concurrency = var.worker_max_concurrency
  }
}
