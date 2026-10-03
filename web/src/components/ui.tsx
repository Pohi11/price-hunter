import type { ButtonHTMLAttributes, ReactNode } from "react";
import type { Badge } from "../lib/format";

const tones: Record<Badge["tone"], string> = {
  good: "bg-accent-soft text-accent",
  warn: "bg-warn-soft text-warn",
  bad: "bg-bad-soft text-bad",
  info: "bg-info-soft text-info",
  muted: "bg-surface-2 text-muted",
};

export function StatusBadge({ badge }: { badge: Badge }) {
  return (
    <span className={`inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium ${tones[badge.tone]}`}>
      {badge.tone === "good" && <span aria-hidden className="size-1.5 rounded-full bg-current" />}
      {badge.label}
    </span>
  );
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "danger" | "ghost" };

const variants = {
  primary: "bg-accent text-accent-ink hover:opacity-90",
  secondary: "bg-surface border border-line text-ink hover:bg-surface-2",
  danger: "bg-surface border border-line text-bad hover:bg-bad-soft",
  ghost: "text-muted hover:text-ink hover:bg-surface-2",
};

export function Button({ variant = "secondary", className = "", ...props }: ButtonProps) {
  return (
    <button
      {...props}
      className={`inline-flex items-center justify-center gap-2 rounded-lg px-3.5 py-2 text-sm font-medium transition disabled:cursor-not-allowed disabled:opacity-50 ${variants[variant]} ${className}`}
    />
  );
}

export function Card({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <section className={`rounded-xl border border-line bg-surface ${className}`}>{children}</section>;
}

export function Field({ label, error, hint, children }: { label: string; error?: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-sm font-medium">{label}</span>
      {children}
      {error ? <span className="mt-1 block text-xs text-bad">{error}</span> : hint ? <span className="mt-1 block text-xs text-muted">{hint}</span> : null}
    </label>
  );
}

export const inputClass =
  "w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-ink placeholder:text-muted focus:border-accent focus:outline-none";

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <span role="status" className="inline-flex items-center gap-2 text-sm text-muted">
      <span aria-hidden className="size-4 animate-spin rounded-full border-2 border-line border-t-accent" />
      {label}
    </span>
  );
}

export function ErrorNote({ error }: { error: unknown }) {
  const msg = error instanceof Error ? error.message : "Something went wrong.";
  return <p className="rounded-lg bg-bad-soft px-3 py-2 text-sm text-bad">{msg}</p>;
}
