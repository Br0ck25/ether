import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, ApiError, newId } from "../api";
import { ErrorBanner, Field, Notice, SecretInput, Spinner, TestButton, TextInput, Toggle, UsageBar, type TestState } from "../components";
import { formatBytes } from "../format";
import type { EtherConfig, Indexer, UsenetProvider } from "../types";

const STEP_TITLES = ["Plex sign-in", "Usenet providers", "Indexers", "Cache", "Optional"];

function blankProvider(): UsenetProvider {
  return { id: newId("prov"), name: "", host: "", port: 563, tls: true, username: "", password: "", connections: 20, priority: 0, backfill: false, enabled: true };
}
function blankIndexer(): Indexer {
  return { id: newId("idx"), name: "", url: "", apiKey: "", dailyLimit: 0, dailyGrabs: 0, priority: 0, enabled: true, movieCategories: null, tvCategories: null };
}

export function canAdvance(step: number, opts: {
  plexDone: boolean; providers: UsenetProvider[]; indexers: Indexer[];
  providerTests: Record<string, TestState>; indexerTests: Record<string, TestState>;
  skipped: Record<number, boolean>;
}): boolean {
  if (opts.skipped[step]) return true;
  switch (step) {
    case 0: return opts.plexDone;
    case 1: {
      const enabled = opts.providers.filter((p) => p.enabled);
      return enabled.length > 0 && enabled.every((p) => p.host.trim() && opts.providerTests[p.id]?.status === "ok");
    }
    case 2: {
      const enabled = opts.indexers.filter((i) => i.enabled);
      return enabled.length > 0 && enabled.every((i) => i.url.trim() && opts.indexerTests[i.id]?.status === "ok");
    }
    default: return true;
  }
}

export default function Setup() {
  const nav = useNavigate();
  const [cfg, setCfg] = useState<EtherConfig | null>(null);
  const [step, setStep] = useState(0);
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [skipped, setSkipped] = useState<Record<number, boolean>>({});
  const [providerTests, setProviderTests] = useState<Record<string, TestState>>({});
  const [indexerTests, setIndexerTests] = useState<Record<string, TestState>>({});
  const [pin, setPin] = useState<{ id: string; code: string; authUrl: string } | null>(null);
  const [pinDone, setPinDone] = useState(false);
  const [claimMsg, setClaimMsg] = useState("");
  const [disk, setDisk] = useState<{ freeBytes: number; totalBytes: number } | null>(null);
  const [optTests, setOptTests] = useState<Record<string, TestState>>({});
  const pollRef = useRef<number | null>(null);

  useEffect(() => {
    api.getConfig().then((c) => {
      if (!c.usenet.providers?.length) c.usenet.providers = [blankProvider()];
      if (!c.indexers?.length) c.indexers = [blankIndexer()];
      setCfg(c);
      setPinDone(Boolean(c.plex.claimed && c.plex.token));
    }).catch(setError);
    return () => { if (pollRef.current) window.clearInterval(pollRef.current); };
  }, []);

  const refreshDisk = useCallback((path: string) => {
    api.disk(path).then(setDisk).catch(() => setDisk(null));
  }, []);
  useEffect(() => { if (step === 3 && cfg) refreshDisk(cfg.cache.dir); }, [step, cfg?.cache.dir]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = useCallback(async (next: EtherConfig) => {
    setSaving(true); setError(null);
    try { setCfg(await api.putConfig(next)); }
    catch (e) { setError(e); throw e; }
    finally { setSaving(false); }
  }, []);

  const startPin = async () => {
    setError(null);
    try {
      const p = await api.pinStart();
      setPin(p);
      window.open(p.authUrl, "_blank", "noopener,noreferrer");
      if (pollRef.current) window.clearInterval(pollRef.current);
      pollRef.current = window.setInterval(async () => {
        try {
          const { done } = await api.pinPoll(p.id);
          if (done) {
            if (pollRef.current) window.clearInterval(pollRef.current);
            setPinDone(true);
            setClaimMsg("Signed in. Claiming this server…");
            try { await api.claim(); setClaimMsg("Server claimed to your Plex account."); }
            catch (e) { setClaimMsg(`Signed in, but claim failed: ${(e as Error).message}`); }
          }
        } catch { /* keep polling until timeout/close */ }
      }, 2000);
    } catch (e) { setError(e); }
  };

  const runTest = async (
    kind: "provider" | "indexer" | "opt", key: string, fn: () => Promise<{ ok: boolean; error?: string; durationMs: number }>,
  ) => {
    const setMap = kind === "provider" ? setProviderTests : kind === "indexer" ? setIndexerTests : setOptTests;
    setMap((m) => ({ ...m, [key]: { status: "running" } }));
    try {
      const r = await fn();
      setMap((m) => ({ ...m, [key]: r.ok ? { status: "ok", durationMs: r.durationMs } : { status: "fail", message: r.error ?? "failed" } }));
    } catch (e) {
      setMap((m) => ({ ...m, [key]: { status: "fail", message: (e as ApiError).message } }));
    }
  };

  const advanceOk = useMemo(() => cfg ? canAdvance(step, {
    plexDone: pinDone || Boolean(cfg.plex.claimed && cfg.plex.token),
    providers: cfg.usenet.providers ?? [], indexers: cfg.indexers ?? [],
    providerTests, indexerTests, skipped,
  }) : false, [cfg, step, pinDone, providerTests, indexerTests, skipped]);

  if (error && !cfg) return <div className="page"><h1>Set up Ether</h1><ErrorBanner error={error} /></div>;
  if (!cfg) return <div className="page"><Spinner label="Loading configuration…" /></div>;

  const updateProviders = (providers: UsenetProvider[]) => setCfg({ ...cfg, usenet: { providers } });
  const updateIndexers = (indexers: Indexer[]) => setCfg({ ...cfg, indexers });

  const next = async () => {
    try {
      await save(cfg);
      if (step < STEP_TITLES.length - 1) setStep(step + 1);
      else {
        await api.setupComplete(Object.values(skipped).some(Boolean));
        nav("/");
      }
    } catch { /* error banner shows details */ }
  };

  return (
    <div className="page page-narrow">
      <h1>Set up Ether</h1>
      <ol className="steps" aria-label="Setup progress">
        {STEP_TITLES.map((t, i) => (
          <li key={t} className={`step${i === step ? " step-current" : ""}${i < step ? " step-done" : ""}`}>
            <button type="button" className="step-btn" disabled={i > step} onClick={() => setStep(i)}>
              <span className="step-num">{i + 1}</span> {t}
            </button>
          </li>
        ))}
      </ol>
      <ErrorBanner error={error} />

      {step === 0 && (
        <section className="card">
          <h2>Sign in to plex.tv and claim this server</h2>
          <p className="muted">Ether registers with your Plex account like a fresh Plex Media Server, so the official Plex apps can find it.</p>
          {pinDone || cfg.plex.claimed ? <Notice kind="ok">Signed in{cfg.plex.ownerUsername ? ` as ${cfg.plex.ownerUsername}` : ""}. {claimMsg}</Notice> : null}
          {pin ? (
            <div>
              <p>Enter this code at plex.tv: <strong className="pin-code">{pin.code}</strong></p>
              <p><a href={pin.authUrl} target="_blank" rel="noreferrer">Open plex.tv sign-in</a> — waiting for approval… <span className="spinner" aria-hidden /></p>
            </div>
          ) : (
            <button type="button" className="btn btn-primary" onClick={startPin}>Sign in to plex.tv</button>
          )}
          {claimMsg && !cfg.plex.claimed ? <p className="muted">{claimMsg}</p> : null}
          <button type="button" className="btn" onClick={async () => { try { await api.claim(); setPinDone(true); setClaimMsg("Server claimed."); } catch (e) { setError(e); } }}>
            Claim server
          </button>
        </section>
      )}

      {step === 1 && (
        <section className="card">
          <h2>Usenet providers</h2>
          <p className="muted">Add at least one provider. Test each one before continuing; block accounts can be marked as backfill.</p>
          {(cfg.usenet.providers ?? []).map((p, i) => (
            <fieldset key={p.id || i} className="group">
              <legend>{p.name || `Provider ${i + 1}`}</legend>
              <div className="grid2">
                <Field label="Name"><TextInput value={p.name} onChange={(e) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, name: e.target.value } : x))} /></Field>
                <Field label="Host"><TextInput value={p.host} placeholder="news.example.com" onChange={(e) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, host: e.target.value } : x))} /></Field>
                <Field label="Port"><TextInput type="number" value={p.port} onChange={(e) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, port: e.target.valueAsNumber || 0 } : x))} /></Field>
                <Field label="Connections"><TextInput type="number" value={p.connections} onChange={(e) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, connections: e.target.valueAsNumber || 0 } : x))} /></Field>
                <Field label="Username"><TextInput value={p.username} autoComplete="username" onChange={(e) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, username: e.target.value } : x))} /></Field>
                <Field label="Password"><SecretInput value={p.password} onChange={(v) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, password: v } : x))} /></Field>
                <Field label="Priority" hint="Lower is tried first."><TextInput type="number" value={p.priority} onChange={(e) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, priority: e.target.valueAsNumber || 0 } : x))} /></Field>
              </div>
              <div className="toggles">
                <Toggle label="SSL (TLS)" checked={p.tls} onChange={(v) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, tls: v } : x))} />
                <Toggle label="Backfill only" checked={p.backfill} onChange={(v) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, backfill: v } : x))} />
                <Toggle label="Enabled" checked={p.enabled} onChange={(v) => updateProviders((cfg.usenet.providers ?? []).map((x, j) => j === i ? { ...x, enabled: v } : x))} />
              </div>
              <div className="row-actions">
                <TestButton state={providerTests[p.id] ?? { status: "idle" }} onTest={() => runTest("provider", p.id, () => api.testUsenet(p))} />
                <button type="button" className="btn btn-ghost btn-small" onClick={() => updateProviders((cfg.usenet.providers ?? []).filter((_, j) => j !== i))}>Remove</button>
              </div>
            </fieldset>
          ))}
          <button type="button" className="btn" onClick={() => updateProviders([...(cfg.usenet.providers ?? []), blankProvider()])}>Add provider</button>
        </section>
      )}

      {step === 2 && (
        <section className="card">
          <h2>Newznab indexers</h2>
          <p className="muted">Add at least one indexer. Daily limits keep Ether inside your API allowance (0 = unlimited).</p>
          {(cfg.indexers ?? []).map((ix, i) => (
            <fieldset key={ix.id || i} className="group">
              <legend>{ix.name || `Indexer ${i + 1}`}</legend>
              <div className="grid2">
                <Field label="Name"><TextInput value={ix.name} onChange={(e) => updateIndexers((cfg.indexers ?? []).map((x, j) => j === i ? { ...x, name: e.target.value } : x))} /></Field>
                <Field label="Base URL" hint="/api is appended automatically."><TextInput value={ix.url} placeholder="https://indexer.example.com" onChange={(e) => updateIndexers((cfg.indexers ?? []).map((x, j) => j === i ? { ...x, url: e.target.value } : x))} /></Field>
                <Field label="API key"><SecretInput value={ix.apiKey} onChange={(v) => updateIndexers((cfg.indexers ?? []).map((x, j) => j === i ? { ...x, apiKey: v } : x))} /></Field>
                <Field label="Daily API limit"><TextInput type="number" value={ix.dailyLimit} onChange={(e) => updateIndexers((cfg.indexers ?? []).map((x, j) => j === i ? { ...x, dailyLimit: e.target.valueAsNumber || 0 } : x))} /></Field>
                <Field label="Daily grabs"><TextInput type="number" value={ix.dailyGrabs} onChange={(e) => updateIndexers((cfg.indexers ?? []).map((x, j) => j === i ? { ...x, dailyGrabs: e.target.valueAsNumber || 0 } : x))} /></Field>
                <Field label="Priority"><TextInput type="number" value={ix.priority} onChange={(e) => updateIndexers((cfg.indexers ?? []).map((x, j) => j === i ? { ...x, priority: e.target.valueAsNumber || 0 } : x))} /></Field>
              </div>
              <div className="toggles"><Toggle label="Enabled" checked={ix.enabled} onChange={(v) => updateIndexers((cfg.indexers ?? []).map((x, j) => j === i ? { ...x, enabled: v } : x))} /></div>
              <div className="row-actions">
                <TestButton state={indexerTests[ix.id] ?? { status: "idle" }} onTest={() => runTest("indexer", ix.id, () => api.testIndexer(ix))} />
                <button type="button" className="btn btn-ghost btn-small" onClick={() => updateIndexers((cfg.indexers ?? []).filter((_, j) => j !== i))}>Remove</button>
              </div>
            </fieldset>
          ))}
          <button type="button" className="btn" onClick={() => updateIndexers([...(cfg.indexers ?? []), blankIndexer()])}>Add indexer</button>
        </section>
      )}

      {step === 3 && (
        <section className="card">
          <h2>Cache folder and disk budget</h2>
          <Field label="Cache folder">
            <span className="inline-input">
              <TextInput value={cfg.cache.dir} onChange={(e) => setCfg({ ...cfg, cache: { ...cfg.cache, dir: e.target.value } })} />
              <button type="button" className="btn btn-small" onClick={() => refreshDisk(cfg.cache.dir)}>Check space</button>
            </span>
          </Field>
          {disk ? <p className="muted">{formatBytes(disk.freeBytes)} free of {formatBytes(disk.totalBytes)} on this volume.</p> : null}
          <Field label={`Disk budget: ${formatBytes(cfg.cache.budgetBytes)}`}>
            <input type="range" min={1 << 30} max={disk ? Math.max(1 << 30, disk.freeBytes) : 500 * (1 << 30)} step={1 << 30}
              value={cfg.cache.budgetBytes} aria-label="Disk budget in bytes"
              onChange={(e) => setCfg({ ...cfg, cache: { ...cfg.cache, budgetBytes: e.target.valueAsNumber } })} />
          </Field>
          <Field label="Budget (bytes)">
            <TextInput type="number" value={cfg.cache.budgetBytes} onChange={(e) => setCfg({ ...cfg, cache: { ...cfg.cache, budgetBytes: e.target.valueAsNumber || 0 } })} />
          </Field>
          {disk ? <UsageBar used={cfg.cache.budgetBytes} budget={disk.totalBytes} label="Budget vs. volume" /> : null}
          <div className="toggles">
            <Toggle label="Prefetch next episode" checked={cfg.cache.prefetchNextEpisode} onChange={(v) => setCfg({ ...cfg, cache: { ...cfg.cache, prefetchNextEpisode: v } })} />
            <Toggle label="Keep in-progress titles warm" checked={cfg.cache.keepInProgressWarm} onChange={(v) => setCfg({ ...cfg, cache: { ...cfg.cache, keepInProgressWarm: v } })} />
          </div>
        </section>
      )}

      {step === 4 && (
        <section className="card">
          <h2>Optional integrations</h2>
          <p className="muted">All of these can be set up later in Settings.</p>
          <div className="grid2">
            <Field label="Your TMDB API key" hint="Leave empty to use the bundled key.">
              <span className="inline-input"><SecretInput value={cfg.catalog.tmdbKey} onChange={(v) => setCfg({ ...cfg, catalog: { ...cfg.catalog, tmdbKey: v } })} />
                <TestButton state={optTests.tmdb ?? { status: "idle" }} onTest={() => runTest("opt", "tmdb", () => api.testTMDB(cfg.catalog.tmdbKey))} /></span>
            </Field>
            <Field label="Trakt client ID">
              <TextInput value={cfg.trakt.clientId} onChange={(e) => setCfg({ ...cfg, trakt: { ...cfg.trakt, clientId: e.target.value, enabled: Boolean(e.target.value) } })} />
            </Field>
            <Field label="OpenSubtitles API key">
              <SecretInput value={cfg.openSubtitles.apiKey} onChange={(v) => setCfg({ ...cfg, openSubtitles: { ...cfg.openSubtitles, apiKey: v, enabled: Boolean(v) } })} />
            </Field>
            <Field label="LLM endpoint (OpenAI-compatible)" hint="e.g. http://localhost:11434/v1">
              <span className="inline-input"><TextInput value={cfg.llm.baseUrl} onChange={(e) => setCfg({ ...cfg, llm: { ...cfg.llm, baseUrl: e.target.value, enabled: Boolean(e.target.value) } })} />
                <TestButton state={optTests.llm ?? { status: "idle" }} onTest={() => runTest("opt", "llm", () => api.testLLM(cfg.llm))} /></span>
            </Field>
            <Field label="LLM model">
              <TextInput value={cfg.llm.model} onChange={(e) => setCfg({ ...cfg, llm: { ...cfg.llm, model: e.target.value } })} />
            </Field>
          </div>
          <div className="toggles">
            <Toggle label="Allow CAM / telesync releases" checked={cfg.resolver.allowCam} onChange={(v) => setCfg({ ...cfg, resolver: { ...cfg.resolver, allowCam: v } })} />
          </div>
          <Notice kind="info">Finishing setup locks this admin UI to your owner Plex token. You can reopen setup later from Settings.</Notice>
        </section>
      )}

      <div className="wizard-nav">
        <button type="button" className="btn" disabled={step === 0} onClick={() => setStep(step - 1)}>Back</button>
        {step < 3 ? (
          <label className="skip">
            <input type="checkbox" checked={Boolean(skipped[step])} onChange={(e) => setSkipped({ ...skipped, [step]: e.target.checked })} />
            Skip test for this step (advanced)
          </label>
        ) : <span />}
        <button type="button" className="btn btn-primary" disabled={!advanceOk || saving} onClick={next}>
          {saving ? "Saving…" : step === STEP_TITLES.length - 1 ? "Finish setup" : "Save and continue"}
        </button>
      </div>
      {!advanceOk && step < 3 ? <p className="muted right">Complete and test this step to continue — or use the skip option.</p> : null}
    </div>
  );
}
