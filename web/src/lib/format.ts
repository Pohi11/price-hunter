import type { Money, Product } from "../api/client";

const formatters = new Map<string, Intl.NumberFormat>();

/** Formats a decimal-string amount without float rounding surprises. */
export function money(m: Money | null | undefined): string {
  if (!m) return "—";
  return amount(m.amount, m.currency);
}

export function amount(value: string | null | undefined, currency: string): string {
  if (value == null) return "—";
  let f = formatters.get(currency);
  if (!f) {
    f = new Intl.NumberFormat(undefined, { style: "currency", currency });
    formatters.set(currency, f);
  }
  // Amounts are small decimals; Number is exact enough for display.
  return f.format(Number(value));
}

/** Whole-unit formatting for big-ticket prices (cars): "$25,400". */
export function compactMoney(m: Money | null | undefined): string {
  if (!m) return "—";
  const n = Number(m.amount);
  if (n >= 10_000 && Number.isInteger(n)) {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: m.currency, maximumFractionDigits: 0 }).format(n);
  }
  return money(m);
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const s = Math.round((new Date(iso).getTime() - now) / 1000);
  const abs = Math.abs(s);
  if (abs < 45) return s <= 0 ? "just now" : "in a moment";
  if (abs < 3600) return rtf.format(Math.round(s / 60), "minute");
  if (abs < 86_400) return rtf.format(Math.round(s / 3600), "hour");
  return rtf.format(Math.round(s / 86_400), "day");
}

export type Badge = { label: string; tone: "good" | "warn" | "muted" | "info" | "bad" };

/** One status badge per product, most important condition first. */
export function badge(p: Product): Badge {
  if (p.status === "GONE") return { label: "Removed by retailer", tone: "muted" };
  if (p.status === "PAUSED") return { label: "Paused", tone: "muted" };
  if (p.status === "NEEDS_ATTENTION") return { label: "Needs attention", tone: "bad" };
  if (!p.current_price) return p.last_error ? { label: "Can't read price", tone: "warn" } : { label: "Checking…", tone: "info" };
  if (p.alert.state === "TRIGGERED") return { label: "Target hit", tone: "good" };
  if (p.stale) return { label: "Stale", tone: "warn" };
  if (p.availability === "OUT_OF_STOCK") return { label: "Out of stock", tone: "warn" };
  return { label: "Watching", tone: "info" };
}

/** Percent the current price sits above (+) or below (−) the target. */
export function distanceToTarget(p: Product): number | null {
  if (!p.current_price) return null;
  const cur = Number(p.current_price.amount);
  const tgt = Number(p.target_price.amount);
  if (!tgt) return null;
  return ((cur - tgt) / tgt) * 100;
}

const frequencyLabels: Record<string, string> = { "1h": "Hourly", "6h": "Every 6 hours", "12h": "Twice a day", "24h": "Daily" };
export const frequencyLabel = (f: string) => frequencyLabels[f] ?? f;

const outcomeLabels: Record<string, string> = {
  OK: "Price found",
  PRICE_NOT_FOUND: "No price on page",
  AMBIGUOUS_PRICE: "Several prices on page",
  IMPLAUSIBLE_PRICE: "Price looked wrong",
  CURRENCY_MISMATCH: "Different currency",
  FETCH_TRANSIENT: "Retailer didn't respond",
  RATE_LIMITED: "Retailer rate limit",
  BLOCKED: "Blocked by retailer",
  ROBOTS_DISALLOWED: "Disallowed by robots.txt",
  NOT_FOUND: "Page no longer exists",
  FORBIDDEN_TARGET: "Address not allowed",
  UNSUPPORTED_CONTENT: "Unreadable page",
  TOO_LARGE: "Page too large",
  HTTP_ERROR: "Unexpected response",
  CIRCUIT_OPEN: "Retailer paused briefly",
  PRODUCT_INACTIVE: "Not tracked",
};
export const outcomeLabel = (code?: string) => (code ? (outcomeLabels[code] ?? code) : "");
