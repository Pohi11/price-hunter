variable "env" {
  description = "dev or prod"
  type        = string
  validation {
    condition     = contains(["dev", "prod"], var.env)
    error_message = "env must be dev or prod."
  }
}

variable "region" { type = string }

variable "artifacts_bucket" {
  description = "Bucket holding Lambda zips (from infra/bootstrap)"
  type        = string
}

variable "app_version" {
  description = "Build to deploy: artifacts are read from s3://<artifacts_bucket>/<app_version>/<fn>.zip"
  type        = string
}

variable "log_retention_days" {
  type    = number
  default = 14
}

variable "alarm_email" {
  description = "Receives CloudWatch alarm notifications (confirm the SNS subscription email)"
  type        = string
  default     = ""
}

# --- Workers ---

variable "worker_max_concurrency" {
  description = "Max concurrent worker Lambdas (SQS event source maximum_concurrency, >= 2). Bounds load on retailers and DynamoDB."
  type        = number
  default     = 5
  validation {
    condition     = var.worker_max_concurrency >= 2 && var.worker_max_concurrency <= 1000
    error_message = "maximum_concurrency must be between 2 and 1000."
  }
}

variable "worker_batch_concurrency" {
  description = "Messages processed in parallel inside one worker invocation"
  type        = number
  default     = 5
}

variable "worker_timeout" {
  type    = number
  default = 60
}

variable "scheduler_rate" {
  description = "EventBridge Scheduler expression for the scheduling pass"
  type        = string
  default     = "rate(5 minutes)"
}

# --- Product behaviour ---

variable "allow_generic" {
  description = "Accept retailers without a profile (structured data only). false = allowlist mode."
  type        = bool
  default     = true
}

variable "max_products_per_user" {
  type    = number
  default = 50
}

variable "deploy_demostore" {
  description = "Deploy the fictional demo retailer as a public Lambda Function URL (dev)"
  type        = bool
  default     = false
}

variable "demostore_host" {
  description = "Hostname of a deployed demo store to treat as the demostore profile (dev only)"
  type        = string
  default     = ""
}

# --- Email ---

variable "email_from" {
  description = "Sender address for alert emails (e.g. alerts@example.com). Empty = log-only, no SES."
  type        = string
  default     = ""
}

variable "email_domain" {
  description = "Verify this whole domain with DKIM instead of the single sender address"
  type        = string
  default     = ""
}

# --- Data protection ---

variable "point_in_time_recovery" {
  type    = bool
  default = false
}

variable "deletion_protection" {
  type    = bool
  default = false
}

# --- Auth ---

variable "self_signup" {
  description = "Allow anyone to register. Off by default: create users with the AWS CLI."
  type        = bool
  default     = false
}

variable "allow_admin_password_auth" {
  description = "Allow ADMIN_USER_PASSWORD_AUTH so CI (with IAM credentials) can sign in a smoke-test user. Dev only."
  type        = bool
  default     = false
}

variable "extra_callback_urls" {
  description = "Additional OAuth redirect URLs, e.g. http://localhost:5173/ for local UI against dev"
  type        = list(string)
  default     = []
}

# --- API ---

variable "api_throttle_rate" {
  description = "Steady-state requests/second for the whole API stage"
  type        = number
  default     = 20
}

variable "api_throttle_burst" {
  type    = number
  default = 40
}

variable "tracing" {
  description = "X-Ray active tracing on all functions"
  type        = bool
  default     = false
}
