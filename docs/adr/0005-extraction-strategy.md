# ADR-005: Use configurable extraction strategies, with a conservative scraping policy

- **Status:** accepted
- **Date:** 2026-10-01

## Context

Every retailer marks up prices differently, and markup changes without notice. The obvious design is one Go type per retailer (`AmazonProvider`, `WalmartProvider`, …). But most retailers differ only in *where* the price sits in the page, which is configuration, not behavior. Many sites also publish machine-readable schema.org data for search engines.

## Decision

1. A **strategy chain**, most reliable first:
   - `jsonld`: schema.org Product and Offer data
   - `meta`: Open Graph product tags and itemprop microdata
   - `selectors`: CSS selectors from the retailer profile

   The first strategy that finds a plausible price wins. The others still run, so disagreement between them can be reported.
2. **Retailer profiles live in `retailers.yaml`**, which is embedded in the binary and validated at startup. A new retailer is a YAML change plus golden fixtures. Custom Go code is only for logic that config can't express, and none has been needed yet.
3. **Hosts without a profile** use structured data only (`generic`), unless allowlist mode is on.
4. **Prefer no price over a wrong price.** Ambiguous text is rejected. Implausible values are rejected. Large jumps are held as `suspect` until a recheck confirms them.
5. **Scraping policy**, since the repo is public:
   - robots.txt is always honored
   - honest User-Agent with a repo link
   - check interval of at least 1 hour
   - low per-host rates
   - no proxy rotation, CAPTCHA solving, or fingerprint evasion
   - heavily bot-protected retailers (e.g. Amazon) are unsupported by design
   - the demo store is the primary demo target

## Consequences

- Most new retailers need no code.
- Golden fixtures (`testdata/pages`) make parser drift a failing test instead of a silent production bug.
- Some retailers will be unreachable. That's a product limitation, and it's stated in the README.
- Prices rendered client-side by JavaScript are out of scope until a V3 renderer exists.
