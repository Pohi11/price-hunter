import { lazy, Suspense, type ComponentProps } from "react";

// The chart library is the largest dependency; load it on demand so the
// first paint only needs the app shell and data.
const LazyPriceChart = lazy(() => import("./PriceChart").then((m) => ({ default: m.PriceChart })));
const LazySparkline = lazy(() => import("./PriceChart").then((m) => ({ default: m.Sparkline })));

export function PriceChart(props: ComponentProps<typeof LazyPriceChart>) {
  return (
    <Suspense fallback={<div className="h-72 w-full animate-pulse rounded-lg bg-surface-2" />}>
      <LazyPriceChart {...props} />
    </Suspense>
  );
}

export function Sparkline(props: ComponentProps<typeof LazySparkline>) {
  return (
    <Suspense fallback={<span className="block h-8 w-24" />}>
      <LazySparkline {...props} />
    </Suspense>
  );
}
