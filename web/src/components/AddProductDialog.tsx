import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError } from "../api/client";
import { useCreateProduct } from "../api/hooks";
import { Button, Field, inputClass } from "./ui";

const currencies = ["USD", "EUR", "GBP", "CAD", "AUD"];

export function AddProductDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const navigate = useNavigate();
  const create = useCreateProduct();
  const [form, setForm] = useState({ name: "", url: "", amount: "", currency: "USD", check_frequency: "6h" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);

  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErrors({});
    setFormError(null);
    try {
      const p = await create.mutateAsync({
        name: form.name,
        url: form.url,
        target_price: { amount: form.amount, currency: form.currency },
        check_frequency: form.check_frequency as "1h" | "6h" | "12h" | "24h",
      });
      setForm({ name: "", url: "", amount: "", currency: form.currency, check_frequency: "6h" });
      onClose();
      navigate(`/products/${p.id}`);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 409 && err.problem.existing_product_id) {
          setFormError("You're already tracking this product.");
          return;
        }
        const fe = err.fieldErrors();
        if (Object.keys(fe).length) setErrors(fe);
        else setFormError(err.message);
      } else setFormError("Could not reach the server.");
    }
  }

  return (
    <dialog ref={ref} onClose={onClose}
      className="m-auto w-[min(32rem,calc(100vw-2rem))] rounded-2xl border border-line bg-surface p-0 text-ink shadow-xl backdrop:bg-black/40">
      <form onSubmit={submit} className="space-y-4 p-6">
        <div>
          <h2 className="text-lg font-semibold">Track a product</h2>
          <p className="text-sm text-muted">We'll check the price on a schedule and email you when it reaches your target.</p>
        </div>
        <Field label="Product page URL" error={errors.url} hint="Paste the link to the product page.">
          <input className={inputClass} type="url" required placeholder="https://…" value={form.url} onChange={set("url")} autoFocus />
        </Field>
        <Field label="Name" error={errors.name}>
          <input className={inputClass} required maxLength={120} placeholder="Ninja Air Fryer" value={form.name} onChange={set("name")} />
        </Field>
        <div className="grid grid-cols-[1fr_auto] gap-3">
          <Field label="Alert me at or below" error={errors["target_price.amount"] ?? errors["target_price.currency"]}>
            <input className={`${inputClass} tabular`} required inputMode="decimal" pattern="\d+(\.\d{1,2})?" placeholder="100.00"
              value={form.amount} onChange={set("amount")} />
          </Field>
          <Field label="Currency">
            <select className={inputClass} value={form.currency} onChange={set("currency")}>
              {currencies.map((c) => <option key={c}>{c}</option>)}
            </select>
          </Field>
        </div>
        <Field label="Check" error={errors.check_frequency}>
          <select className={inputClass} value={form.check_frequency} onChange={set("check_frequency")}>
            <option value="1h">Hourly</option>
            <option value="6h">Every 6 hours</option>
            <option value="12h">Twice a day</option>
            <option value="24h">Daily</option>
          </select>
        </Field>
        {formError && <p className="rounded-lg bg-bad-soft px-3 py-2 text-sm text-bad">{formError}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={create.isPending}>{create.isPending ? "Adding…" : "Start tracking"}</Button>
        </div>
      </form>
    </dialog>
  );
}
