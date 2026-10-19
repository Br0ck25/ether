import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api, ApiError, getToken, setToken } from "./api";
import { ErrorBanner, Spinner } from "./components";
import Dashboard from "./pages/Dashboard";
import Logs from "./pages/Logs";
import Settings from "./pages/Settings";
import Setup from "./pages/Setup";
import Traces from "./pages/Traces";
import type { Bootstrap } from "./types";

const SIGN_IN_TIMEOUT_MS = 10 * 60 * 1000;
const POLL_MS = 2000;

/** Login screen: "Sign in with Plex", with a manual token as a fallback. */
function LoginGate({ boot, onReady }: { boot: Bootstrap; onReady: () => void }) {
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState<{ code: string; authUrl: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const cancelled = useRef(false);
  useEffect(() => () => { cancelled.current = true; }, []);

  const signIn = async () => {
    setError(null); setBusy(true);
    // Open the window inside the click so popup blockers allow it.
    const popup = window.open("", "plex-sign-in", "width=560,height=720");
    try {
      const pin = await api.signInStart();
      setPending({ code: pin.code, authUrl: pin.authUrl });
      if (popup) popup.location.href = pin.authUrl;
      const deadline = Date.now() + SIGN_IN_TIMEOUT_MS;
      while (!cancelled.current && Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, POLL_MS));
        const res = await api.signInPoll(pin.handle);
        if (res.done && res.token) {
          setToken(res.token);
          popup?.close();
          onReady();
          return;
        }
      }
      if (!cancelled.current) setError(new Error("Sign-in timed out. Try again."));
    } catch (err) {
      popup?.close();
      setError(err instanceof ApiError && err.code === "not_owner"
        ? new Error("That Plex account doesn't own this server. Sign in with the account that set Ether up.")
        : err);
    } finally {
      if (!cancelled.current) { setBusy(false); setPending(null); }
    }
  };

  return (
    <main className="gate">
      <div className="card gate-card">
        <h1>{boot.serverName || "Ether"}</h1>
        <p className="muted">Sign in with the Plex account that owns this server.</p>
        <ErrorBanner error={error} />
        {boot.signIn !== false ? (
          <button type="button" className="btn btn-primary btn-plex" onClick={() => void signIn()} disabled={busy}>
            {busy ? "Waiting for Plex…" : "Sign in with Plex"}
          </button>
        ) : null}
        {pending ? (
          <p className="muted small">
            Finish signing in in the Plex window. No window? <a href={pending.authUrl} target="_blank" rel="noreferrer">Open the Plex sign-in page</a>.
          </p>
        ) : null}
        <TokenFallback onReady={onReady} />
        <p className="muted small">On the computer running Ether, open <code>http://localhost:{location.port || "32400"}/ether/</code> to skip sign-in.</p>
      </div>
    </main>
  );
}

function TokenFallback({ onReady }: { onReady: () => void }) {
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
    <details className="token-fallback">
      <summary className="muted small">Use a Plex token instead</summary>
      <form onSubmit={submit}>
        <ErrorBanner error={error} />
        <label className="field">
          <span className="field-label">Owner token (X-Plex-Token)</span>
          <input className="input" type="password" value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" />
        </label>
        <button type="submit" className="btn" disabled={checking || !value.trim()}>{checking ? "Checking…" : "Unlock"}</button>
      </form>
    </details>
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
          <button type="button" className="btn btn-ghost btn-small" onClick={() => { setToken(""); location.reload(); }}>Sign out</button>
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
  if (needsToken) return <LoginGate boot={boot} onReady={() => { setUnlocked(true); void loadBoot(); }} />;

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
