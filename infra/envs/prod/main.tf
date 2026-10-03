# Price Hunter — prod environment.
#
#   terraform init -backend-config=backend.hcl
#   terraform apply -var app_version=<git-sha>

terraform {
  required_version = ">= 1.10"
  required_providers {
    aws    = { source = "hashicorp/aws", version = ">= 6.0" }
    random = { source = "hashicorp/random", version = ">= 3.6" }
  }
  # Partial configuration: bucket/region come from backend.hcl (see
  # backend.hcl.example) because they contain the AWS account ID.
  backend "s3" {
    key          = "price-hunter/prod.tfstate"
    use_lockfile = true # S3-native state locking (Terraform >= 1.10)
    encrypt      = true
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "price-hunter", Environment = "prod", ManagedBy = "terraform" }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}
variable "artifacts_bucket" { type = string }
variable "app_version" { type = string }
variable "alarm_email" {
  type    = string
  default = ""
}
variable "email_from" {
  description = "Alert sender; must be verified in SES (empty disables email)"
  type        = string
  default     = ""
}

module "app" {
  source           = "../../modules/app"
  env              = "prod"
  region           = var.region
  artifacts_bucket = var.artifacts_bucket
  app_version      = var.app_version
  alarm_email      = var.alarm_email
  email_from       = var.email_from

  log_retention_days     = 30
  worker_max_concurrency = 10
  point_in_time_recovery = true
  deletion_protection    = true
  allow_generic          = false # allowlist mode for a public deployment
  api_throttle_rate      = 50
  api_throttle_burst     = 100
}

output "app" { value = module.app }
