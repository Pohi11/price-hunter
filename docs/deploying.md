# Deploying Price Hunter to AWS

The steps below take an empty AWS account to a running environment. Once CI/CD is set up and the repository variable `DEPLOY_ENABLED` is set to `true`, merges to `main` run steps 3–5 automatically.

## Prerequisites

- An AWS account. Ideally one account for dev and one for prod under AWS Organizations; one account with both environments also works.
- AWS CLI v2 signed in as an administrator (`aws login` or SSO), Terraform ≥ 1.10, Go, Node 20+, Python 3.
- The account's root user has MFA, and there are no root access keys.

## 1. Bootstrap the account (once per account)

```sh
cd infra/bootstrap
terraform init
terraform apply \
  -var 'github_repo=Pohi11/price-hunter' \
  -var 'environments=["dev","prod"]' \
  -var 'budget_email=you@example.com'
```

This creates:

- the Terraform state bucket (versioned, encrypted, with S3-native locking)
- the artifacts bucket
- the GitHub OIDC provider
- the `pricehunter-ci-plan` role (read-only) and the `pricehunter-ci-deploy` role (assumable only by protected GitHub environments)
- a $10/month budget alert

Keep the local `terraform.tfstate` from this step somewhere safe.

## 2. Configure the environment

```sh
cd infra/envs/dev
cp backend.hcl.example backend.hcl                 # fill in the account ID
cp terraform.tfvars.example terraform.tfvars       # artifacts bucket + alarm email
```

## 3. Deploy

```sh
scripts/deploy.sh dev                 # builds, uploads s3://<artifacts>/<sha>/*.zip, applies, publishes the UI
```

The script prints the web URL (CloudFront) and the API URL. Confirm the SNS email so you receive alarms.

## 4. Create your user

Self-signup is off by default:

```sh
POOL=$(terraform -chdir=infra/envs/dev output -json app | python -c 'import json,sys;print(json.load(sys.stdin)["user_pool_id"])')
aws cognito-idp admin-create-user --user-pool-id "$POOL" --username you@example.com \
  --user-attributes Name=email,Value=you@example.com Name=email_verified,Value=true
```

Cognito emails a temporary password, and the first sign-in asks for a new one.

## 5. Smoke test

```sh
API=$(terraform -chdir=infra/envs/dev output -json app | python -c 'import json,sys;print(json.load(sys.stdin)["api_url"])')
curl -s "$API/healthz"                                   # {"status":"ok","version":"<sha>"}
TOKEN=<ID token from the browser session> API=${API%/} scripts/smoke-api.sh
```

## Promote to prod

Prod never builds. It deploys a version that already ran in dev:

```sh
scripts/deploy.sh prod <sha-that-is-in-dev>
```

## Roll back

Redeploy an earlier SHA. Artifacts are immutable and kept for 180 days.

```sh
scripts/deploy.sh prod <previous-sha>
```

## Running the UI locally against dev

`envs/dev` adds `http://localhost:5173/` as an OAuth callback. To use it, put the dev `config.json` values in `web/public/config.json` (git-ignored) and run `npm run dev`.

## Tear down (dev)

```sh
terraform -chdir=infra/envs/dev destroy -var app_version=<any-deployed-sha>
```

Prod has deletion protection on the table and the user pool, so it must be disabled in tfvars first.

## Cost (dev, idle to light use)

The main costs, all small:

- CloudWatch Logs ingestion (~$0.50/GB, with 14-day retention)
- custom metrics (~$0.30 each)
- alarms beyond the 10 free ones ($0.10 each)

Lambda, SQS, DynamoDB on-demand, API Gateway, EventBridge Scheduler, S3 and CloudFront stay inside or near their free tiers at personal scale. Expect **about $1–5/month**. The budget alert fires at 80% of $10.
