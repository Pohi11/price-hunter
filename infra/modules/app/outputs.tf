output "web_url" { value = local.web_url }
output "api_url" { value = aws_apigatewayv2_stage.default.invoke_url }
output "web_bucket" { value = aws_s3_bucket.web.bucket }
output "cloudfront_distribution_id" { value = aws_cloudfront_distribution.web.id }
output "user_pool_id" { value = aws_cognito_user_pool.main.id }
output "user_pool_client_id" { value = aws_cognito_user_pool_client.web.id }
output "cognito_domain" { value = "https://${aws_cognito_user_pool_domain.main.domain}.auth.${var.region}.amazoncognito.com" }
output "table_name" { value = aws_dynamodb_table.main.name }
output "table_stream_arn" { value = aws_dynamodb_table.main.stream_arn }
output "checks_queue_url" { value = aws_sqs_queue.checks.url }
output "checks_dlq_url" { value = aws_sqs_queue.checks_dlq.url }
output "streams_dlq_url" { value = aws_sqs_queue.streams_dlq.url }
output "snapshots_bucket" { value = aws_s3_bucket.snapshots.bucket }
output "alarm_topic_arn" { value = aws_sns_topic.alarms.arn }
output "function_names" {
  value = {
    api       = module.api.function_name
    scheduler = module.scheduler.function_name
    worker    = module.worker.function_name
    streams   = module.streams.function_name
  }
}
