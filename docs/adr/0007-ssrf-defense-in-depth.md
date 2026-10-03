# ADR-007: SSRF defense in depth for user-supplied URLs

- **Status:** accepted
- **Date:** 2026-10-01

## Context

Users give us arbitrary URLs, and our servers fetch them. That is the textbook setup for server-side request forgery (SSRF): an attacker gets us to request internal addresses. Targets that matter here:

- cloud metadata endpoints: `169.254.169.254`, `169.254.170.2`, `fd00:ec2::254`
- the Lambda Runtime API on loopback (`127.0.0.1:9001`)
- anything on a private network

Validating a URL once at input time isn't enough:

- DNS can change between validation and fetch (DNS rebinding).
- A public hostname can resolve to a private IP.
- A public page can redirect to an internal one.

## Decision

Four layers, each independently tested:

1. **Input validation** (`urlx.Validate`), which gives users clear 422 errors:
   - http and https only
   - ports 80 and 443 only
   - no userinfo
   - no IP literals, including decimal, hex and octal forms
   - a real ICANN public-suffix domain, so no `localhost`, `.local` or `.internal`
2. **Dial-time IP check** (`fetch.Guard.Control`, a `net.Dialer.Control` hook). It runs for every connection attempt with the *resolved* IP, including each redirect hop and each Happy Eyeballs attempt. It denies:
   - loopback, private, link-local, multicast, unspecified, CGNAT, TEST-NET and reserved ranges
   - NAT64, 6to4 and Teredo, which can tunnel to IPv4 targets
   - IPv4-mapped IPv6, which is unmapped before checking
   - any port other than 80 and 443
3. **Redirect re-validation** (`CheckRedirect`): at most 5 hops, and each target passes layer 1.
4. **Containment if a bypass happens:**
   - `Proxy: nil`, because environment proxies would bypass the dial hook
   - 3 MB body cap after decompression, plus content-type checks and strict timeouts
   - least-privilege IAM for the worker role, so even a successful SSRF reaches little
   - no VPC, so there are no internal services to reach

**Local development:** reaching the demo store on `localhost` needs an explicit opt-in (`--allow-localhost` / `PH_ALLOW_LOCALHOST`). Config will refuse it unless `PH_ENV=local`. It permits loopback *only*; metadata and private ranges stay blocked.

## Consequences

- Tests cover a 28-address guard table, resolution-based bypass (`localhost` → `127.0.0.1`), a redirect to the metadata endpoint, a gzip bomb, and fuzzing of the validator.
- A few legitimate retailers on non-standard ports can't be tracked. That's an acceptable restriction.
