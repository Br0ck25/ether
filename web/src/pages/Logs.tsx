import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { ErrorBanner, Spinner } from "../components";
import type { LogEntry } from "../types";

export default function Logs() {
  const [entries, setEntries] = useState<LogEntry[] | null>(null);
  const [total, setTotal] = useState(0);
  const [level, setLevel] = useState("");
  const [q, setQ] = useState("");
  const [follow, setFollow] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const endRef = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.logs({ limit: 500, level, q });
      setEntries(r.entries); setTotal(r.total); setError(null);
    } catch (e) { setError(e); }
  }, [level, q]);

  useEffect(() => { const t = setTimeout(load, 200); return () => clearTimeout(t); }, [load]);
  useEffect(() => {
    if (!follow) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [follow, load]);
  useEffect(() => { if (follow) endRef.current?.scrollIntoView({ block: "end" }); }, [entries, follow]);

  return (
    <div className="page">
      <div className="page-head"><h1>Logs</h1><span className="muted">{total} entries retained</span></div>
      <ErrorBanner error={error} />
      <div className="toolbar" role="search">
        <input className="input" type="search" placeholder="Filter messages…" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter logs" />
        <select className="input input-narrow" value={level} onChange={(e) => setLevel(e.target.value)} aria-label="Minimum level">
          <option value="">All levels</option><option value="debug">Debug+</option>
          <option value="info">Info+</option><option value="warn">Warnings+</option><option value="error">Errors</option>
        </select>
        <label className="toggle"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Follow</label>
        <button type="button" className="btn btn-small" onClick={load}>Reload</button>
        <button type="button" className="btn btn-small btn-ghost" onClick={async () => { try { await api.clearLogs(); await load(); } catch (e) { setError(e); } }}>Clear</button>
      </div>
      {!entries ? <Spinner /> : entries.length === 0 ? <p className="muted">No log entries match.</p> : (
        <div className="log-view" role="log" aria-label="Server logs">
          {entries.map((e, i) => (
            <div key={i} className="log-line">
              <time>{new Date(e.time).toLocaleTimeString()}</time>
              <span className={`log-level log-${e.level.toLowerCase()}`}>{e.level}</span>
              <span className="log-msg">{e.message}</span>
              {e.attrs && Object.keys(e.attrs).length ? (
                <span className="log-attrs">{Object.entries(e.attrs).map(([k, v]) => `${k}=${v}`).join(" ")}</span>
              ) : null}
            </div>
          ))}
          <div ref={endRef} />
        </div>
      )}
    </div>
  );
}
