# Architecture decision records

| # | Decision | Status |
|---|---|---|
| [001](0001-lambda-over-fargate.md) | Run compute on Lambda (with a server-mode fallback), not ECS/Fargate | accepted |
| [002](0002-single-table-dynamodb.md) | One DynamoDB table with a sparse, sharded "due" index | accepted |
| [003](0003-database-driven-scheduling.md) | Database-driven scheduling; `next_check_at` doubles as the lease | accepted |
| [004](0004-transactional-outbox.md) | Alerts through a transactional outbox delivered by DynamoDB Streams | accepted |
| [005](0005-extraction-strategy.md) | Configurable extraction strategy chain and a conservative scraping policy | accepted |
| [006](0006-failure-classification-and-rate-limits.md) | Expected failures are outcomes; per-host rate bounds | accepted |
| [007](0007-ssrf-defense-in-depth.md) | SSRF defense in depth, with the authoritative check at dial time | accepted |
