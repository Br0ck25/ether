import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { ErrorBanner, JsonView, Notice, Spinner, StatCard } from "../components";
import { humanizeKey } from "../format";
import type { SetupStatus } from "../types";

const TITLES: Record<string, string> = {
  sessions: "Now playing", cache: "Cache", providers: "Usenet providers",
  indexers: "Indexer budgets", catalog: "Catalogue", remote: "Remote access",
  hwaccel: "Hardware acceleration", updates: "Updates", traces: "Resolver activity",
};
const ORDER = ["sessions", "cache", "providers", "indexers", "catalog", "remote", "hwaccel", "updates", "traces"];

export default function Dashboard() {
  const [data, setData] = useState<{ sections: Record<string, unknown>; names: string[]; version: string } | null>(null);
  const [setup, setSetup] = useState<SetupStatus | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      const [dash, st] = await Promise.all([api.dashboard(), api.setupStatus()]);
      setData(dash); setSetup(st); setError(null);
    } catch (e) { setError(e); }
    finally { setRefreshing(false); }
  }, []);

  useEffect(() => { void load(); }, [load]);

  if (!data && !error) return <div className="page"><Spinner /></div>;
  const names = [...ORDER.filter((n) => data?.names.includes(n)), ...(data?.names.filter((n) => !ORDER.includes(n)) ?? [])];

  return (
    <div className="page">
      <div className="page-head">
        <h1>Dashboard</h1>
        <button type="button" className="btn btn-small" onClick={load} disabled={refreshing}>{refreshing ? "Refreshing…" : "Refresh"}</button>
      </div>
      <ErrorBanner error={error} />
      {setup && !setup.setupComplete ? (
        <Notice kind="warn">Setup is not finished. <Link to="/setup">Continue the setup wizard</Link> so Plex apps can find and play from Ether.</Notice>
      ) : null}
      {data && names.length === 0 ? (
        <Notice kind="info">No live status providers are wired into this build yet. Configuration, users and logs are available from the navigation.</Notice>
      ) : null}
      <div className="cards">
        {names.map((name) => (
          <StatCard key={name} title={TITLES[name] ?? humanizeKey(name)} wide={name === "sessions" || name === "traces"}>
            <JsonView value={data?.sections[name]} />
          </StatCard>
        ))}
        <StatCard title="Server">
          <dl className="kv">
            <div className="kv-row"><dt>Version</dt><dd>{data?.version ?? "—"}</dd></div>
            <div className="kv-row"><dt>Setup</dt><dd>{setup?.setupComplete ? "Complete" : "In progress"}</dd></div>
            {setup?.steps.map((s) => (
              <div key={s.id} className="kv-row"><dt>{s.title}</dt><dd><span className={s.complete ? "pill pill-ok" : "pill"}>{s.complete ? "Done" : "Pending"}</span> <span className="muted">{s.detail}</span></dd></div>
            ))}
          </dl>
        </StatCard>
      </div>
    </div>
  );
}
