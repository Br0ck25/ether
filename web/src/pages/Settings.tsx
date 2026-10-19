import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { ErrorBanner, Field, Notice, Spinner, TestButton, TextInput, UsageBar, type TestState } from "../components";
import { ValueEditor } from "../ConfigEditor";
import { formatBytes } from "../format";
import type { EtherConfig, EtherUser } from "../types";
import UsersPanel from "./UsersPanel";

const SECTIONS: { id: string; title: string; blurb: string }[] = [
  { id: "server", title: "Server", blurb: "Port, name, logging and stream limits." },
  { id: "plex", title: "Plex & remote access", blurb: "Account, claim state, remote access, UPnP and TLS." },
  { id: "usenet", title: "Usenet providers", blurb: "NNTP providers in priority order, with backfill accounts." },
  { id: "indexers", title: "Indexers", blurb: "Newznab indexers and their daily budgets." },
  { id: "cache", title: "Cache & prefetch", blurb: "Disk budget, eviction and next-episode prefetch." },
  { id: "catalog", title: "Catalogue", blurb: "TMDB key, language and browse thresholds." },
  { id: "resolver", title: "Resolver", blurb: "CAM policy, health checks, search cache and blocklists." },
  { id: "quality", title: "Quality profiles", blurb: "Profiles and the rules that pick one per client." },
  { id: "transcode", title: "Transcoding", blurb: "ffmpeg paths, hardware acceleration and remote bitrate." },
  { id: "trakt", title: "Trakt", blurb: "Optional Trakt sync credentials." },
  { id: "opensubtitles", title: "OpenSubtitles", blurb: "Subtitle fallback credentials." },
  { id: "llm", title: "LLM recommendations", blurb: "Optional OpenAI-compatible endpoint. Off by default." },
  { id: "updates", title: "Updates", blurb: "Release channel and auto-update." },
  { id: "spike", title: "Spike (local files)", blurb: "Serve local files for Plex-client verification." },
  { id: "users", title: "Users", blurb: "Per-user stream limits, content-rating caps and languages." },
];

export default function Settings() {
  const params = useParams();
  const sectionId = params.section ?? "server";
  const section = SECTIONS.find((s) => s.id === sectionId) ?? SECTIONS[0];
  const [cfg, setCfg] = useState<EtherConfig | null>(null);
  const [draft, setDraft] = useState<unknown>(null);
  const [error, setError] = useState<unknown>(null);
  const [fieldErrors, setFieldErrors] = useState<string[]>([]);
  const [saved, setSaved] = useState(false);
  const [tests, setTests] = useState<Record<string, TestState>>({});
  const [disk, setDisk] = useState<{ freeBytes: number; totalBytes: number } | null>(null);

  const load = useCallback(async () => {
    try {
      const c = await api.getConfig();
      setCfg(c);
      setDraft(structuredClone((c as unknown as Record<string, unknown>)[sectionKey(section.id)]));
    } catch (e) { setError(e); }
  }, [section.id]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    if (section.id === "cache" && cfg) api.disk(cfg.cache.dir).then(setDisk).catch(() => setDisk(null));
  }, [section.id, cfg?.cache.dir]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    setError(null); setFieldErrors([]); setSaved(false);
    try {
      await api.putSection(section.id, draft);
      setSaved(true);
      await load();
    } catch (e) {
      setError(e);
      if (e instanceof ApiError && e.fields) setFieldErrors(e.fields.map((f) => `${f.field}: ${f.message}`));
    }
  };

  const runTest = async (key: string, fn: () => Promise<{ ok: boolean; error?: string; durationMs: number }>) => {
    setTests((t) => ({ ...t, [key]: { status: "running" } }));
    try {
      const r = await fn();
      setTests((t) => ({ ...t, [key]: r.ok ? { status: "ok", durationMs: r.durationMs } : { status: "fail", message: r.error } }));
    } catch (e) {
      setTests((t) => ({ ...t, [key]: { status: "fail", message: (e as Error).message } }));
    }
  };

  if (!cfg && !error) return <div className="page"><Spinner /></div>;

  return (
    <div className="page settings-page">
      <nav className="settings-nav" aria-label="Settings sections">
        {SECTIONS.map((s) => (
          <Link key={s.id} to={`/settings/${s.id}`} className={s.id === section.id ? "active" : ""}>{s.title}</Link>
        ))}
      </nav>
      <div className="settings-body">
        <h1>{section.title}</h1>
        <p className="muted">{section.blurb}</p>
        <ErrorBanner error={error} />
        {fieldErrors.length ? <div className="banner banner-error" role="alert"><ul>{fieldErrors.map((f) => <li key={f}>{f}</li>)}</ul></div> : null}
        {saved ? <Notice kind="ok">Saved.</Notice> : null}

        {section.id === "users" ? <UsersPanel /> : cfg ? (
          <>
            {section.id === "cache" && disk ? (
              <div className="card">
                <p className="muted">{formatBytes(disk.freeBytes)} free of {formatBytes(disk.totalBytes)} on the cache volume.</p>
                <UsageBar used={(draft as EtherConfig["cache"] | null)?.budgetBytes ?? 0} budget={disk.totalBytes} label="Budget vs. volume" />
              </div>
            ) : null}
            {section.id === "plex" ? (
              <div className="card">
                <p className="muted">
                  {cfg.plex.claimed ? `Claimed${cfg.plex.ownerUsername ? ` by ${cfg.plex.ownerUsername}` : ""}.` : "Not claimed yet — use the setup wizard to sign in."}
                </p>
                <button type="button" className="btn" onClick={async () => { try { await api.setupReopen(); await load(); } catch (e) { setError(e); } }}>
                  Reopen setup wizard
                </button>
              </div>
            ) : null}
            <section className="card">
              <ValueEditor value={draft} onChange={setDraft} path={section.id} />
            </section>
            {renderTesters(section.id, draft, tests, runTest)}
            <div className="wizard-nav">
              <button type="button" className="btn" onClick={load}>Discard changes</button>
              <span />
              <button type="button" className="btn btn-primary" onClick={save}>Save {section.title}</button>
            </div>
          </>
        ) : null}
      </div>
    </div>
  );
}

function sectionKey(id: string): string {
  return id === "opensubtitles" ? "openSubtitles" : id;
}

function renderTesters(section: string, draft: unknown, tests: Record<string, TestState>,
  runTest: (k: string, fn: () => Promise<{ ok: boolean; error?: string; durationMs: number }>) => void) {
  if (section === "usenet") {
    const providers = (draft as EtherConfig["usenet"] | null)?.providers ?? [];
    return <section className="card"><h3>Test providers</h3>{providers.map((p) => (
      <p key={p.id}>{p.name || p.host || p.id} <TestButton state={tests[p.id] ?? { status: "idle" }} onTest={() => runTest(p.id, () => api.testUsenet(p))} /></p>
    ))}</section>;
  }
  if (section === "indexers") {
    const list = (draft as EtherConfig["indexers"]) ?? [];
    return <section className="card"><h3>Test indexers</h3>{list.map((ix) => (
      <p key={ix.id}>{ix.name || ix.url || ix.id} <TestButton state={tests[ix.id] ?? { status: "idle" }} onTest={() => runTest(ix.id, () => api.testIndexer(ix))} /></p>
    ))}</section>;
  }
  if (section === "catalog") {
    return <section className="card"><h3>Test TMDB key</h3><TestButton state={tests.tmdb ?? { status: "idle" }} onTest={() => runTest("tmdb", () => api.testTMDB((draft as EtherConfig["catalog"]).tmdbKey))} /></section>;
  }
  if (section === "opensubtitles") {
    return <section className="card"><h3>Test OpenSubtitles</h3><TestButton state={tests.os ?? { status: "idle" }} onTest={() => runTest("os", () => api.testOpenSubtitles(draft as EtherConfig["openSubtitles"]))} /></section>;
  }
  if (section === "llm") {
    return <section className="card"><h3>Test LLM endpoint</h3><TestButton state={tests.llm ?? { status: "idle" }} onTest={() => runTest("llm", () => api.testLLM(draft as EtherConfig["llm"]))} /></section>;
  }
  if (section === "trakt") {
    return <section className="card"><h3>Test Trakt</h3><TestButton state={tests.trakt ?? { status: "idle" }} onTest={() => runTest("trakt", () => api.testTrakt(draft as EtherConfig["trakt"]))} /></section>;
  }
  return null;
}

export function userPatch(u: EtherUser): Partial<EtherUser> {
  return { streamLimit: u.streamLimit, maxContentRating: u.maxContentRating, subtitleLanguages: u.subtitleLanguages, audioLanguages: u.audioLanguages, allowedSections: u.allowedSections };
}
export { Field, TextInput };
