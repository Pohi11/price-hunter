import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, type Product } from "../api/client";
import { isTerminal, keys, useCheckStatus, useChecks, useDeleteProduct, usePrices, useProduct, useTriggerCheck, useUpdateProduct } from "../api/hooks";
import { PriceChart } from "../components/charts";
import { Button, Card, ErrorNote, Field, Spinner, StatusBadge, inputClass } from "../components/ui";
import { ago, amount, badge, compactMoney, frequencyLabel, money, outcomeLabel } from "../lib/format";

const ranges = [
  { label: "7D", days: 7 },
  { label: "30D", days: 30 },
  { label: "90D", days: 90 },
  { label: "All", days: 0 },
];

function Stat({ label, value, tone = "" }: { label: string; value: string; tone?: string }) {
  return (
    <div>
      <p className="text-xs uppercase tracking-wide text-muted">{label}</p>
      <p className={`tabular mt-0.5 text-xl font-semibold ${tone}`}>{value}</p>
    </div>
  );
}

function CheckNow({ p }: { p: Product }) {
  const qc = useQueryClient();
  const trigger = useTriggerCheck(p.id);
  const [checkId, setCheckId] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const { data: check } = useCheckStatus(p.id, checkId);

  useEffect(() => {
    if (!isTerminal(check)) return;
    qc.invalidateQueries({ queryKey: keys.product(p.id) });
    qc.invalidateQueries({ queryKey: ["products", p.id, "prices"] });
    qc.invalidateQueries({ queryKey: keys.checks(p.id) });
    qc.invalidateQueries({ queryKey: keys.products });
  }, [check, p.id, qc]);

  async function run() {
    setNote(null);
    try {
      const c = await trigger.mutateAsync();
      setCheckId(c.check_id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 429) setNote(`Checked recently. Try again in ${Math.ceil((e.retryAfter ?? 60) / 60)} min.`);
      else setNote(e instanceof Error ? e.message : "Check failed to start.");
    }
  }

  const running = !!checkId && !isTerminal(check);
  let status: string | null = note;
  if (running) status = check?.status === "RUNNING" ? "Checking the retailer…" : "Queued…";
  else if (check?.status === "SUCCEEDED") status = `Found ${money(check.result?.price)}${check.result?.alert_triggered ? " — target hit!" : ""}`;
  else if (check && isTerminal(check)) status = check.error?.message ?? outcomeLabel(check.error?.code);

  return (
    <div className="flex flex-wrap items-center gap-3">
      <Button variant="primary" onClick={run} disabled={running || trigger.isPending}>{running ? "Checking…" : "Check now"}</Button>
      {status && <span role="status" className="text-sm text-muted">{status}</span>}
    </div>
  );
}

function EditForm({ p, onDone }: { p: Product; onDone: () => void }) {
  const update = useUpdateProduct(p);
  const [name, setName] = useState(p.name);
  const [target, setTarget] = useState(p.target_price.amount);
  const [freq, setFreq] = useState(p.check_frequency);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErrors({});
    setErr(null);
    try {
      await update.mutateAsync({
        ...(name !== p.name ? { name } : {}),
        ...(target !== p.target_price.amount ? { target_price: { amount: target, currency: p.target_price.currency } } : {}),
        ...(freq !== p.check_frequency ? { check_frequency: freq } : {}),
      });
      onDone();
    } catch (e) {
      if (e instanceof ApiError && e.status === 412) setErr("This product changed in another tab. Reload and try again.");
      else if (e instanceof ApiError && e.problem.errors?.length) setErrors(e.fieldErrors());
      else setErr(e instanceof Error ? e.message : "Update failed.");
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-4 sm:grid-cols-3">
      <Field label="Name" error={errors.name}><input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} maxLength={120} /></Field>
      <Field label={`Target (${p.target_price.currency})`} error={errors["target_price.amount"]}>
        <input className={`${inputClass} tabular`} inputMode="decimal" value={target} onChange={(e) => setTarget(e.target.value)} />
      </Field>
      <Field label="Check">
        <select className={inputClass} value={freq} onChange={(e) => setFreq(e.target.value as Product["check_frequency"])}>
          {["1h", "6h", "12h", "24h"].map((f) => <option key={f} value={f}>{frequencyLabel(f)}</option>)}
        </select>
      </Field>
      {err && <p className="sm:col-span-3 rounded-lg bg-bad-soft px-3 py-2 text-sm text-bad">{err}</p>}
      <div className="flex gap-2 sm:col-span-3">
        <Button type="submit" variant="primary" disabled={update.isPending}>Save</Button>
        <Button type="button" variant="ghost" onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}

function CheckLog({ id }: { id: string }) {
  const { data } = useChecks(id);
  if (!data?.length) return <p className="text-sm text-muted">No checks yet.</p>;
  return (
    <ul className="divide-y divide-line text-sm">
      {data.map((c) => (
        <li key={c.check_id} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-2">
          <span className="text-muted">{ago(c.finished_at ?? c.queued_at)} · {c.trigger.toLowerCase()}</span>
          <span className={c.status === "FAILED" ? "text-bad" : c.status === "SUCCEEDED" ? "" : "text-muted"}>
            {c.status === "SUCCEEDED" ? `${money(c.result?.price)} via ${c.result?.strategy}` : c.status === "QUEUED" || c.status === "RUNNING"
              ? c.status.toLowerCase() : outcomeLabel(c.error?.code)}
            {c.duration_ms != null && c.status !== "QUEUED" ? <span className="text-muted"> · {(c.duration_ms / 1000).toFixed(1)}s</span> : null}
          </span>
        </li>
      ))}
    </ul>
  );
}

export function ProductDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { data: p, isLoading, error } = useProduct(id);
  const [days, setDays] = useState(30);
  const { data: history, isLoading: chartLoading } = usePrices(id, days);
  const del = useDeleteProduct();
  const [editing, setEditing] = useState(false);
  const pause = useUpdateProduct(p ?? ({ id, version: 0 } as Product));

  if (isLoading) return <Spinner />;
  if (error || !p) return <ErrorNote error={error ?? new Error("Not found")} />;

  const hit = p.alert.state === "TRIGGERED" && p.status === "ACTIVE";
  const paused = p.status === "PAUSED" || p.status === "GONE";

  return (
    <div className="space-y-6">
      <Link to="/" className="text-sm text-muted hover:text-ink">← Watchlist</Link>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">{p.name}</h1>
            <StatusBadge badge={badge(p)} />
          </div>
          <a href={p.url} target="_blank" rel="noopener noreferrer nofollow" className="mt-1 block truncate text-sm text-muted underline-offset-2 hover:underline">
            {p.retailer} · {p.url}
          </a>
        </div>
        <CheckNow p={p} />
      </div>

      {p.status === "NEEDS_ATTENTION" && (
        <p className="rounded-lg bg-bad-soft px-4 py-3 text-sm text-bad">
          The last {p.last_error ? "few checks" : "checks"} failed ({outcomeLabel(p.last_error?.code)}). We now check once a day.
          If the retailer changed its page, try again later or delete and re-add the product.
        </p>
      )}

      <Card className="grid grid-cols-2 gap-6 p-5 sm:grid-cols-4">
        <Stat label="Current" value={compactMoney(p.current_price)} tone={hit ? "text-accent" : ""} />
        <Stat label="Target" value={compactMoney(p.target_price)} />
        <Stat label="Lowest seen" value={compactMoney(p.lowest_price)} />
        <Stat label="Next check" value={paused ? "—" : ago(p.next_check_at)} />
      </Card>

      <Card className="p-5">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="font-semibold">Price history</h2>
            {history?.summary.min && (
              <p className="tabular text-sm text-muted">
                Low {amount(history.summary.min, history.currency)} · High {amount(history.summary.max, history.currency)} · {history.summary.count} checks
              </p>
            )}
          </div>
          <div role="tablist" className="flex rounded-lg border border-line p-0.5">
            {ranges.map((r) => (
              <button key={r.label} role="tab" aria-selected={days === r.days} onClick={() => setDays(r.days)}
                className={`rounded-md px-3 py-1 text-sm ${days === r.days ? "bg-surface-2 font-medium" : "text-muted hover:text-ink"}`}>{r.label}</button>
            ))}
          </div>
        </div>
        {chartLoading || !history ? <div className="h-72 py-24 text-center"><Spinner /></div> : <PriceChart history={history} target={Number(p.target_price.amount)} />}
      </Card>

      <div className="grid gap-6 lg:grid-cols-[2fr_1fr]">
        <Card className="p-5">
          <h2 className="mb-2 font-semibold">Recent checks</h2>
          <CheckLog id={p.id} />
        </Card>
        <Card className="space-y-4 p-5">
          <h2 className="font-semibold">Settings</h2>
          {editing ? (
            <EditForm p={p} onDone={() => setEditing(false)} />
          ) : (
            <>
              <dl className="space-y-1 text-sm">
                <div className="flex justify-between"><dt className="text-muted">Checks</dt><dd>{frequencyLabel(p.check_frequency)}</dd></div>
                <div className="flex justify-between"><dt className="text-muted">Tracking since</dt><dd>{new Date(p.created_at).toLocaleDateString()}</dd></div>
                <div className="flex justify-between"><dt className="text-muted">Availability</dt><dd>{p.availability.replaceAll("_", " ").toLowerCase()}</dd></div>
              </dl>
              <div className="flex flex-wrap gap-2">
                <Button onClick={() => setEditing(true)}>Edit</Button>
                <Button onClick={() => pause.mutate({ paused: !paused })} disabled={pause.isPending}>{paused ? "Resume" : "Pause"}</Button>
                <Button variant="danger" disabled={del.isPending}
                  onClick={async () => { if (confirm(`Stop tracking "${p.name}"? Its price history will be deleted.`)) { await del.mutateAsync(p.id); navigate("/"); } }}>
                  Delete
                </Button>
              </div>
            </>
          )}
        </Card>
      </div>
    </div>
  );
}
