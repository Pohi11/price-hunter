# ADR-001: Run compute on Lambda, not ECS/Fargate

- **Status:** accepted
- **Date:** 2026-10-01

## Context

Price Hunter has four kinds of work: the HTTP API, a scheduler that runs every 5 minutes, price-check workers that take 1–30 seconds each, and sending notifications. Load is bursty and usually close to zero, since it's a personal app. It should still scale to thousands of products, and one person has to be able to run it cheaply.

## Decision

Run all four on **AWS Lambda** (Go, `provided.al2023`, arm64):

- the API behind API Gateway HTTP API
- the scheduler triggered by EventBridge Scheduler
- workers fed by an SQS event source mapping
- notifications fed by DynamoDB Streams

The Go code is also written as an ordinary long-running server (`pricehunter serve`), with an HTTP server, a scheduler ticker, an SQS long-poll consumer with a worker pool, and graceful shutdown. That's what runs locally, and it could be containerized for ECS without code changes. The Lambda entrypoints are thin adapters over the same services.

## Consequences

**In Lambda's favor**

- Costs about $0 at personal scale, and scales to zero.
- The SQS event source does the polling, batching, partial-batch failures and scaling. `maximum_concurrency` gives a global concurrency cap, which protects retailers.
- No VPC is needed. DynamoDB, SQS, S3 and SES are reachable over IAM-authenticated public endpoints. A Fargate service in private subnets would need a NAT gateway (at least $32/month) just to reach the internet.
- No servers or images to patch.

**Costs we accept**

- In-memory state isn't shared across instances. Shared state, such as the per-retailer circuit breaker, lives in DynamoDB.
- The 15-minute execution limit doesn't matter, because checks are capped at about 25 seconds.
- Outbound IPs are shared AWS ranges. Retailers that block cloud IPs block Fargate too.

**When to revisit**

- JS rendering with a headless browser (V3) would run as a separate Fargate service on its own queue.
- Steady high throughput, or a need for a fixed egress IP.

## Alternatives considered

- **ECS/Fargate for everything.** The ALB is about $16/month, plus about $9/month per task and public IPv4 charges, with no benefit for 1–30 second jobs.
- **A single EC2 instance.** Cheap, but it means patching, a single point of failure, and you'd build scheduling and retries yourself.
