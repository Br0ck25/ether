import { Button, SettingsShell, Spinner, toast, type SettingsNavGroup } from "@braedonsaunders/appkit-ui";
import {
  ArrowDownToLine, Bot, Captions, Cpu, Film, Gauge, HardDrive, Layers, Network, Puzzle, RotateCcw, Save, Search,
  Server, ShieldCheck, SlidersHorizontal, Tv, Users,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useApp } from "../../app-context";
import { ErrorPanel, MutationError } from "../../components/feedback";
import { TestButton, type TestState } from "../../components/fields";
import { api, ApiError } from "../../lib/api";
import { humanizeKey } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import type { EtherConfig } from "../../lib/types";
import { asObj, SchemaGroups, type FieldGroup } from "./SchemaForm";
import * as S from "./schema";
import { CacheStatus, IndexersSection, PlexStatus, QualitySection, TranscodeStatus, UsenetSection, UsersSection, type RunTest } from "./sections";

interface SectionDef { id: string; key: string; label: string; description: string; icon: ReactNode; schema?: FieldGroup[] }

const SECTIONS: SectionDef[] = [
  { id: "server", key: "server", label: "Server", description: "Name, port, stream limits and logging.", icon: <Server />, schema: S.SERVER },
  { id: "plex", key: "plex", label: "Plex & remote access", description: "Your Plex account, remote access and secure connections.", icon: <Tv />, schema: S.PLEX },
  { id: "users", key: "", label: "Users", description: "Stream limits, content-rating caps and languages for each person.", icon: <Users /> },
  { id: "usenet", key: "usenet", label: "Usenet providers", description: "NNTP providers in priority order, with backfill accounts.", icon: <Network /> },
  { id: "indexers", key: "indexers", label: "Indexers", description: "Newznab indexers and their daily budgets.", icon: <Search /> },
  { id: "cache", key: "cache", label: "Cache & prefetch", description: "Disk budget and what gets pulled ahead.", icon: <HardDrive />, schema: S.CACHE },
  { id: "resolver", key: "resolver", label: "Release selection", description: "How releases are picked, health-checked and blocked.", icon: <ShieldCheck />, schema: S.RESOLVER },
  { id: "quality", key: "quality", label: "Quality profiles", description: "Profiles and the rules that choose one per device.", icon: <SlidersHorizontal /> },
  { id: "transcode", key: "transcode", label: "Transcoding", description: "Hardware acceleration and remote bitrate.", icon: <Cpu />, schema: S.TRANSCODE },
  { id: "catalog", key: "catalog", label: "Catalogue", description: "TMDB, language and what's browsable.", icon: <Film />, schema: S.CATALOG },
  { id: "llm", key: "llm", label: "LLM", description: "The optional language model behind “Describe it” and themed rows.", icon: <Bot />, schema: S.LLM },
  { id: "trakt", key: "trakt", label: "Trakt", description: "Optional Trakt sync.", icon: <Layers />, schema: S.TRAKT },
  { id: "opensubtitles", key: "openSubtitles", label: "OpenSubtitles", description: "Subtitle fallback.", icon: <Captions />, schema: S.OPENSUBTITLES },
  { id: "updates", key: "updates", label: "Updates", description: "Release channel and automatic updates.", icon: <ArrowDownToLine />, schema: S.UPDATES },
  { id: "spike", key: "spike", label: "Local files", description: "Play local files to troubleshoot a Plex app.", icon: <Gauge />, schema: S.SPIKE },
];

const GROUPS: { label: string; ids: string[] }[] = [
  { label: "Server", ids: ["server", "plex", "users"] },
  { label: "Streaming", ids: ["usenet", "indexers", "cache", "resolver", "quality", "transcode"] },
  { label: "Catalogue", ids: ["catalog"] },
  { label: "Integrations", ids: ["llm", "trakt", "opensubtitles"] },
  { label: "System", ids: ["updates", "spike"] },
];

const KNOWN_KEYS = new Set(["server", "plex", "usenet", "indexers", "cache", "catalog", "resolver", "quality", "transcode", "trakt", "openSubtitles", "llm", "updates", "spike", "setupComplete"]);
const SECTION_ENDPOINT = new Set(["server", "plex", "usenet", "indexers", "cache", "catalog", "resolver", "quality", "transcode", "trakt", "opensubtitles", "llm", "updates", "spike"]);

export default function Settings() {
  const { section: param } = useParams();
  const navigate = useNavigate();
  const { dashboard, refreshDashboard, reloadBoot } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const cfg = config.data;

  // Sections the server has that this UI has no dedicated form for.
  const extraSections = useMemo<SectionDef[]>(() => Object.keys(cfg ?? {}).filter((k) => !KNOWN_KEYS.has(k) && typeof (cfg as unknown as Record<string, unknown>)[k] === "object")
    .map((k) => ({ id: `x-${k}`, key: k, label: humanizeKey(k), description: `${humanizeKey(k)} options.`, icon: <Puzzle /> })), [cfg]);
  const all = [...SECTIONS, ...extraSections];
  const section = all.find((s) => s.id === param) ?? SECTIONS[0]!;

  const nav: SettingsNavGroup[] = [
    ...GROUPS.map((g) => ({ label: g.label, items: g.ids.map((id) => SECTIONS.find((s) => s.id === id)!).map((s) => ({ key: s.id, label: s.label, icon: s.icon })) })),
    ...(extraSections.length ? [{ label: "More", items: extraSections.map((s) => ({ key: s.id, label: s.label, icon: s.icon })) }] : []),
  ];

  const [draft, setDraft] = useState<unknown>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saveError, setSaveError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [tests, setTests] = useState<Record<string, TestState>>({});

  const stored = useMemo(() => (cfg && section.key ? structuredClone((cfg as unknown as Record<string, unknown>)[section.key]) : null), [cfg, section.key]);
  useEffect(() => { setDraft(stored); setErrors({}); setSaveError(null); }, [stored]);
  const dirty = section.key !== "" && JSON.stringify(draft) !== JSON.stringify(stored);

  const runTest: RunTest = useCallback((key, fn) => {
    setTests((t) => ({ ...t, [key]: { status: "running" } }));
    fn().then(
      (r) => setTests((t) => ({ ...t, [key]: r.ok ? { status: "ok", durationMs: r.durationMs } : { status: "fail", message: r.error ?? "failed" } })),
      (e) => setTests((t) => ({ ...t, [key]: { status: "fail", message: (e as Error).message } })),
    );
  }, []);

  const save = async () => {
    if (!cfg) return;
    setSaving(true); setSaveError(null); setErrors({});
    try {
      if (SECTION_ENDPOINT.has(section.id)) await api.putSection(section.id, draft);
      else await api.putConfig({ ...cfg, [section.key]: draft } as EtherConfig);
      toast.success("Settings saved", { description: section.label });
      await config.reload();
      void refreshDashboard();
      if (section.id === "server") void reloadBoot();
    } catch (e) {
      setSaveError(e);
      if (e instanceof ApiError && e.fields) setErrors(Object.fromEntries(e.fields.map((f) => [f.field, f.message])));
    } finally { setSaving(false); }
  };

  const footer = (
    <>
      <Button variant="ghost" size="sm" onClick={() => { setDraft(stored); setErrors({}); setSaveError(null); }} disabled={!dirty || saving}><RotateCcw size={14} /> Discard</Button>
      <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}><Save size={14} /> {saving ? "Saving…" : "Save changes"}</Button>
    </>
  );

  const sections = dashboard?.sections ?? {};
  let body: ReactNode;
  if (config.error) body = <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  else if (!cfg || (section.key && draft === null && stored !== null)) body = <div className="py-10 text-center"><Spinner size={20} label="Loading settings" /></div>;
  else if (section.id === "users") body = <UsersSection defaultStreamLimit={cfg.server.defaultStreamLimit} />;
  else if (section.id === "usenet") body = <UsenetSection value={draft as EtherConfig["usenet"]} onChange={setDraft} errors={errors} tests={tests} runTest={runTest} status={sections.providers} footer={footer} />;
  else if (section.id === "indexers") body = <IndexersSection value={(draft as EtherConfig["indexers"]) ?? []} onChange={setDraft} errors={errors} tests={tests} runTest={runTest} status={sections.indexers} footer={footer} />;
  else if (section.id === "quality") body = <QualitySection value={draft as EtherConfig["quality"]} onChange={setDraft} errors={errors} footer={footer} />;
  else {
    const prefix = section.key;
    const testFor: Record<string, ReactNode> = {
      catalog: <TestButton label="Test TMDB key" state={tests.tmdb ?? { status: "idle" }} onTest={() => runTest("tmdb", () => api.testTMDB(String(asObj(draft).tmdbKey ?? "")))} />,
      llm: <TestButton label="Test endpoint" state={tests.llm ?? { status: "idle" }} onTest={() => runTest("llm", () => api.testLLM(draft as EtherConfig["llm"]))} />,
      trakt: <TestButton label="Test Trakt" state={tests.trakt ?? { status: "idle" }} onTest={() => runTest("trakt", () => api.testTrakt(draft as EtherConfig["trakt"]))} />,
      opensubtitles: <TestButton label="Test OpenSubtitles" state={tests.os ?? { status: "idle" }} onTest={() => runTest("os", () => api.testOpenSubtitles(draft as EtherConfig["openSubtitles"]))} />,
    };
    body = (
      <>
        {section.id === "plex" ? <PlexStatus cfg={cfg} remote={sections.remote} onReopen={async () => { try { await api.setupReopen(); await reloadBoot(); navigate("/setup"); } catch (e) { toast.error("Couldn't reopen setup", { description: (e as Error).message }); } }} /> : null}
        {section.id === "cache" ? <CacheStatus dir={String(asObj(draft).dir ?? cfg.cache.dir)} budget={Number(asObj(draft).budgetBytes ?? cfg.cache.budgetBytes)} status={sections.cache} /> : null}
        {section.id === "transcode" ? <TranscodeStatus status={sections.hwaccel} /> : null}
        <SchemaGroups groups={section.schema ?? []} value={asObj(draft)} onChange={setDraft} errors={errors} prefix={prefix} footer={<>{testFor[section.id] ? <span className="mr-auto">{testFor[section.id]}</span> : null}{footer}</>} />
      </>
    );
  }

  return (
    <div className="h-full">
      <SettingsShell
        title="Settings"
        description="Everything about how Ether runs. Viewers never see any of this."
        nav={nav}
        activeKey={section.id}
        onSelect={(k) => navigate(`/settings/${k}`)}
        contentWidth={section.id === "users" ? "wide" : "narrow"}
        linkRender={({ href, children, className }) => <Link to={href} className={className}>{children}</Link>}
        actions={dirty ? <span className="text-xs text-warning">Unsaved changes</span> : null}
      >
        <div>
          <h2 className="text-lg font-semibold text-fg">{section.label}</h2>
          <p className="text-sm text-fg-muted">{section.description}</p>
        </div>
        <MutationError error={saveError} />
        {body}
      </SettingsShell>
    </div>
  );
}

