# ADR-006: Expected failures are outcomes; rate limits are bounded per host

- **Status:** accepted
- **Date:** 2026-10-01

## Context

Retailer failures (503s, timeouts, removed pages, layout changes) are normal and frequent. If they were Go errors that caused SQS redelivery, one flaky retailer would cause retry storms, fill the dead-letter queue (DLQ) with non-bugs, and hide real infrastructure faults.

## Decision

**Two retry layers with different jobs**

1. *In-request retries* happen in `fetch`: at most 2, with exponential backoff and full jitter, and only for network errors, 5xx and 429. A `Retry-After` longer than 3 seconds isn't waited for in-request; it's returned so the scheduler can honor it.
2. *State-based backoff* happens in `domain.Policy`. A failed check is recorded as an outcome, and the message is acknowledged. `next_check_at` moves out by 15m → 30m → 1h → … → 24h.
   - 3 consecutive extraction failures set the status to **NEEDS_ATTENTION**.
   - 3 consecutive 404/410 responses set it to **GONE**, and the product is unscheduled.

**SQS redelivery and the DLQ are reserved for infrastructure failures** (DynamoDB errors, panics, timeouts). A message in the DLQ means a bug or an outage, never "the retailer was down."

**Rate limits**

- Each process keeps a token bucket per host, with the rate from the retailer profile.
- The global bound per host is (the SQS event source's `maximum_concurrency`) × (the per-host rate). For example, 5 × 0.5 rps gives a worst case of 2.5 rps.
- The scheduler adds ±5% jitter so checks don't line up.
- A shared per-domain circuit breaker in DynamoDB stops checks to a retailer that is failing for every product.

## Consequences

- DLQ alarms are always actionable.
- Retailers see bounded, predictable traffic from us.
- The global rate bound is derived, not enforced exactly. A DynamoDB token bucket could make it exact if ever needed; that isn't justified at this scale.
