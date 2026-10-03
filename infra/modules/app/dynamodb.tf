# Single-table design (ADR-002). Mirrors store.TableDefinition in Go;
# keep the two in sync.
resource "aws_dynamodb_table" "main" {
  name         = local.name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "PK"
  range_key    = "SK"

  attribute {
    name = "PK"
    type = "S"
  }
  attribute {
    name = "SK"
    type = "S"
  }
  attribute {
    name = "GSI1PK"
    type = "S"
  }
  attribute {
    name = "GSI1SK"
    type = "S"
  }

  # Sparse "due for a check" index: only schedulable products carry GSI1 keys.
  global_secondary_index {
    name            = "GSI1"
    projection_type = "KEYS_ONLY"
    key_schema {
      attribute_name = "GSI1PK"
      key_type       = "HASH"
    }
    key_schema {
      attribute_name = "GSI1SK"
      key_type       = "RANGE"
    }
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  # Streams feed the notification outbox and cascade deletes.
  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  point_in_time_recovery {
    enabled = var.point_in_time_recovery
  }

  deletion_protection_enabled = var.deletion_protection

  server_side_encryption {
    enabled = true # AWS-owned key; a CMK costs $1/month for no added benefit here
  }
}
