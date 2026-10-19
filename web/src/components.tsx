import { useState, type ReactNode } from "react";
import { formatBytes, formatNumber, humanizeKey, isPlainObject, looksLikeBytes } from "./format";
import { REDACTED } from "./types";

export function Spinner({ label = "Loading…" }: { label?: string }) {
  return <p className="muted" role="status"><span className="spinner" aria-hidden /> {label}</p>;
}

export function ErrorBanner({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : String(error);
  return <div className="banner banner-error" role="alert">{msg}</div>;
}

export function Notice({ kind = "info", children }: { kind?: "info" | "ok" | "warn"; children: ReactNode }) {
  return <div className={`banner banner-${kind}`} role="status">{children}</div>;
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {hint ? <span className="field-hint">{hint}</span> : null}
    </label>
  );
}

export function TextInput(props: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input className="input" {...props} />;
}

/** Password input that treats the redaction placeholder as "unchanged". */
export function SecretInput({ value, onChange, placeholder }: {
  value: string; onChange: (v: string) => void; placeholder?: string;
}) {
  const [shown, setShown] = useState(false);
  const isPlaceholder = value === REDACTED;
  return (
    <span className="secret">
      <input
        className="input"
        type={shown || isPlaceholder ? "text" : "password"}
        value={value}
        placeholder={placeholder}
        autoComplete="new-password"
        onChange={(e) => onChange(e.target.value)}
        aria-describedby={isPlaceholder ? undefined : undefined}
      />
      <button type="button" className="btn btn-ghost btn-small" onClick={() => setShown((s) => !s)}>
        {shown ? "Hide" : "Show"}
      </button>
      {isPlaceholder ? <span className="field-hint">Saved — leave as {REDACTED} to keep</span> : null}
    </span>
  );
}

export function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  );
}

export function UsageBar({ used, budget, label }: { used: number; budget: number; label?: string }) {
  const pct = budget > 0 ? Math.min(100, Math.max(0, (used / budget) * 100)) : 0;
  return (
    <div className="usage">
      <div className="usage-head">
        <span>{label ?? "Cache"}</span>
        <span className="muted">{formatBytes(used)} of {formatBytes(budget)} · {pct.toFixed(0)}%</span>
      </div>
      <div className="usage-track" role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
        <div className={`usage-fill${pct > 90 ? " usage-fill-warn" : ""}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

/** Generic, readable renderer for arbitrary status blobs from other components. */
export function JsonView({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (value === null || value === undefined) return <span className="muted">—</span>;
  if (typeof value === "boolean") return <span className={value ? "pill pill-ok" : "pill"}>{value ? "Yes" : "No"}</span>;
  if (typeof value === "number") return <>{formatNumber(value)}</>;
  if (typeof value === "string") return <>{value}</>;
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="muted">Nothing to show</span>;
    if (value.every((v) => typeof v === "string" || typeof v === "number")) {
      return <>{value.join(", ")}</>;
    }
    if (depth > 3) return <pre className="json-raw">{JSON.stringify(value, null, 2)}</pre>;
    return (
      <div className="json-list">
        {value.map((item, i) => (
          <div key={i} className="json-list-item"><JsonView value={item} depth={depth + 1} /></div>
        ))}
      </div>
    );
  }
  if (isPlainObject(value)) {
    const entries = Object.entries(value);
    if (entries.length === 0) return <span className="muted">—</span>;
    const used = Number(value.used ?? value.usedBytes);
    const budget = Number(value.budget ?? value.budgetBytes ?? value.total ?? value.totalBytes);
    return (
      <div>
        {Number.isFinite(used) && Number.isFinite(budget) && budget > 0 ? <UsageBar used={used} budget={budget} /> : null}
        <dl className="kv">
          {entries.map(([k, v]) => (
            <div key={k} className="kv-row">
              <dt>{humanizeKey(k)}</dt>
              <dd>{looksLikeBytes(k, v) ? formatBytes(v) : <JsonView value={v} depth={depth + 1} />}</dd>
            </div>
          ))}
        </dl>
      </div>
    );
  }
  return <>{String(value)}</>;
}

export function StatCard({ title, children, wide }: { title: string; children: ReactNode; wide?: boolean }) {
  return (
    <section className={`card${wide ? " card-wide" : ""}`} aria-label={title}>
      <h3 className="card-title">{title}</h3>
      {children}
    </section>
  );
}

export interface TestState { status: "idle" | "running" | "ok" | "fail"; message?: string; durationMs?: number }

export function TestButton({ label = "Test", state, onTest }: { label?: string; state: TestState; onTest: () => void }) {
  return (
    <span className="test-cell">
      <button type="button" className="btn btn-small" onClick={onTest} disabled={state.status === "running"}>
        {state.status === "running" ? "Testing…" : label}
      </button>
      {state.status === "ok" ? <span className="pill pill-ok">OK{state.durationMs != null ? ` · ${state.durationMs} ms` : ""}</span> : null}
      {state.status === "fail" ? <span className="pill pill-bad" title={state.message}>Failed{state.message ? `: ${state.message}` : ""}</span> : null}
    </span>
  );
}
