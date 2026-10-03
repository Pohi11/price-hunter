terraform {
  required_version = ">= 1.10"
  required_providers {
    aws    = { source = "hashicorp/aws", version = ">= 6.0" }
    random = { source = "hashicorp/random", version = ">= 3.6" }
  }
}

data "aws_caller_identity" "current" {}

locals {
  name    = "pricehunter-${var.env}"
  account = data.aws_caller_identity.current.account_id
  web_url = "https://${aws_cloudfront_distribution.web.domain_name}"

  # Environment shared by every function. Each function adds its own.
  common_env = {
    PH_ENV             = var.env
    PH_LOG_LEVEL       = "info"
    PH_TABLE           = aws_dynamodb_table.main.name
    PH_CHECK_QUEUE_URL = aws_sqs_queue.checks.url
    PH_SNAPSHOT_BUCKET = aws_s3_bucket.snapshots.bucket
    PH_ALLOW_GENERIC   = tostring(var.allow_generic)
    PH_DEMOSTORE_HOST  = local.demostore_host
    PH_MAX_PRODUCTS    = tostring(var.max_products_per_user)
    PH_APP_BASE_URL    = local.web_url
    PH_EMAIL_MODE      = "log"
    PH_METRICS         = "true"
  }
}

resource "random_id" "suffix" {
  byte_length = 3
}

# --- Ops notifications ---

resource "aws_sns_topic" "alarms" {
  name = "${local.name}-alarms"
}

resource "aws_sns_topic_subscription" "alarm_email" {
  count     = var.alarm_email == "" ? 0 : 1
  topic_arn = aws_sns_topic.alarms.arn
  protocol  = "email"
  endpoint  = var.alarm_email
}
