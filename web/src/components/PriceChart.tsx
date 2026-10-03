import { CartesianGrid, Line, LineChart, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import type { PriceHistory } from "../api/client";
import { amount } from "../lib/format";

type Point = { t: number; price: number; suspect?: boolean };

function toPoints(h: PriceHistory): Point[] {
  return h.points.filter((p) => !p.suspect).map((p) => ({ t: new Date(p.t).getTime(), price: Number(p.price) }));
}

/** Step chart: a price holds until the next observation changes it. */
export function PriceChart({ history, target }: { history: PriceHistory; target: number }) {
  const data = toPoints(history);
  if (data.length === 0) {
    return <p className="py-16 text-center text-sm text-muted">No price history in this range yet.</p>;
  }
  const values = data.map((d) => d.price).concat(target);
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const pad = Math.max((hi - lo) * 0.1, hi * 0.02);
  const spanDays = (data[data.length - 1]!.t - data[0]!.t) / 86_400_000;
  const tick = (t: number) =>
    new Date(t).toLocaleString(undefined, spanDays > 2 ? { month: "short", day: "numeric" } : { hour: "numeric", minute: "2-digit" });

  return (
    <div className="h-72 w-full" role="img" aria-label={`Price history, ${data.length} points, lowest ${amount(String(Math.min(...data.map((d) => d.price))), history.currency)}`}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 8, right: 12, bottom: 0, left: 4 }}>
          <CartesianGrid stroke="var(--line)" vertical={false} />
          <XAxis dataKey="t" type="number" scale="time" domain={["dataMin", "dataMax"]} tickFormatter={tick}
            stroke="var(--muted)" tick={{ fontSize: 12 }} tickLine={false} axisLine={false} minTickGap={40} />
          <YAxis domain={[lo - pad, hi + pad]} tickFormatter={(v: number) => amount(v.toFixed(0), history.currency)}
            stroke="var(--muted)" tick={{ fontSize: 12 }} tickLine={false} axisLine={false} width={72} />
          <Tooltip
            contentStyle={{ background: "var(--surface)", border: "1px solid var(--line)", borderRadius: 8, color: "var(--ink)" }}
            labelFormatter={(t) => new Date(Number(t)).toLocaleString()}
            formatter={(v) => [amount(Number(v).toFixed(2), history.currency), "Price"]}
          />
          <ReferenceLine y={target} stroke="var(--chart-target)" strokeDasharray="5 4"
            label={{ value: `Target ${amount(target.toFixed(2), history.currency)}`, position: "insideTopRight", fill: "var(--chart-target)", fontSize: 12 }} />
          <Line type="stepAfter" dataKey="price" stroke="var(--chart-line)" strokeWidth={2} dot={false} isAnimationActive={false} />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

export function Sparkline({ history }: { history?: PriceHistory }) {
  const data = history ? toPoints(history) : [];
  if (data.length < 2) return <span className="block h-8 w-24" />;
  return (
    <span className="block h-8 w-24" aria-hidden>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data}>
          <YAxis hide domain={["dataMin", "dataMax"]} />
          <Line type="stepAfter" dataKey="price" stroke="var(--chart-line)" strokeWidth={1.5} dot={false} isAnimationActive={false} />
        </LineChart>
      </ResponsiveContainer>
    </span>
  );
}
