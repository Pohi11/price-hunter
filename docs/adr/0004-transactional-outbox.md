# ADR-004: Send alerts through a transactional outbox

- **Status:** accepted
- **Date:** 2026-10-01

## Context

When a price crosses the target, two things must happen: the product's alert state flips ARMED → TRIGGERED, and an email goes out. Doing them as two separate steps is a dual write.

- Flip the state, then send. If the send fails, the next check sees TRIGGERED and the alert is lost forever.
- Send, then flip the state. If the flip fails, the user gets duplicate alerts.

## Decision

- The worker's `CommitCheck` transaction writes a `Notification` item (status PENDING) **in the same transaction** as the alert-state change, the price point and the finished check. Either all of them happen or none do.
- Delivery is a separate, retryable step, `notify.Dispatcher.Dispatch`:
  1. Conditionally claim the notification: PENDING → SENDING. A SENDING claim older than 5 minutes may be retaken.
  2. Load the user's profile. If notifications are disabled or there's no email address, mark it SKIPPED.
  3. Send. On a transient error, release it back to PENDING and return an error so the caller retries. On a permanent error (bounce or suppression), mark it FAILED.
  4. Record SENT with the provider's message ID.
- **The trigger differs by environment, the logic doesn't:**
  - AWS: DynamoDB Streams invoke a Lambda for INSERTs of `entity = Notification`, with bisect-on-error, retries, and a DLQ on final failure.
  - Local: the checker calls `Dispatch` inline after commit, since there are no Streams locally. A failure there leaves the item PENDING.

## Consequences

- No alert can be lost to a crash between "decided to alert" and "queued the email."
- Duplicates are bounded. Email providers have no exactly-once delivery. The only duplicate window is a crash after the provider accepts the message and before SENT is recorded. That's documented, not hidden.
- Notifications carry a snapshot of product name, URL, price and target, so delivery needs no extra reads and stays correct even if the product changes or is deleted afterwards.
- Outbox items expire after 90 days via TTL.
