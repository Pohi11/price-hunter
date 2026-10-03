import { describe, expect, it } from "vitest";
import type { Product } from "../api/client";
import { ago, badge, distanceToTarget } from "./format";

const base: Product = {
  id: "p", name: "Air Fryer", url: "https://x", retailer: "demostore",
  target_price: { amount: "100.00", currency: "USD" }, current_price: { amount: "94.00", currency: "USD" },
  lowest_price: { amount: "94.00", currency: "USD" }, availability: "IN_STOCK", check_frequency: "6h",
  status: "ACTIVE", stale: false, alert: { state: "ARMED", triggered_at: null }, last_checked_at: null,
  last_success_at: null, next_check_at: null, last_error: null, created_at: "", updated_at: "", version: 1,
};

describe("badge", () => {
  it("prioritizes lifecycle states over price state", () => {
    expect(badge({ ...base, status: "GONE", alert: { state: "TRIGGERED", triggered_at: null } }).label).toBe("Removed by retailer");
    expect(badge({ ...base, status: "NEEDS_ATTENTION" }).tone).toBe("bad");
    expect(badge({ ...base, current_price: null }).label).toBe("Checking…");
    expect(badge({ ...base, current_price: null, last_error: { code: "BLOCKED", retryable: true } }).label).toBe("Can't read price");
    expect(badge({ ...base, alert: { state: "TRIGGERED", triggered_at: null } }).label).toBe("Target hit");
    expect(badge({ ...base, stale: true }).label).toBe("Stale");
    expect(badge(base).label).toBe("Watching");
  });
});

describe("distanceToTarget", () => {
  it("is negative below target", () => {
    expect(distanceToTarget(base)).toBeCloseTo(-6);
    expect(distanceToTarget({ ...base, current_price: null })).toBeNull();
  });
});

describe("ago", () => {
  it("formats relative times", () => {
    const now = Date.parse("2026-10-01T12:00:00Z");
    expect(ago(null, now)).toBe("never");
    expect(ago("2026-10-01T11:59:50Z", now)).toBe("just now");
    expect(ago("2026-10-01T09:00:00Z", now)).toMatch(/3 hours ago/);
  });
});
