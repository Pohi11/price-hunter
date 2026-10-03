# Price Hunter — dev environment.
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
    key          = "price-hunter/dev.tfstate"
    use_lockfile = true # S3-native state locking (Terraform >= 1.10)
    encrypt      = true
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "price-hunter", Environment = "dev", ManagedBy = "terraform" }
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
variable "demostore_host" {
  type    = string
  default = ""
}

module "app" {
  source           = "../../modules/app"
  env              = "dev"
  region           = var.region
  artifacts_bucket = var.artifacts_bucket
  app_version      = var.app_version
  alarm_email      = var.alarm_email
  email_from       = var.email_from

  log_retention_days     = 14
  worker_max_concurrency = 3
  point_in_time_recovery = false
  deletion_protection    = false
  allow_generic          = true
  deploy_demostore       = true
  demostore_host         = var.demostore_host
  # Lets `npm run dev` on localhost sign in against the dev user pool.
  extra_callback_urls = ["http://localhost:5173/"]
  # CI signs in a dedicated smoke-test user with IAM-authenticated admin auth.
  allow_admin_password_auth = true
}

output "app" { value = module.app }
