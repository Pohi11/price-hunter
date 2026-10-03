# ADR-002: One DynamoDB table, with a sparse "due" index

- **Status:** accepted
- **Date:** 2026-10-01

## Context

Price Hunter has seven small entities: profile, product, URL guard, price point, check, notification and domain health. All their access patterns are known in advance (see [architecture.md](../architecture.md)). Several writes must be atomic across entities. For example, creating a product writes the product, a uniqueness guard, a quota counter and the first check.

## Decision

- **One table** (`pricehunter-<env>`), on-demand capacity, with string `PK`/`SK` keys. User-owned items live under `USER#<sub>`. Product-owned items (history and checks) live under `PRODUCT#<id>`, so a chart is one `Query` over a contiguous sort-key range.
- **GSI1, sparse and KEYS_ONLY**, for "what is due?":
  - `GSI1PK = DUE#<fnv32(product_id) % 4>`
  - `GSI1SK = next_check_at`

  Only schedulable products (ACTIVE or NEEDS_ATTENTION) carry these attributes, so pausing a product or marking it GONE *removes* it from the index. The scheduler never filters.
- **Transactions are the integrity mechanism:**
  - product create: product + URL guard + quota + first check
  - check commit: check + product + price point + outbox notification
  - manual check: cooldown stamp + check record

  Partial writes are impossible, and the integration tests assert this.
- **Optimistic concurrency.** `version` increments on user edits and is exposed as the HTTP `ETag`. The worker's commit is conditioned on the version it read, so a target edited mid-check forces a re-evaluation instead of a stale alert.
- **Ownership checks for product-scoped partitions.** `PRODUCT#<id>` keys aren't user-scoped, so every history or check read first does `GetProduct(USER#<caller>, PRODUCT#<id>)`. Page cursors carry only a sort key, and the partition key is always rebuilt from the token. A forged cursor can't cross users.
- **Retention:** checks have a 30-day TTL and notifications 90 days. Price history is kept, at about 100 bytes per point.

## Consequences

- Every pattern is a single `GetItem`, `Query` or transaction. There are no scans.
- Fixed-width UTC timestamps (`2006-01-02T15:04:05.000Z`) keep lexical order equal to time order for sort keys.
- Four due shards cost nothing to query and avoid ever having to re-key. The shard count can never change without a migration, so it's a documented constant.
- Ad-hoc analytics need an export (V3: DynamoDB export to S3, queried with Athena).
- `store` owns every key format. No other package builds `"USER#..."` strings.

## Alternatives considered

- **Table per entity.** Easier to read, but cross-entity transactions get awkward and there's more IAM and Terraform to manage.
- **PostgreSQL (RDS or Aurora Serverless).** Flexible queries, but it needs a VPC (and so a NAT gateway for the scrapers), costs at least $15/month, and the flexibility isn't needed for a fixed set of access patterns.
