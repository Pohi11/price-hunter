# One-time, per-AWS-account setup. Run locally with admin credentials:
#
#   cd infra/bootstrap
#   terraform init
#   terraform apply -var 'github_repo=Pohi11/price-hunter' -var 'environments=["dev","prod"]' -var 'budget_email=you@example.com'
#
# It creates what every other stack depends on, so it uses local state
# (keep terraform.tfstate safe, or migrate it into the bucket it creates).

terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.0" }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "price-hunter", ManagedBy = "terraform", Stack = "bootstrap" }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "github_repo" {
  description = "owner/name of the GitHub repository allowed to deploy"
  type        = string
}

variable "environments" {
  description = "GitHub environments that may deploy to this account (e.g. [\"dev\"] or [\"prod\"])"
  type        = list(string)
}

variable "budget_email" {
  description = "Receives AWS Budgets alerts"
  type        = string
}

variable "monthly_budget_usd" {
  type    = number
  default = 10
}

data "aws_caller_identity" "current" {}

locals {
  account = data.aws_caller_identity.current.account_id
}

# --- Terraform state (S3-native locking via use_lockfile; no DynamoDB lock table) ---

resource "aws_s3_bucket" "tfstate" {
  bucket = "pricehunter-tfstate-${local.account}"
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "tfstate" {
  bucket                  = aws_s3_bucket.tfstate.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# --- Build artifacts: immutable Lambda zips keyed by git SHA ---

resource "aws_s3_bucket" "artifacts" {
  bucket = "pricehunter-artifacts-${local.account}"
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket                  = aws_s3_bucket.artifacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    id     = "expire-old-builds"
    status = "Enabled"
    filter {}
    expiration {
      days = 180 # keep half a year of rollback targets
    }
  }
}

# --- GitHub Actions OIDC: CI gets short-lived credentials, no stored keys ---

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

data "aws_iam_policy_document" "plan_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${var.github_repo}:pull_request", "repo:${var.github_repo}:ref:refs/heads/main"]
    }
  }
}

# Pull requests may run `terraform plan`: read-only AWS access plus the
# state lock file.
resource "aws_iam_role" "ci_plan" {
  name               = "pricehunter-ci-plan"
  assume_role_policy = data.aws_iam_policy_document.plan_trust.json
}

resource "aws_iam_role_policy_attachment" "ci_plan_readonly" {
  role       = aws_iam_role.ci_plan.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

data "aws_iam_policy_document" "state_access" {
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.tfstate.arn]
  }
  statement {
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${aws_s3_bucket.tfstate.arn}/*"]
  }
}

resource "aws_iam_role_policy" "ci_plan_state" {
  name   = "terraform-state"
  role   = aws_iam_role.ci_plan.id
  policy = data.aws_iam_policy_document.state_access.json
}

data "aws_iam_policy_document" "deploy_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    # Only jobs bound to a protected GitHub environment may deploy. The prod
    # environment requires a manual approval in GitHub.
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = [for e in var.environments : "repo:${var.github_repo}:environment:${e}"]
    }
  }
}

# Terraform manages IAM, Lambda, DynamoDB, CloudFront, Cognito, ... so the
# deploy role is broad. It is constrained by *who* can assume it (protected
# environments only, short-lived OIDC sessions) rather than by what it can
# do. A scoped deploy policy is a documented follow-up.
resource "aws_iam_role" "ci_deploy" {
  name                 = "pricehunter-ci-deploy"
  assume_role_policy   = data.aws_iam_policy_document.deploy_trust.json
  max_session_duration = 3600
}

resource "aws_iam_role_policy_attachment" "ci_deploy_admin" {
  role       = aws_iam_role.ci_deploy.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

# --- Cost guardrail ---

resource "aws_budgets_budget" "monthly" {
  name         = "pricehunter-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.budget_email]
  }
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_email]
  }
}

output "state_bucket" { value = aws_s3_bucket.tfstate.bucket }
output "artifacts_bucket" { value = aws_s3_bucket.artifacts.bucket }
output "ci_plan_role_arn" { value = aws_iam_role.ci_plan.arn }
output "ci_deploy_role_arn" { value = aws_iam_role.ci_deploy.arn }
