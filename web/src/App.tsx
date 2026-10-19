import { useCallback, useEffect, useState, type ReactNode } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api, ApiError, getToken, setToken } from "./api";
import { ErrorBanner, Spinner } from "./components";
import Dashboard from "./pages/Dashboard";
import Logs from "./pages/Logs";
import Settings from "./pages/Settings";
import Setup from "./pages/Setup";
import Traces from "./pages/Traces";
import type { Bootstrap } from "./types";

function TokenGate({ onReady }: { onReady: () => void }) {
  const [value, setValue] = useState(getToken());
  const [error, setError] = useState<unknown>(null);
  const [checking, setChecking] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setChecking(true); setError(null);
    setToken(value.trim());
    try {
      await api.setupStatus(); // cheap authenticated probe
      onReady();
    } catch (err) {
      setToken("");
      setError(err instanceof ApiError && err.status === 401 ? new Error("That token was not accepted as the owner token.") : err);
    } finally { setChecking(false); }
  };
  return (
    <main className="gate">
      <form className="card gate-card" onSubmit={submit}>
        <h1>Ether admin</h1>
        <p className="muted">This server is set up. Enter the owner Plex token (<code>X-Plex-Token</code>) to manage it. The token is stored only in this browser and sent as a header — never as a cookie.</p>
        <ErrorBanner error={error} />
        <label className="field">
          <span className="field-label">Owner token</span>
          <input className="input" type="password" value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" autoFocus />
        </label>
        <button type="submit" className="btn btn-primary" disabled={checking || !value.trim()}>{checking ? "Checking…" : "Unlock"}</button>
      </form>
    </main>
  );
}

function Shell({ boot, children }: { boot: Bootstrap; children: ReactNode }) {
  return (
    <div className="shell">
      <header className="topbar">
        <span className="brand">Ether</span>
        <span className="muted topbar-name">{boot.serverName} · v{boot.version}</span>
        <nav className="mainnav" aria-label="Main">
          {!boot.setupComplete ? <NavLink to="/setup">Setup</NavLink> : null}
          <NavLink to="/" end>Dashboard</NavLink>
          <NavLink to="/settings/server">Settings</NavLink>
          <NavLink to="/logs">Logs</NavLink>
          <NavLink to="/traces">Resolver traces</NavLink>
        </nav>
        {boot.authRequired ? (
          <button type="button" className="btn btn-ghost btn-small" onClick={() => { setToken(""); location.reload(); }}>Lock</button>
        ) : null}
      </header>
      <main>{children}</main>
    </div>
  );
}

export default function App() {
  const [boot, setBoot] = useState<Bootstrap | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [unlocked, setUnlocked] = useState(false);
  const location = useLocation();

  const loadBoot = useCallback(async () => {
    try { setBoot(await api.bootstrap()); setError(null); }
    catch (e) { setError(e); }
  }, []);
  useEffect(() => { void loadBoot(); }, [loadBoot, location.pathname]);

  if (!boot) {
    return <main className="gate"><div className="card gate-card">{error ? <ErrorBanner error={error} /> : <Spinner label="Connecting to Ether…" />}</div></main>;
  }
  const needsToken = boot.authRequired && !boot.authenticated && !unlocked;
  if (needsToken) return <TokenGate onReady={() => { setUnlocked(true); void loadBoot(); }} />;

  return (
    <Shell boot={boot}>
      <Routes>
        <Route path="/" element={boot.setupComplete ? <Dashboard /> : <Navigate to="/setup" replace />} />
        <Route path="/setup" element={<Setup />} />
        <Route path="/settings" element={<Navigate to="/settings/server" replace />} />
        <Route path="/settings/:section" element={<Settings />} />
        <Route path="/logs" element={<Logs />} />
        <Route path="/traces" element={<Traces />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  );
}
