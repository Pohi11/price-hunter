# A Go Lambda (provided.al2023, arm64) with its own role, log group and
# error/throttle alarms. Code comes from an immutable zip in the artifacts
# bucket: s3://<artifacts>/<version>/<name>.zip

terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.0" }
  }
}

variable "function_name" { type = string }
variable "artifact_name" {
  description = "Zip name in the artifacts bucket, without .zip (e.g. worker)"
  type        = string
}
variable "artifacts_bucket" { type = string }
variable "app_version" { type = string }
variable "description" {
  type    = string
  default = ""
}
variable "timeout" {
  type    = number
  default = 30
}
variable "memory_size" {
  type    = number
  default = 256
}
variable "environment" {
  type    = map(string)
  default = {}
}
variable "policy_json" {
  description = "IAM policy document for the function's own permissions (logs are added automatically)"
  type        = string
}
variable "log_retention_days" {
  type    = number
  default = 14
}
variable "reserved_concurrency" {
  description = "-1 for unreserved"
  type        = number
  default     = -1
}
variable "alarm_topic_arn" {
  type    = string
  default = ""
}
variable "error_alarm_threshold" {
  description = "Errors per 5 minutes that trigger the alarm"
  type        = number
  default     = 1
}
variable "tracing" {
  description = "Enable X-Ray active tracing"
  type        = bool
  default     = false
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "this" {
  name               = var.function_name
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

# Created explicitly so retention is managed (Lambda would otherwise create
# a log group that keeps logs forever).
resource "aws_cloudwatch_log_group" "this" {
  name              = "/aws/lambda/${var.function_name}"
  retention_in_days = var.log_retention_days
}

data "aws_iam_policy_document" "logs" {
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.this.arn}:*"]
  }
}

resource "aws_iam_role_policy" "logs" {
  name   = "logs"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.logs.json
}

resource "aws_iam_role_policy" "app" {
  name   = "app"
  role   = aws_iam_role.this.id
  policy = var.policy_json
}

resource "aws_iam_role_policy_attachment" "xray" {
  count      = var.tracing ? 1 : 0
  role       = aws_iam_role.this.name
  policy_arn = "arn:aws:iam::aws:policy/AWSXrayWriteOnlyAccess"
}

resource "aws_lambda_function" "this" {
  function_name = var.function_name
  description   = var.description
  role          = aws_iam_role.this.arn
  runtime       = "provided.al2023"
  handler       = "bootstrap"
  architectures = ["arm64"]
  s3_bucket     = var.artifacts_bucket
  s3_key        = "${var.app_version}/${var.artifact_name}.zip"
  timeout       = var.timeout
  memory_size   = var.memory_size

  reserved_concurrent_executions = var.reserved_concurrency

  environment {
    variables = merge(var.environment, { PH_VERSION = var.app_version })
  }

  logging_config {
    log_format = "Text" # the app already writes JSON lines via slog
    log_group  = aws_cloudwatch_log_group.this.name
  }

  tracing_config {
    mode = var.tracing ? "Active" : "PassThrough"
  }

  depends_on = [aws_iam_role_policy.logs]
}

resource "aws_cloudwatch_metric_alarm" "errors" {
  count               = var.alarm_topic_arn == "" ? 0 : 1
  alarm_name          = "${var.function_name}-errors"
  alarm_description   = "Lambda ${var.function_name} returned errors (crashes, timeouts, init failures). See docs/runbook.md."
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  dimensions          = { FunctionName = aws_lambda_function.this.function_name }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = var.error_alarm_threshold
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [var.alarm_topic_arn]
  ok_actions          = [var.alarm_topic_arn]
}

resource "aws_cloudwatch_metric_alarm" "throttles" {
  count               = var.alarm_topic_arn == "" ? 0 : 1
  alarm_name          = "${var.function_name}-throttles"
  alarm_description   = "Lambda ${var.function_name} is being throttled (account or reserved concurrency limit)."
  namespace           = "AWS/Lambda"
  metric_name         = "Throttles"
  dimensions          = { FunctionName = aws_lambda_function.this.function_name }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 3
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [var.alarm_topic_arn]
}

output "function_name" { value = aws_lambda_function.this.function_name }
output "function_arn" { value = aws_lambda_function.this.arn }
output "invoke_arn" { value = aws_lambda_function.this.invoke_arn }
output "role_name" { value = aws_iam_role.this.name }
output "role_arn" { value = aws_iam_role.this.arn }
output "log_group_name" { value = aws_cloudwatch_log_group.this.name }
