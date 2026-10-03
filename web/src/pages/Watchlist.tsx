import { useState } from "react";
import { Link } from "react-router-dom";
import type { Product } from "../api/client";
import { usePrices, useProducts } from "../api/hooks";
import { AddProductDialog } from "../components/AddProductDialog";
import { Sparkline } from "../components/charts";
import { Button, Card, ErrorNote, Spinner, StatusBadge } from "../components/ui";
import { ago, badge, compactMoney, distanceToTarget } from "../lib/format";

function Row({ p }: { p: Product }) {
  const { data: history } = usePrices(p.id, 30, 40);
  const dist = distanceToTarget(p);
  const hit = p.alert.state === "TRIGGERED" && p.status === "ACTIVE";
  return (
    <li>
      <Link to={`/products/${p.id}`}
        className="grid grid-cols-[1fr_auto] items-center gap-x-4 gap-y-1 px-4 py-3.5 hover:bg-surface-2 sm:grid-cols-[minmax(0,2.4fr)_6rem_1fr_1fr_1fr_8.5rem] sm:px-5">
        <div className="min-w-0">
          <p className="truncate font-medium">{p.name}</p>
          <p className="truncate text-xs text-muted">{p.retailer} · checked {ago(p.last_checked_at)}</p>
        </div>
        <div className="hidden sm:block"><Sparkline history={history} /></div>
        <div className="text-right sm:text-left">
          <p className={`tabular text-base font-semibold ${hit ? "text-accent" : ""}`}>{compactMoney(p.current_price)}</p>
          {dist !== null && (
            <p className="tabular text-xs text-muted">{dist <= 0 ? `${Math.abs(dist).toFixed(1)}% under` : `${dist.toFixed(1)}% to go`}</p>
          )}
        </div>
        <p className="tabular hidden text-sm text-muted sm:block">{compactMoney(p.target_price)}</p>
        <p className="tabular hidden text-sm text-muted sm:block">{compactMoney(p.lowest_price)}</p>
        <div className="col-span-2 sm:col-span-1 sm:text-right"><StatusBadge badge={badge(p)} /></div>
      </Link>
    </li>
  );
}

export function Watchlist() {
  const { data, isLoading, error } = useProducts();
  const [adding, setAdding] = useState(false);
  const hits = data?.filter((p) => p.alert.state === "TRIGGERED" && p.status === "ACTIVE").length ?? 0;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">My watchlist</h1>
          <p className="text-sm text-muted">
            {data ? `${data.length} tracked${hits ? ` · ${hits} at or below target` : ""}` : "Prices you're waiting on"}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>+ Track a product</Button>
      </div>

      <Card className="overflow-hidden">
        {isLoading && <div className="p-6"><Spinner /></div>}
        {error && <div className="p-6"><ErrorNote error={error} /></div>}
        {data && data.length === 0 && (
          <div className="px-6 py-16 text-center">
            <p className="font-medium">Nothing tracked yet</p>
            <p className="mx-auto mt-1 max-w-sm text-sm text-muted">
              Paste a product link and a target price. We'll check it on a schedule and email you when it drops.
            </p>
            <Button className="mt-5" variant="primary" onClick={() => setAdding(true)}>Track your first product</Button>
          </div>
        )}
        {data && data.length > 0 && (
          <>
            <div className="hidden grid-cols-[minmax(0,2.4fr)_6rem_1fr_1fr_1fr_8.5rem] gap-4 border-b border-line px-5 py-2 text-xs font-medium uppercase tracking-wide text-muted sm:grid">
              <span>Product</span><span>30 days</span><span>Price</span><span>Target</span><span>Lowest</span><span className="text-right">Status</span>
            </div>
            <ul className="divide-y divide-line">{data.map((p) => <Row key={p.id} p={p} />)}</ul>
          </>
        )}
      </Card>
      <AddProductDialog open={adding} onClose={() => setAdding(false)} />
    </div>
  );
}
