import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  EmptyState,
  SearchSelect,
  SettingsSection,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Cloud, Plus } from "lucide-react";
import { useCallback, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useApp } from "../../app-context";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { SecretInput, TestButton } from "../../components/fields";
import { Section } from "../../components/layout";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { api, ApiError, newId } from "../../lib/api";
import { formatBytes, plural, relativeTime } from "../../lib/format";
import { useAsync, useInterval } from "../../lib/hooks";
import type {
  DebridConfig,
  DebridEngineStatus,
  DebridLatency,
  DebridKind,
  DebridProvider,
  DebridProviderStatus,
  EtherConfig,
} from "../../lib/types";
import { SchemaField } from "../settings/SchemaForm";
import { DEBRID } from "../settings/schema";
import {
  BoolRow,
  OrderButtons,
  RecordDrawer,
  RecordItem,
  TextRow,
  recordErrors,
  swap,
  toastError,
  useTests,
  type Errors,
} from "./form";

export const DEBRID_KINDS: { value: DebridKind; label: string }[] = [
  { value: "realdebrid", label: "Real-Debrid" },
  { value: "torbox", label: "TorBox" },
  { value: "alldebrid", label: "AllDebrid" },
  { value: "premiumize", label: "Premiumize" },
  { value: "debridlink", label: "Debrid-Link" },
  { value: "offcloud", label: "Offcloud" },
];

export function debridKindLabel(kind: string): string {
  return DEBRID_KINDS.find((k) => k.value === kind)?.label ?? kind;
}

/** Defaults for servers that report no debrid section yet. */
const EMPTY: DebridConfig = { providers: [], cachedOnly: true, removeUncached: true };

/** Debrid settings for `cfg`, with defaults filled in. */
export function debridOf(cfg: EtherConfig): DebridConfig {
  return { ...EMPTY, ...cfg.debrid };
}

/** Whether any debrid account can be used (enabled and has a key). */
export function hasUsableDebrid(cfg: EtherConfig): boolean {
  return (cfg.debrid?.providers ?? []).some((p) => p.enabled && p.apiKey !== "");
}

function blankProvider(priority: number): DebridProvider {
  return {
    id: newId("debrid"),
    name: "",
    kind: "realdebrid",
    apiKey: "",
    priority,
    enabled: true,
  };
}

function providerName(p: DebridProvider, i: number) {
  return p.name || debridKindLabel(p.kind) || `Provider ${i + 1}`;
}

function role(i: number) {
  return i === 0 ? "Tried first" : `Priority ${i + 1}`;
}

/** Counters and last activity for one provider's row. */
function activity(s: DebridProviderStatus): string[] {
  const out: string[] = [];
  const requests = Number(s.requests ?? 0);
  if (requests > 0) out.push(plural(requests, "request"));
  if (s.cached || s.notCached)
    out.push(`${Number(s.cached ?? 0)} cached · ${Number(s.notCached ?? 0)} not cached`);
  if (s.blocked) out.push(`${Number(s.blocked)} blocked by provider`);
  if (s.failures) out.push(plural(Number(s.failures), "failure"));
  if (s.reused) out.push(`${Number(s.reused)} reused`);
  if (s.linksRefreshed) out.push(`${plural(Number(s.linksRefreshed), "link")} refreshed`);
  // In a cluster the account's concurrent API calls are divided among nodes.
  const slots = Number(s.slots ?? 0);
  const account = Number(s.accountSlots ?? 0);
  if (account > 0 && slots !== account)
    out.push(`this node: ${slots} of ${account} API connections`);
  if (s.lastSuccess) out.push(`last worked ${relativeTime(s.lastSuccess)}`);
  return out;
}

function ms(n: number): string {
  // Half a second and up reads better in seconds ("0.8 s").
  return n >= 500 ? `${(n / 1000).toFixed(1)} s` : `${Math.round(n)} ms`;
}

/** Megabytes per second, decimal (as speed tests report it). */
function mbps(bytesPerSecond: number): string {
  const v = bytesPerSecond / 1e6;
  return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} MB/s`;
}

/** Averaged timings for one provider, or null before it has been used. */
export function latencySummary(l: DebridLatency | undefined): string | null {
  if (!l || !Number(l.samples ?? 0)) return null;
  const out: string[] = [];
  if (l.resolveMs) out.push(`API ${ms(l.resolveMs)}`);
  if (l.linkMs) out.push(`link ${ms(l.linkMs)}`);
  if (l.ttfbMs) out.push(`first byte ${ms(l.ttfbMs)}`);
  if (l.throughputBps) out.push(mbps(l.throughputBps));
  return out.length ? out.join(" · ") : null;
}

/** One line about the shared streaming engine, or null when the server reports none. */
export function engineSummary(e: DebridEngineStatus | undefined): string | null {
  if (!e) return null;
  const n = (v: number | undefined) => Number(v ?? 0);
  const hits = n(e.memoryHits) + n(e.diskHits);
  const out = [
    plural(n(e.openFiles), "open file"),
    `${plural(hits, "cache hit")} (${n(e.memoryHits)} memory, ${n(e.diskHits)} disk) vs ${n(e.blocksFetched)} fetched` +
      (n(e.bytesFetched) ? ` (${formatBytes(n(e.bytesFetched))})` : ""),
  ];
  if (n(e.hedges)) out.push(`${plural(n(e.hedges), "hedged request")}, ${n(e.hedgeWins)} won`);
  if (n(e.linkRefreshes)) out.push(`${plural(n(e.linkRefreshes), "link")} refreshed`);
  if (n(e.errors)) out.push(plural(n(e.errors), "error"));
  return out.join(" · ");
}

function StateBadge({ p, s }: { p: DebridProvider; s?: DebridProviderStatus }) {
  if (!p.enabled) return <Badge variant="secondary">Off</Badge>;
  if (!s) return null;
  switch (s.state) {
    case "ok":
      return <Badge variant="success">Ready</Badge>;
    case "backoff":
      return (
        <Badge variant="warning">
          {s.backoffUntil ? `Paused · retries ${relativeTime(s.backoffUntil)}` : "Paused"}
        </Badge>
      );
    case "auth_failed":
      return <Badge variant="destructive">Key rejected</Badge>;
    case "disabled":
      return <Badge variant="secondary">Inactive</Badge>;
    default:
      return <Badge variant="secondary">{String(s.state || "Unknown")}</Badge>;
  }
}

/**
 * Debrid accounts in priority order, each edited in a drawer, followed by the
 * section's torrent-handling settings edited in place.
 */
export function DebridSources() {
  const { refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  // Live status refreshes on its own; a failed poll leaves the list usable.
  const status = useAsync(() => api.debridStatus().catch(() => null), []);
  useInterval(() => void status.reload(), 15_000);
  const [editing, setEditing] = useState<{ index: number; draft: DebridProvider } | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const { tests, runTest, reset } = useTests();

  const cfg = config.data;
  const debrid = cfg ? debridOf(cfg) : null;
  const providers = useMemo(
    () => [...(debrid?.providers ?? [])].sort((a, b) => (a.priority ?? 0) - (b.priority ?? 0)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [JSON.stringify(debrid?.providers ?? [])],
  );
  const live = status.data?.providers ?? [];
  const engine = engineSummary(status.data?.engine);
  const torrentIndexers = (cfg?.indexers ?? []).filter(
    (ix) => ix.protocol === "torrent" && ix.enabled,
  ).length;

  const reloadAll = useCallback(async () => {
    await config.reload();
    void status.reload();
    void refreshDashboard();
  }, [config, status, refreshDashboard]);

  async function commit(list: DebridProvider[], message: string) {
    if (!debrid) return;
    await api.putSection("debrid", {
      ...debrid,
      providers: list.map((p, priority) => ({ ...p, priority })),
    });
    toast.success(message);
    await reloadAll();
  }

  const open = (index: number, draft: DebridProvider) => {
    setErrors({});
    setError(null);
    reset(draft.id);
    setEditing({ index, draft: structuredClone(draft) });
  };
  const close = () => {
    if (!saving) setEditing(null);
  };

  async function save() {
    if (!editing) return;
    const { index, draft } = editing;
    const list =
      index < providers.length
        ? providers.map((p, j) => (j === index ? draft : p))
        : [...providers, draft];
    setSaving(true);
    setErrors({});
    setError(null);
    try {
      await commit(list, index < providers.length ? "Provider saved" : "Provider added");
      setEditing(null);
    } catch (e) {
      setError(e);
      setErrors(recordErrors(e, `debrid.providers[${index}].`));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!editing) return;
    const { index, draft } = editing;
    const ok = await confirmDialog({
      title: "Remove provider?",
      message: `${providerName(draft, index)} will no longer be used for streaming. Its API key is deleted from Ether.`,
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    setSaving(true);
    setError(null);
    try {
      await commit(
        providers.filter((_, j) => j !== index),
        "Provider removed",
      );
      setEditing(null);
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }

  async function move(i: number, d: -1 | 1) {
    try {
      await commit(swap(providers, i, i + d), "Provider order saved");
    } catch (e) {
      toastError("Couldn't reorder providers", e);
    }
  }

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!cfg || !debrid) return <LoadingRows rows={3} />;

  const draft = editing?.draft;
  const set = (patch: Partial<DebridProvider>) =>
    setEditing((e) => (e ? { ...e, draft: { ...e.draft, ...patch } } : e));
  const isNew = editing ? editing.index >= providers.length : false;
  const name = draft && editing ? providerName(draft, editing.index) : "";

  return (
    <>
      <Alert variant="info">
        <AlertDescription>
          Ether ships no debrid providers: bring your own Real-Debrid, TorBox, AllDebrid,
          Premiumize, Debrid-Link or Offcloud account and its API key. Debrid streams torrent
          releases, so it also needs at least one torrent source (a Torznab indexer, built-in site
          or Stremio addon) under{" "}
          <Link to="/sources/indexers" className="font-medium underline underline-offset-2">
            Indexers
          </Link>
          {torrentIndexers === 0 ? "; none is set up yet." : "."}
        </AlertDescription>
      </Alert>

      <Section
        title="Debrid providers"
        description="Tried in order. A torrent is used only when a provider can stream it."
        actions={
          <Button size="sm" onClick={() => open(providers.length, blankProvider(providers.length))}>
            <Plus size={14} /> Add provider
          </Button>
        }
        bodyClassName=""
      >
        {providers.length === 0 ? (
          <EmptyState
            icon={<Cloud />}
            title="No debrid providers"
            description="Add your debrid account to stream torrent releases."
          />
        ) : (
          <ul className="divide-y divide-border">
            {providers.map((p, i) => {
              const s = live.find((x) => x.id === p.id);
              const label = providerName(p, i);
              const latency = latencySummary(s?.latency);
              const details: ReactNode = (
                <>
                  {[role(i), ...(s ? activity(s) : [])].join(" · ")}
                  {latency ? <span className="block tabular-nums">{latency}</span> : null}
                  {s?.lastError ? (
                    <span className="block truncate text-danger" title={s.lastError}>
                      {s.lastError}
                    </span>
                  ) : null}
                </>
              );
              return (
                <RecordItem
                  key={p.id}
                  title={label}
                  description={details}
                  meta={
                    <>
                      <Badge variant="outline">{debridKindLabel(p.kind)}</Badge>
                      <StateBadge p={p} s={s} />
                    </>
                  }
                  order={
                    <OrderButtons
                      label={label}
                      i={i}
                      n={providers.length}
                      onMove={(d) => void move(i, d)}
                    />
                  }
                  editLabel={`Edit ${label}`}
                  onEdit={() => open(i, p)}
                />
              );
            })}
          </ul>
        )}
        {engine ? (
          <p
            aria-label="Streaming engine"
            className="border-t border-border px-5 py-3 text-xs text-fg-muted tabular-nums"
          >
            <span className="font-medium text-fg">Streaming engine</span> · {engine}
          </p>
        ) : null}
      </Section>

      <DebridSettings debrid={debrid} onSaved={reloadAll} />

      <RecordDrawer
        open={editing !== null}
        onClose={close}
        title={isNew ? "New debrid provider" : name}
        description={editing && !isNew ? role(editing.index) : "Debrid account"}
        onSave={() => void save()}
        saving={saving}
        saveLabel="Save provider"
        onRemove={isNew ? undefined : () => void remove()}
        removeLabel="Remove provider"
        error={error}
        tools={
          draft ? (
            <TestButton
              state={tests[draft.id] ?? { status: "idle" }}
              onTest={() => runTest(draft.id, () => api.testDebrid(draft))}
              disabled={!draft.apiKey}
            />
          ) : null
        }
      >
        {draft ? (
          <SettingsSection title="Account">
            <TextRow
              label="Name"
              value={draft.name}
              placeholder={debridKindLabel(draft.kind)}
              error={errors.name}
              onChange={(v) => set({ name: v })}
            />
            <SettingsRow
              title="Service"
              description={
                errors.kind ? (
                  <span className="block text-xs text-danger" role="alert">
                    {errors.kind}
                  </span>
                ) : undefined
              }
              control={
                <div className="w-48">
                  <SearchSelect
                    value={draft.kind}
                    onChange={(v) => set({ kind: v as DebridKind })}
                    options={DEBRID_KINDS}
                    ariaLabel="Service"
                    searchable={false}
                  />
                </div>
              }
            />
            <SettingsRow
              stacked
              title="API key"
              description={
                <>
                  From your debrid account&apos;s settings.
                  {errors.apiKey ? (
                    <span className="block text-xs text-danger" role="alert">
                      {errors.apiKey}
                    </span>
                  ) : null}
                </>
              }
            >
              <SecretInput
                ariaLabel="API key"
                value={draft.apiKey}
                invalid={Boolean(errors.apiKey)}
                onChange={(v) => set({ apiKey: v })}
              />
            </SettingsRow>
            <BoolRow label="Enabled" value={draft.enabled} onChange={(v) => set({ enabled: v })} />
          </SettingsSection>
        ) : null}
      </RecordDrawer>
    </>
  );
}

type Obj = Record<string, unknown>;

/** cachedOnly, removeUncached and the advanced streaming knobs, edited in place. */
function DebridSettings({
  debrid,
  onSaved,
}: {
  debrid: DebridConfig;
  onSaved: () => Promise<void>;
}) {
  const saved = useMemo<Obj>(() => {
    const { providers: _providers, ...rest } = debrid;
    return {
      chunkBytes: 0,
      readAheadChunks: 0,
      maxConnections: 0,
      ...rest,
    };
  }, [debrid]);
  const { draft, setDraft, dirty, reset } = useDraft<Obj>(saved);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);

  async function save() {
    if (!draft) return;
    setSaving(true);
    setError(null);
    setErrors({});
    try {
      // Providers come from the last saved config so unsaved settings never
      // resurrect a removed provider (their keys stay redacted and are kept).
      await api.putSection("debrid", { ...debrid, ...draft });
      toast.success("Settings saved", { description: "Debrid" });
      await onSaved();
    } catch (e) {
      setError(e);
      if (e instanceof ApiError && e.fields)
        setErrors(Object.fromEntries(e.fields.map((f) => [f.field, f.message])));
    } finally {
      setSaving(false);
    }
  }

  if (!draft) return null;
  return (
    <>
      {DEBRID.map((g) => (
        <SettingsSection key={g.title} title={g.title} description={g.description}>
          {g.fields.map((f) => (
            <SchemaField
              key={f.key}
              def={f}
              value={draft[f.key]}
              path={`debrid.${f.key}`}
              error={errors[`debrid.${f.key}`]}
              onChange={(v) => setDraft({ ...draft, [f.key]: v })}
            />
          ))}
        </SettingsSection>
      ))}
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={save}
        onDiscard={() => {
          reset();
          setErrors({});
          setError(null);
        }}
      />
    </>
  );
}
