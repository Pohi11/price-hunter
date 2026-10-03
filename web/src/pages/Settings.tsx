import { useMe, useUpdateMe } from "../api/hooks";
import { Card, ErrorNote, Spinner } from "../components/ui";

export function Settings() {
  const { data: me, isLoading, error } = useMe();
  const update = useUpdateMe();
  if (isLoading) return <Spinner />;
  if (error || !me) return <ErrorNote error={error} />;
  return (
    <div className="max-w-xl space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
      <Card className="divide-y divide-line">
        <div className="flex items-center justify-between gap-4 p-5">
          <div>
            <p className="font-medium">Email alerts</p>
            <p className="text-sm text-muted">Sent to {me.email || "your account email"} when a price reaches your target.</p>
          </div>
          <label className="relative inline-flex cursor-pointer items-center">
            <input type="checkbox" className="peer sr-only" checked={me.notifications_enabled} disabled={update.isPending}
              onChange={(e) => update.mutate(e.target.checked)} aria-label="Email alerts" />
            <span className="h-6 w-11 rounded-full bg-line transition peer-checked:bg-accent" />
            <span className="absolute left-0.5 size-5 rounded-full bg-surface shadow transition peer-checked:translate-x-5" />
          </label>
        </div>
        <div className="flex justify-between p-5 text-sm">
          <span className="text-muted">Products tracked</span>
          <span className="tabular">{me.product_count} of {me.product_limit}</span>
        </div>
      </Card>
    </div>
  );
}
