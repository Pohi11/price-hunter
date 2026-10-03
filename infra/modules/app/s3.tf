# HTML of pages whose extraction failed, for diagnosing parser drift.
resource "aws_s3_bucket" "snapshots" {
  bucket        = "${local.name}-snapshots-${local.account}"
  force_destroy = var.env != "prod"
}

resource "aws_s3_bucket_public_access_block" "snapshots" {
  bucket                  = aws_s3_bucket.snapshots.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "snapshots" {
  bucket = aws_s3_bucket.snapshots.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "snapshots" {
  bucket = aws_s3_bucket.snapshots.id
  rule {
    id     = "expire-snapshots"
    status = "Enabled"
    filter {
      prefix = "snapshots/"
    }
    expiration {
      days = 14
    }
  }
}
