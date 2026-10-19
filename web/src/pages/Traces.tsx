import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api";
import { ErrorBanner, JsonView, Notice, Spinner, StatCard } from "../components";
import { isPlainObject } from "../format";

/** Resolver traces: why a release was picked or rejected. The resolver is
 * built concurrently, so traces render generically with sensible
 * highlighting for known pick/reject field names. */
export default function Traces() {
  const [data, setData] = useState<unknown>(null);
  const [error, setError] = useState<unknown>(null);
  const [notWired, setNotWired] = useState(false);
  const [q, setQ] = useState("");

  const load = useCallback(async () => {
    try { setData((await api.traces()).traces); setError(null); setNotWired(false); }
    catch (e) {
      if (e instanceof ApiError && e.status === 501) setNotWired(true);
      else setError(e);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const traces = useMemo(() => normalizeTraces(data), [data]);
  const visible = useMemo(() => {
    if (!q.trim()) return traces;
    const needle = q.toLowerCase();
    return traces.filter((t) => JSON.stringify(t).toLowerCase().includes(needle));
  }, [traces, q]);

  if (notWired) return <div className="page"><h1>Resolver traces</h1><Notice kind="info">Resolver traces are not wired into this build yet. Once the resolver records decisions, each title's picked and rejected releases — with reasons — will appear here.</Notice></div>;
  if (data === null && !error) return <div className="page"><Spinner /></div>;

  return (
    <div className="page">
      <div className="page-head"><h1>Resolver traces</h1><button type="button" className="btn btn-small" onClick={load}>Refresh</button></div>
      <p className="muted">Why each release was picked or rejected for a title: profile match, size sanity, health checks and CAM policy.</p>
      <ErrorBanner error={error} />
      <div className="toolbar"><input className="input" type="search" placeholder="Filter by title or release…" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter traces" /></div>
      {visible.length === 0 ? <p className="muted">No traces recorded yet. Play a title to generate one.</p> : (
        <div className="cards">
          {visible.map((t, i) => (
            <StatCard key={i} title={traceTitle(t, i)} wide>
              <TraceView trace={t} />
            </StatCard>
          ))}
        </div>
      )}
    </div>
  );
}

function normalizeTraces(data: unknown): Record<string, unknown>[] {
  if (Array.isArray(data)) return data.filter(isPlainObject);
  if (isPlainObject(data)) {
    if (Array.isArray(data.traces)) return data.traces.filter(isPlainObject);
    if (Array.isArray(data.items)) return data.items.filter(isPlainObject);
    return [data];
  }
  return [];
}

function traceTitle(t: Record<string, unknown>, i: number): string {
  const title = t.title ?? t.name ?? t.key ?? t.ratingKey;
  return typeof title === "string" && title ? title : `Trace ${i + 1}`;
}

function verdictOf(v: unknown): { label: string; cls: string } | null {
  if (!isPlainObject(v)) return null;
  const picked = v.picked ?? v.selected ?? v.chosen ?? v.accepted;
  if (picked === true) return { label: "Picked", cls: "pill-ok" };
  if (picked === false) return { label: "Rejected", cls: "pill-bad" };
  const status = String(v.status ?? v.verdict ?? "").toLowerCase();
  if (status.includes("pick") || status.includes("accept") || status === "ok") return { label: status, cls: "pill-ok" };
  if (status.includes("reject") || status.includes("fail") || status.includes("bad")) return { label: status, cls: "pill-bad" };
  return null;
}

function TraceView({ trace }: { trace: Record<string, unknown> }) {
  const candidates = [trace.candidates, trace.releases, trace.results].find(Array.isArray) as unknown[] | undefined;
  const rest = { ...trace };
  delete rest.candidates; delete rest.releases; delete rest.results;
  return (
    <div>
      <JsonView value={rest} />
      {candidates?.length ? (
        <div className="json-list">
          {candidates.map((c, i) => {
            const verdict = verdictOf(c);
            return (
              <div key={i} className="json-list-item">
                {verdict ? <span className={`pill ${verdict.cls}`}>{verdict.label}</span> : null}
                <JsonView value={c} />
              </div>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
