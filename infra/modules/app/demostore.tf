# Optional demo retailer (dev): a public Lambda Function URL serving
# fictional products. Workers treat its host as the "demostore" profile.

resource "random_password" "demostore_admin" {
  count   = var.deploy_demostore ? 1 : 0
  length  = 32
  special = false
}

data "aws_iam_policy_document" "demostore" {
  # No AWS permissions needed beyond logs (added by the module).
  statement {
    sid       = "None"
    effect    = "Deny"
    actions   = ["*"]
    resources = ["*"]
  }
}

module "demostore" {
  count              = var.deploy_demostore ? 1 : 0
  source             = "../lambda-go"
  function_name      = "${local.name}-demostore"
  artifact_name      = "demostore"
  description        = "Fictional demo retailer for Price Hunter"
  artifacts_bucket   = var.artifacts_bucket
  app_version        = var.app_version
  timeout            = 30 # the slow-kettle fault sleeps 20s
  memory_size        = 128
  environment        = { DEMOSTORE_ADMIN_TOKEN = random_password.demostore_admin[0].result }
  policy_json        = data.aws_iam_policy_document.demostore.json
  log_retention_days = 7
  # Cap cost and blast radius: it's a toy.
  reserved_concurrency = 10
}

resource "aws_lambda_function_url" "demostore" {
  count              = var.deploy_demostore ? 1 : 0
  function_name      = module.demostore[0].function_name
  authorization_type = "NONE" # public on purpose: it's fictional, and workers fetch it like any retailer
}

locals {
  # https://abc.lambda-url.us-east-1.on.aws/ -> abc.lambda-url.us-east-1.on.aws
  demostore_host = var.deploy_demostore ? trimsuffix(trimprefix(aws_lambda_function_url.demostore[0].function_url, "https://"), "/") : var.demostore_host
}

output "demostore_url" {
  value = var.deploy_demostore ? aws_lambda_function_url.demostore[0].function_url : ""
}

output "demostore_admin_token" {
  value     = var.deploy_demostore ? random_password.demostore_admin[0].result : ""
  sensitive = true
}
