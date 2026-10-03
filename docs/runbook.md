# Price Hunter runbook

Every CloudWatch alarm links to a section here. Each section covers what the alarm means, what to look at first, and how to fix it. Dashboard: the `dashboard_url` Terraform output.

Useful commands (set `ENV=dev` or `ENV=prod`):

```sh
O() { terraform -chdir=infra/envs/$ENV output -json app | python -c "import json,sys; print(json.load(sys.stdin)['$1'])"; }
aws logs tail /aws/lambda/pricehunter-$ENV-worker --since 30m --format short    # live worker logs
```

Saved Logs Insights queries (CloudWatch → Logs Insights → *Queries*):

- `failed-checks-by-outcome`
- `slowest-checks`
- `trace-one-check` (every log line for one `check_id`, across all functions)
- `errors`

---

## schedule-lag

**`pricehunter-<env>-schedule-lag-slo`**: p95 of (check start − due time) exceeded 10 minutes over the last hour. **This is the user-facing SLO.**

1. Dashboard → *Check queue*. If the queue is deep, go to [queue-backlog](#queue-backlog).
2. If the queue is shallow but lag is high, the scheduler is behind:
   - Check the `scheduler-not-running` alarm.
   - Check `SchedulerBackpressureSkips`. If backpressure is active, the workers are the bottleneck.
   - Check `ProductsEnqueued` against `PH_SCHEDULER_MAX_PER_RUN` (500 per 5 minutes ≈ 6,000 per hour). If due products exceed that, raise the cap.
3. Fix the cause. Lag recovers by itself as the backlog drains: products wait safely in DynamoDB.

## queue-backlog

**`…-checks-queue-backlog`**: the oldest check message is more than 15 minutes old.

- **Worker errors** (`…-worker-errors` alarm, *Lambda errors* widget): run the `errors` query. DynamoDB throttling or bad config shows up there.
- **Concurrency cap reached** (*Worker utilization* at `maximum_concurrency`) and fetches are slow (*fetch latency*): one retailer is slow. Its circuit should open; if not, check `failed-checks-by-outcome`.
- **Throttles** (`…-worker-throttles`): the account's concurrency limit. Lower `worker_max_concurrency` for other workloads, or request a higher limit.
- Raising `worker_max_concurrency` increases load on retailers. Prefer fixing the cause.

## dlq

**`…-checks-dlq-not-empty`**: a check message failed 5 times. Retailer problems never cause this (they're recorded as outcomes), so it's a **bug or an AWS-side outage**.

```sh
export PH_ENV=$ENV PH_CHECK_QUEUE_URL=$(O checks_queue_url) PH_CHECK_DLQ_URL=$(O checks_dlq_url)
go run ./cmd/pricehunter dlq peek            # read bodies (check_id, product_id); read-only
# trace a check_id with the `trace-one-check` query; fix and deploy
go run ./cmd/pricehunter dlq redrive         # send back to the main queue (safe: consumers are idempotent)
```

**`…-scheduler-dlq-not-empty`**: EventBridge Scheduler couldn't invoke the scheduler Lambda (permissions, function deleted, throttling). Each missed pass is harmless because the next one runs 5 minutes later. Fix the cause, then purge the DLQ; there's nothing to redrive.

## streams-dlq

**`…-streams-dlq-not-empty`**: a DynamoDB Streams batch exhausted its retries. The message holds the shard and sequence range, not the data.

- The notification stays `PENDING` in the table. After fixing the cause (often SES configuration), redeliver it by re-running dispatch for the user's pending notifications, or wait for the user's next alert.
- An unpurged deleted product only leaves orphaned history, invisible to users. Re-run `PurgeProductData` if needed.

## scheduler-not-running

**`…-scheduler-not-running`**: no scheduler invocation for 15 minutes. **No product is being checked.**

1. `aws scheduler get-schedule --name pricehunter-$ENV-scheduling-pass`: is it `ENABLED`?
2. `…-scheduler-dlq` messages and errors on the scheduler-invoke role.
3. The scheduler Lambda exists and its init succeeds: `aws logs tail /aws/lambda/pricehunter-$ENV-scheduler`.

## parser-drift

**`…-extraction-failures-<retailer>`**: 5 or more checks in an hour loaded the page but found no usable price. The retailer most likely changed its layout.

1. Download a snapshot:

   ```sh
   aws s3 ls s3://$(O snapshots_bucket)/snapshots/<retailer>/ --recursive | tail
   ```

2. Save it as `testdata/pages/<retailer>/<case>.html`. Fix the selectors in `internal/retailer/retailers.yaml` (or note that the site now has JSON-LD).
3. `make golden`, review the diff, open a PR.
4. Affected products recover to ACTIVE on their next successful check. Products already in NEEDS_ATTENTION are checked daily, or the user can press *Check now*.

## notification-failures

**`…-notification-failures`**: an alert email permanently failed.

- **SES sandbox:** recipients must be verified (`MessageRejected`). Verify the address or request production access.
- **Bounce or complaint:** the address is on the SES suppression list. Check `aws sesv2 get-suppressed-destination --email-address …`.
- Run the `errors` query against `/aws/lambda/pricehunter-$ENV-streams`.

## email-bounces

**`…-email-bounces`**: 3 or more bounces in an hour. Sustained bounce rates risk SES account suspension. Find the addresses in SES → *Suppression list*, and consider disabling notifications for those users.

## lambda errors and throttles

**`…-<function>-errors`**: a function crashed, timed out or failed init. Run the `errors` query.

- An init failure (`invalid configuration`) after a deploy means a bad environment variable. Roll back with `scripts/deploy.sh $ENV <previous-sha>`.

**`…-api-5xx`**: 5 or more API 5xx responses in 5 minutes. Check the API log group (`level = ERROR`) and DynamoDB throttling.

## Budget alert

AWS Budgets email at 80% forecast or 100% actual of the monthly limit. Usual suspects:

- CloudWatch Logs ingestion: a log-level change, or a retry storm logging errors.
- Custom metric count: a new dimension value.
- A runaway worker concurrency setting.

Check Cost Explorer by service.
