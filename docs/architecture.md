# Architecture

One Go module (a modular monolith) with thin entrypoints, serverless on AWS. There's no VPC, no containers in production, and no microservices.

```mermaid
flowchart LR
  subgraph Browser
    SPA[React SPA]
  end
  SPA -- static files --> CF[CloudFront + S3]
  SPA -- OIDC code+PKCE --> COG[Cognito]
  SPA -- JWT --> APIGW[API Gateway HTTP API<br/>JWT authorizer, throttling]
  APIGW --> API[λ api]
  API <--> DDB[(DynamoDB<br/>single table + sparse GSI1)]
  API -- manual check --> Q[[SQS checks]]
  EB[EventBridge Scheduler<br/>every 5 min] --> SCH[λ scheduler]
  SCH -- query due + lease --> DDB
  SCH -- SendMessageBatch --> Q
  Q -- batch, ReportBatchItemFailures --> W[λ worker]
  Q -. maxReceiveCount 5 .-> DLQ[[checks DLQ]]
  W -- safe fetch --> R((retailers /<br/>demo store))
  W -- one transaction:<br/>check + product + price + outbox --> DDB
  W -- failed pages --> S3S[(S3 snapshots<br/>14 days)]
  DDB -- Streams, filtered --> ST[λ streams]
  ST --> SES[SES] --> U((user email))
  ST -. on failure .-> SDLQ[[streams DLQ]]
  subgraph Observability
    CW[CloudWatch: JSON logs, EMF metrics,<br/>dashboard, SLO + DLQ + dead-man alarms]
  end
```

## Request and check flows

**Track a product.** `POST /v1/products`, then `products.Service.Create`:

1. Validate input and the URL (SSRF layer 1).
2. Resolve the retailer profile and canonicalize the URL.
3. One transaction writes the product, the URL guard, the quota counter and the first QUEUED check.
4. Enqueue the first check. `next_check_at = now + 15m` acts as a lease, so a lost enqueue self-heals.

**Scheduled check.**

1. The scheduler queries the 4 GSI1 shards for `next_check_at ≤ now`, takes a conditional lease on each due product, and enqueues messages in batches of 10.
2. The worker runs `BeginCheck`, the idempotency guard. A duplicate delivery stops here.
3. It checks the circuit breaker, then fetches through the SSRF-safe client.
4. It runs the extractor chain: JSON-LD, then meta/microdata, then selectors.
5. `domain.Policy.Apply`, a pure function, decides the product's next state: price, alert, backoff, status.
6. `CommitCheck` writes everything in one transaction, conditioned on the check being RUNNING and the product being at the version read.

**Alert.** The commit includes a PENDING Notification item (the transactional outbox). DynamoDB Streams invokes `streams`, which claims it (PENDING → SENDING), sends through SES, and records SENT.

## Package map

| Layer | Packages |
|---|---|
| Entrypoints | `cmd/pricehunter` (check · serve · seed · dlq), `cmd/lambda-{api,scheduler,worker,streams,demostore}`, `cmd/smoke`, `cmd/loadtest`, `cmd/demostore` |
| Composition | `internal/app` (builds everything from `internal/config`), `internal/lambdax` (Lambda event adapters) |
| Use cases | `internal/products`, `internal/checker`, `internal/scheduler`, `internal/notify` |
| Domain | `internal/domain`: Money, Product, the check policy and alert state machine (pure, no I/O) |
| Price extraction | `internal/urlx`, `internal/fetch`, `internal/extract`, `internal/retailer` |
| Adapters | `internal/store` (DynamoDB), `internal/queue` (SQS, in-memory, DLQ tools), `internal/snapshot`, `internal/api` (HTTP), `internal/auth` |
| Cross-cutting | `internal/telemetry` (slog, EMF), `internal/metrics` |

Dependencies point inward. `domain` imports nothing from the project, and the use-case packages define the small interfaces they consume.

## Where to read more

- Design decisions: [docs/adr/](adr/)
- Security model: [security.md](security.md) · Operations: [runbook.md](runbook.md) · Deploying: [deploying.md](deploying.md)
