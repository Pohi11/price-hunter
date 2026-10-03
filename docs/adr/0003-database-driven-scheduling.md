# ADR-003: Database-driven scheduling with next_check_at as a lease

- **Status:** accepted
- **Date:** 2026-10-01

## Context

Each product needs a check every 1–24 hours, plus sooner retries after failures and 15-minute rechecks of suspicious prices. The system has to tolerate duplicate scheduler runs, lost enqueues and crashed workers without double-checking or forgetting products.

## Decision

- Every product stores `next_check_at`, mirrored into the sparse GSI1 sort key while the product is schedulable.
- A scheduler runs every 5 minutes (EventBridge Scheduler in AWS, a ticker in server mode). For each of the 4 shards it queries `GSI1SK <= now`, oldest first, capped at `MaxPerRun`.
- **The lease is the schedule.** For each due product the scheduler runs a conditional `UpdateItem SET next_check_at = now+15m WHERE GSI1SK = :seen`. Only one concurrent scheduler wins each product. If the enqueue then fails, or the worker dies, the product becomes due again when the lease expires. There is no separate lease table, no lock, and no cleanup job.
- The worker's commit sets the *real* next time, computed by `domain.Policy`: the frequency ±5% jitter, an exponential backoff, a 15-minute recheck, or "unscheduled" for GONE.
- **Backpressure.** If the queue is deeper than `MaxQueueDepth`, the scheduler skips its run. Due products wait in DynamoDB, which is free, instead of piling into SQS, and schedule lag (an SLI) shows the backlog.
- New products get `next_check_at = now + lease` and a directly enqueued first check, so creation never waits for a scheduler tick.

## Consequences

- At-least-once with bounded staleness: the worst case for a lost message is a 15-minute delay.
- Ticks are 5 minutes, so a check can run up to 5 minutes late. That's fine for hourly-to-daily checks.
- One GSI query per shard per tick costs essentially nothing.

## Alternatives considered

- **One EventBridge schedule per product.** Offloads timing, but means N schedules to keep in sync with the database (create, update, pause, delete), with no central place for backpressure and opaque failures.
- **SQS delay queues.** The maximum delay is 15 minutes, too short for hourly or daily checks.
- **A separate lease item or distributed lock.** Unnecessary: the conditional write on the schedule itself is the lock.
