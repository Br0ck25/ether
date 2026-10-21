import {
  Alert,
  AlertDescription,
  AlertTitle,
  Button,
  Checkbox,
  Input,
} from "@braedonsaunders/appkit-ui";
import { Check, Database, Network, Server } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useAppOptional } from "../../app-context";
import { Drawer } from "../../components/Drawer";
import { Field, SecretInput } from "../../components/fields";
import { api, ApiError, errorMessage } from "../../lib/api";
import type { ClusterSetupRequest } from "../../lib/types";

export function ClusterSetup({
  available,
  onComplete,
}: {
  available: boolean;
  onComplete: () => void;
}) {
  const [mode, setMode] = useState<"create" | "join" | null>(null);
  const app = useAppOptional();
  const configured = app?.boot.setupComplete ?? true;
  return (
    <>
      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-4 rounded-xl border border-border bg-surface p-6">
          <Database size={24} className="text-primary" />
          <div>
            <h2 className="text-base font-semibold text-fg">Create a cluster</h2>
            <p className="mt-1 text-sm text-fg-muted">
              Make this server the first node. Ether backs up your data and moves your settings,
              viewers and history to PostgreSQL.
            </p>
          </div>
          <Button disabled={!available || !configured} onClick={() => setMode("create")}>
            Create cluster
          </Button>
          {!configured ? (
            <p className="text-xs text-fg-muted">
              <Link className="underline" to="/setup">
                Finish this server’s setup
              </Link>{" "}
              before creating a cluster.
            </p>
          ) : null}
        </div>
        <div className="space-y-4 rounded-xl border border-border bg-surface p-6">
          <Network size={24} className="text-primary" />
          <div>
            <h2 className="text-base font-semibold text-fg">Join an existing cluster</h2>
            <p className="mt-1 text-sm text-fg-muted">
              Add this machine using a one-time token from another node. It adopts the cluster’s
              server identity and shared settings.
            </p>
          </div>
          <Button variant="outline" disabled={!available} onClick={() => setMode("join")}>
            Join cluster
          </Button>
        </div>
      </div>
      {!available ? (
        <Alert variant="warning">
          <AlertDescription>
            Guided setup is unavailable because this deployment controls its database through the
            environment.
          </AlertDescription>
        </Alert>
      ) : null}
      <p className="text-sm text-fg-muted">
        You supply a PostgreSQL 14+ database reachable by every node. Ether handles the backup,
        migration and restart automatically. Database hosting and availability are managed
        separately.
      </p>
      {mode ? (
        <SetupWizard
          mode={mode}
          configured={configured}
          onClose={() => setMode(null)}
          onComplete={onComplete}
        />
      ) : null}
    </>
  );
}

function SetupWizard({
  mode,
  configured,
  onClose,
  onComplete,
}: {
  mode: "create" | "join";
  configured: boolean;
  onClose: () => void;
  onComplete: () => void;
}) {
  const [step, setStep] = useState(0);
  const [host, setHost] = useState("");
  const [port, setPort] = useState("5432");
  const [database, setDatabase] = useState("ether");
  const [user, setUser] = useState("ether");
  const [password, setPassword] = useState("");
  const [tls, setTLS] = useState("require");
  const [token, setToken] = useState("");
  const [override, setOverride] = useState("");
  const [replace, setReplace] = useState(false);
  const [busy, setBusy] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("Waiting for Ether to reconnect…");
  const create = mode === "create";
  const databaseURL = `postgresql://${encodeURIComponent(user)}:${encodeURIComponent(password)}@${host.includes(":") && !host.startsWith("[") ? `[${host}]` : host}:${port}/${encodeURIComponent(database)}?sslmode=${tls}`;
  const request: ClusterSetupRequest = {
    mode,
    databaseUrl: create ? databaseURL : override.trim(),
    token: create ? "" : token.trim(),
    replace,
  };
  const valid = create
    ? Boolean(
        host.trim() &&
        user.trim() &&
        database.trim() &&
        Number.isInteger(Number(port)) &&
        Number(port) >= 1 &&
        Number(port) <= 65535,
      )
    : Boolean(token.trim());

  useEffect(() => {
    if (!waiting) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const state = await api.clusterSetupStatus();
        if (cancelled) return;
        if (state.state === "complete") {
          onComplete();
          onClose();
          return;
        }
        if (state.state === "idle") {
          setWaiting(false);
          setStep(0);
          setError("No setup is pending. Check the connection and try again.");
          return;
        }
        if (state.state === "failed") {
          setWaiting(false);
          setStep(0);
          setError(state.message || "Cluster setup failed. Try again.");
          return;
        }
        if (state.message) setMessage(state.message);
      } catch (e) {
        // A joined server now requires the cluster owner's credential.
        if (!cancelled && e instanceof ApiError && (e.status === 401 || e.status === 403)) {
          window.location.reload();
          return;
        }
      }
      if (!cancelled) timer = setTimeout(poll, 2000);
    };
    void poll();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [waiting, onClose, onComplete]);

  const check = async () => {
    setBusy(true);
    setError("");
    try {
      await api.checkClusterSetup(request);
      setStep(1);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const apply = async () => {
    setBusy(true);
    setError("");
    try {
      await api.applyClusterSetup(request);
      setPassword("");
      setToken("");
      setOverride("");
      setWaiting(true);
      setStep(2);
    } catch (e) {
      // A response can be lost as the listener restarts; reconnect before retrying.
      if (e instanceof ApiError && e.status === 0) {
        setWaiting(true);
        setStep(2);
      } else setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer
      open
      onClose={() => {
        if (!busy && !waiting) onClose();
      }}
      title={create ? "Create cluster" : "Join cluster"}
      description="Set up this node without leaving Server Manager."
      size="lg"
      footer={
        waiting ? (
          <Button variant="outline" onClick={() => window.location.reload()}>
            Reconnect
          </Button>
        ) : (
          <div className="flex w-full items-center justify-between gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => (step ? setStep(0) : onClose())}
            >
              {step ? "Back" : "Cancel"}
            </Button>
            <Button
              disabled={busy || !valid || (step === 1 && !create && configured && !replace)}
              onClick={() => void (step === 0 ? check() : apply())}
            >
              {busy
                ? step === 0
                  ? "Checking…"
                  : "Preparing…"
                : step === 0
                  ? "Test connection and continue"
                  : create
                    ? "Create cluster and restart"
                    : "Join cluster and restart"}
            </Button>
          </div>
        )
      }
    >
      <div className="space-y-6">
        <ol aria-label="Cluster setup progress" className="grid grid-cols-3 gap-2 text-xs">
          {["Connect", "Review", "Start"].map((label, index) => (
            <li
              key={label}
              aria-current={step === index ? "step" : undefined}
              className={`flex items-center gap-2 border-t-2 pt-3 ${step >= index ? "border-primary text-fg" : "border-border text-fg-muted"}`}
            >
              <span className="grid size-5 place-items-center rounded-full bg-bg-subtle">
                {step > index ? <Check size={12} /> : index + 1}
              </span>
              {label}
            </li>
          ))}
        </ol>
        {error ? (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        ) : null}
        {step === 0 ? (
          create ? (
            <>
              <div>
                <h3 className="font-medium text-fg">Connect your database</h3>
                <p className="mt-1 text-sm text-fg-muted">
                  Use an empty PostgreSQL database. The connection is tested from this Ether server.
                </p>
              </div>
              <div className="grid grid-cols-[1fr_6rem] gap-3">
                <Field label="Database host" htmlFor="cluster-host">
                  <Input
                    id="cluster-host"
                    value={host}
                    placeholder="db.lan"
                    onChange={(e) => setHost(e.target.value.trim())}
                  />
                </Field>
                <Field label="Port" htmlFor="cluster-port">
                  <Input
                    id="cluster-port"
                    type="number"
                    min={1}
                    max={65535}
                    value={port}
                    onChange={(e) => setPort(e.target.value)}
                  />
                </Field>
              </div>
              <Field label="Database name" htmlFor="cluster-db">
                <Input
                  id="cluster-db"
                  value={database}
                  onChange={(e) => setDatabase(e.target.value)}
                />
              </Field>
              <Field label="Database user" htmlFor="cluster-user">
                <Input
                  id="cluster-user"
                  value={user}
                  onChange={(e) => setUser(e.target.value)}
                  autoComplete="off"
                />
              </Field>
              <Field label="Database password" htmlFor="cluster-password">
                <SecretInput id="cluster-password" value={password} onChange={setPassword} />
              </Field>
              <Field label="Database encryption" htmlFor="cluster-tls">
                <select
                  id="cluster-tls"
                  className="w-full rounded-md border border-border bg-surface px-3 py-2 text-sm"
                  value={tls}
                  onChange={(e) => setTLS(e.target.value)}
                >
                  <option value="require">Require TLS</option>
                  <option value="verify-full">Verify TLS certificate and hostname</option>
                  <option value="disable">No TLS (trusted network only)</option>
                </select>
              </Field>
            </>
          ) : (
            <>
              <div>
                <h3 className="font-medium text-fg">Connect to your cluster</h3>
                <p className="mt-1 text-sm text-fg-muted">
                  On an existing node, open Cluster → Create join token. Copy the token here. It
                  includes the database connection and works once.
                </p>
              </div>
              <Field label="Join token" htmlFor="cluster-token">
                <SecretInput id="cluster-token" value={token} onChange={setToken} />
              </Field>
              <Field
                label="Database URL override (optional)"
                htmlFor="cluster-override"
                hint="Only needed if this node reaches PostgreSQL at a different address."
              >
                <SecretInput id="cluster-override" value={override} onChange={setOverride} />
              </Field>
              {configured ? (
                <label className="flex items-start gap-3 text-sm">
                  <Checkbox checked={replace} onChange={(e) => setReplace(e.target.checked)} />
                  <span>
                    Replace this server’s identity and shared settings with the cluster’s. Keep its
                    local data in an automatic backup.
                  </span>
                </label>
              ) : null}
            </>
          )
        ) : step === 1 ? (
          <>
            <Alert variant="success">
              <AlertTitle>Connection verified</AlertTitle>
              <AlertDescription>
                {create
                  ? "The database is reachable and empty."
                  : "The cluster is reachable and the join token is valid."}
              </AlertDescription>
            </Alert>
            <h3 className="font-medium text-fg">
              Ready to {create ? "create your cluster" : "join this cluster"}
            </h3>
            <ol className="list-decimal space-y-3 pl-5 text-sm text-fg-muted">
              <li>
                Create a portable backup of this server’s database, settings and credential key.
              </li>
              <li>
                Stop this server and its background work safely. Active streams must finish before
                setup can proceed.
              </li>
              <li>
                {create
                  ? `Move your data to ${database} on ${host}. Keep the same server identity in Plex.`
                  : "Adopt the cluster’s identity, viewers and settings. This server’s existing history is not merged."}
              </li>
              <li>
                Restart Ether automatically and reconnect this page.{" "}
                {create
                  ? "Then create a join token to add your next machine."
                  : "Sign in with the cluster owner’s Plex account if prompted."}
              </li>
            </ol>
            <p className="text-xs text-fg-muted">
              Local port, cache paths and transcoding settings stay on this machine. The original
              SQLite database stays in your data directory.
            </p>
          </>
        ) : (
          <div className="space-y-4 py-8 text-center" role="status">
            <Server size={36} className="mx-auto animate-pulse text-primary" />
            <h3 className="text-base font-semibold text-fg">Starting your cluster node</h3>
            <p className="text-sm text-fg-muted">{message}</p>
            <p className="text-xs text-fg-muted">
              The connection may be unavailable while data is copied. This page reconnects
              automatically.
            </p>
          </div>
        )}
      </div>
    </Drawer>
  );
}
