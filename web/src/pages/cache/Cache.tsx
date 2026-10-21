import { Badge, Button, Input, Spinner, cn, toast } from "@braedonsaunders/appkit-ui";
import {
  ArrowUpRight,
  Check,
  Clock3,
  HardDrive,
  Leaf,
  Pin,
  Play,
  RefreshCw,
  Settings2,
  ShieldCheck,
  Sparkles,
  Zap,
} from "lucide-react";
import { useState } from "react";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel } from "../../components/feedback";
import { Field, NumberInput } from "../../components/fields";
import { useDraft, useUnsavedGuard } from "../../components/SaveBar";
import { Workspace } from "../../components/workspace";
import { api, apiFetch } from "../../lib/api";
import { formatBytes } from "../../lib/format";
import { useAsync, useInterval } from "../../lib/hooks";
import type { EtherConfig } from "../../lib/types";

type Policy = EtherConfig["cache"];
type Release = {
  ReleaseID: string;
  Title: string;
  Bytes: number;
  Size: number;
  Segments: number;
  Plays: number;
  LastUsed: string;
  LastPlayed: string;
  Pins: string[] | null;
};
type PreloadStatus = {
  state: string;
  message: string;
  title: string;
  fetched: number;
  size: number;
  allocated: number;
  ready: number;
  failed: number;
  paused: boolean;
  items: { key: number; title: string; releaseId: string; size: number; complete: boolean }[];
};
export type CacheSnapshot = {
  preload: PreloadStatus;
  used: number;
  budget: number;
  directory: string;
  protectedBytes: number;
  releases: Release[];
  metrics: { hits: number; misses: number; servedBytes: number; evictedBytes: number };
  prefetch: { queued: number; running: number; completed: number; failed: number; dropped: number };
};
const GiB = 1024 ** 3,
  MiB = 1024 ** 2;
export const PRESETS: {
  name: string;
  space: string;
  description: string;
  values: Partial<Policy>;
}[] = [
  {
    name: "Feather",
    space: "256 MiB",
    description: "A small footprint. Just the useful moments.",
    values: {
      preloadEnabled: false,
      preloadBudgetBytes: 0,
      mode: "demand",
      budgetBytes: 256 * MiB,
      retentionDays: 1,
      replayBoostHours: 0,
      detailBytes: 2 * MiB,
      detailWarmup: "on",
      disableSpeculation: false,
      prefetchNextEpisode: false,
      keepInProgressWarm: false,
      prefetchBytes: 16 * MiB,
    },
  },
  {
    name: "Everyday",
    space: "32 GiB",
    description: "Quick starts and room for recent watches.",
    values: {
      preloadEnabled: false,
      preloadBudgetBytes: 0,
      mode: "adaptive",
      budgetBytes: 32 * GiB,
      retentionDays: 7,
      replayBoostHours: 12,
      detailBytes: 8 * MiB,
      detailWarmup: "on",
      disableSpeculation: false,
      prefetchNextEpisode: true,
      keepInProgressWarm: false,
      prefetchBytes: 32 * MiB,
    },
  },
  {
    name: "Encore",
    space: "250 GiB",
    description: "Give household favourites a longer stay.",
    values: {
      preloadEnabled: false,
      preloadBudgetBytes: 0,
      mode: "adaptive",
      budgetBytes: 250 * GiB,
      retentionDays: 30,
      replayBoostHours: 48,
      detailBytes: 16 * MiB,
      detailWarmup: "on",
      disableSpeculation: false,
      prefetchNextEpisode: true,
      keepInProgressWarm: true,
      prefetchBytes: 64 * MiB,
    },
  },
  {
    name: "Deep shelf",
    space: "750 GiB",
    description: "Keep more of what everyone comes back to.",
    values: {
      preloadEnabled: false,
      preloadBudgetBytes: 0,
      mode: "adaptive",
      budgetBytes: 750 * GiB,
      retentionDays: 90,
      replayBoostHours: 72,
      detailBytes: 32 * MiB,
      detailWarmup: "on",
      disableSpeculation: false,
      prefetchNextEpisode: true,
      keepInProgressWarm: true,
      prefetchBytes: 64 * MiB,
    },
  },
  {
    name: "Cinema node",
    space: "2 TiB",
    description: "A managed shelf of complete movie sources.",
    values: {
      mode: "adaptive",
      budgetBytes: 2048 * GiB,
      retentionDays: 90,
      replayBoostHours: 72,
      detailBytes: 32 * MiB,
      detailWarmup: "on",
      disableSpeculation: false,
      prefetchNextEpisode: true,
      keepInProgressWarm: true,
      prefetchBytes: 64 * MiB,
      preloadEnabled: true,
      preloadBudgetBytes: 1536 * GiB,
      preloadMaxTitles: 400,
      preloadRateMiB: 20,
      preloadSource: "popular",
    },
  },
  {
    name: "Edge library",
    space: "20 TiB",
    description: "Thousands of films. A shelf that fills itself.",
    values: {
      mode: "adaptive",
      budgetBytes: 20480 * GiB,
      retentionDays: 180,
      replayBoostHours: 96,
      detailBytes: 32 * MiB,
      detailWarmup: "on",
      disableSpeculation: false,
      prefetchNextEpisode: true,
      keepInProgressWarm: true,
      prefetchBytes: 64 * MiB,
      preloadEnabled: true,
      preloadBudgetBytes: 18432 * GiB,
      preloadMaxTitles: 2500,
      preloadRateMiB: 50,
      preloadSource: "popular",
    },
  },
];

function Toggle({
  title,
  description,
  checked,
  disabled,
  onChange,
}: {
  title: string;
  description: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start justify-between gap-5 py-3",
        disabled && "opacity-45",
      )}
    >
      <span>
        <span className="block text-sm font-medium text-fg">{title}</span>
        <span className="mt-1 block text-xs leading-relaxed text-fg-muted">{description}</span>
      </span>
      <input
        type="checkbox"
        role="switch"
        aria-label={title}
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-1 h-4 w-4 shrink-0 accent-primary"
      />
    </label>
  );
}

function Age({ value }: { value: string }) {
  const hours = Math.max(0, (Date.now() - Date.parse(value)) / 3600000);
  return (
    <span title={new Date(value).toLocaleString()}>
      {hours < 1
        ? "Just now"
        : hours < 24
          ? `${Math.floor(hours)}h ago`
          : `${Math.floor(hours / 24)}d ago`}
    </span>
  );
}

export default function Cache() {
  const config = useAsync(() => api.getConfig(), []);
  const live = useAsync((signal) => apiFetch<CacheSnapshot>("/cache/", { signal }), []);
  useInterval(() => void live.reload(), 5000);
  const { draft, setDraft, dirty, reset } = useDraft<Policy>(config.data?.cache);
  useUnsavedGuard(dirty);
  const [saving, setSaving] = useState(false),
    [error, setError] = useState<unknown>(null);
  const [advanced, setAdvanced] = useState(false),
    [search, setSearch] = useState("");
  const [unit, setUnit] = useState<"GiB" | "TiB">("GiB");
  const [filter, setFilter] = useState("all"),
    [busy, setBusy] = useState<string | null>(null);
  const disk = useAsync(() => api.disk(config.data?.cache.dir ?? ""), [config.data?.cache.dir]);
  const patch = (v: Partial<Policy>) => setDraft((d) => (d ? { ...d, ...v } : d));
  async function save() {
    if (!draft) return;
    setSaving(true);
    setError(null);
    try {
      await api.putSection("cache", draft);
      await config.reload();
      await live.reload();
      toast.success("Cache policy saved");
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }
  async function action(path: string, body?: unknown) {
    setBusy(path);
    setError(null);
    try {
      await apiFetch(`/cache/${path}`, {
        method: "POST",
        ...(body ? { body: JSON.stringify(body) } : {}),
      });
      await live.reload();
      if (path === "warm") toast.success("Resume warming requested");
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  }
  if (!draft)
    return (
      <Workspace title="Cache">
        {config.error ? (
          <ErrorPanel error={config.error} onRetry={() => void config.reload()} />
        ) : (
          <div className="grid flex-1 place-items-center">
            <Spinner label="Loading cache" />
          </div>
        )}
      </Workspace>
    );
  const snap = live.data;
  const used = snap?.used ?? 0,
    budget = snap?.budget ?? 0;
  const percent = budget > 0 ? Math.min(100, (used / budget) * 100) : 0;
  const activePreset = PRESETS.find((p) =>
    Object.entries(p.values).every(([k, v]) => draft[k as keyof Policy] === v),
  );
  const enabled = draft.mode !== "off",
    speculate = enabled && !draft.disableSpeculation;
  const releases = (snap?.releases ?? []).filter(
    (r) =>
      (filter === "all" || (filter === "protected" ? (r.Pins?.length ?? 0) > 0 : r.Plays > 1)) &&
      `${r.Title} ${r.ReleaseID}`.toLowerCase().includes(search.toLowerCase()),
  );
  const reads = (snap?.metrics.hits ?? 0) + (snap?.metrics.misses ?? 0);
  return (
    <Workspace
      title="Cache"
      subtitle="Make space for a faster next watch."
      actions={
        <>
          <Badge variant={dirty ? "warning" : "secondary"}>
            {dirty ? "Unsaved changes" : "This node"}
          </Badge>
          {dirty && (
            <Button variant="ghost" size="sm" onClick={reset} disabled={saving}>
              Discard
            </Button>
          )}
          <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}>
            {saving ? "Saving…" : "Save policy"}
          </Button>
        </>
      }
      asideLabel="Cache policy"
      mainLabel="Cache overview"
      aside={
        <div className="space-y-6 p-4 sm:p-5">
          <div>
            <p className="text-[10px] font-semibold uppercase tracking-[.2em] text-fg-subtle">
              01 / Your space
            </p>
            <h2 className="mt-2 text-lg font-semibold tracking-tight">
              Small footprint. Or a deep shelf.
            </h2>
            <p className="mt-1 text-xs leading-relaxed text-fg-muted">
              From almost no storage to a managed local library. The two largest presets enable
              full-movie preloading when you save; every budget is yours to change.
            </p>
          </div>
          <button
            type="button"
            aria-pressed={!enabled}
            onClick={() =>
              patch({
                mode: "off",
                budgetBytes: 0,
                preloadEnabled: false,
                preloadBudgetBytes: 0,
                disableSpeculation: true,
              })
            }
            className={cn(
              "flex w-full items-center gap-3 rounded-xl border p-3 text-left transition hover:border-primary/60",
              !enabled ? "border-primary bg-primary/10" : "border-border bg-surface",
            )}
          >
            <Leaf size={18} className="text-primary" />
            <span className="flex-1">
              <span className="block text-sm font-semibold">Pass-through</span>
              <span className="block text-xs text-fg-muted">
                No persistent media. Stream and move on.
              </span>
            </span>
            <span className="text-xs text-primary">0 B</span>
          </button>
          <div className="grid grid-cols-2 gap-2">
            {PRESETS.map((p, i) => (
              <button
                key={p.name}
                type="button"
                aria-pressed={activePreset?.name === p.name}
                onClick={() => patch(p.values)}
                className={cn(
                  "relative rounded-xl border p-3 text-left transition hover:border-primary/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  activePreset?.name === p.name
                    ? "border-primary bg-primary/10"
                    : "border-border bg-surface",
                )}
              >
                <span className="flex items-center justify-between text-fg-muted">
                  {i === 0 ? (
                    <Leaf size={16} />
                  ) : i === 1 ? (
                    <Zap size={16} />
                  ) : i === 2 ? (
                    <Sparkles size={16} />
                  ) : (
                    <HardDrive size={16} />
                  )}
                  {activePreset?.name === p.name && <Check size={14} className="text-primary" />}
                </span>
                <span className="mt-3 block text-sm font-semibold">{p.name}</span>
                <span className="block text-xs text-primary">{p.space}</span>
                <span className="mt-2 block text-[11px] leading-relaxed text-fg-muted">
                  {p.description}
                </span>
              </button>
            ))}
          </div>
          <Field
            label="Media disk budget"
            hint="Zero means unlimited, not disabled. Choose No persistent media below to turn disk caching off."
            htmlFor="cache-budget"
          >
            <div className="flex items-center gap-2">
              <NumberInput
                id="cache-budget"
                min={0}
                step={0.25}
                value={draft.budgetBytes / (unit === "TiB" ? 1024 * GiB : GiB)}
                onChange={(n) =>
                  patch({ budgetBytes: Math.round(n * (unit === "TiB" ? 1024 * GiB : GiB)) })
                }
              />
              <select
                aria-label="Budget unit"
                value={unit}
                onChange={(e) => setUnit(e.target.value as "GiB" | "TiB")}
                className="rounded border border-border bg-surface p-2 text-xs"
              >
                <option>GiB</option>
                <option>TiB</option>
              </select>
            </div>
          </Field>
          {disk.data && (
            <p className="flex items-center gap-2 text-xs text-fg-muted">
              <HardDrive size={13} />
              {formatBytes(disk.data.freeBytes)} currently free
            </p>
          )}
          {disk.data &&
            enabled &&
            draft.budgetBytes > 0 &&
            draft.budgetBytes - used > disk.data.freeBytes && (
              <p className="rounded-lg bg-warning/10 p-3 text-xs text-warning">
                This budget needs more space than is currently free. Choose a smaller budget.
              </p>
            )}
          <div className="border-t border-border pt-5">
            <p className="text-[10px] font-semibold uppercase tracking-[.2em] text-fg-subtle">
              02 / What stays
            </p>
            <label className="mt-3 block text-sm font-medium" htmlFor="cache-mode">
              Retention strategy
            </label>
            <select
              id="cache-mode"
              value={draft.mode || "demand"}
              onChange={(e) =>
                patch({
                  mode: e.target.value,
                  ...(e.target.value === "off" ? { preloadEnabled: false } : {}),
                })
              }
              className="mt-2 w-full rounded-lg border border-border bg-surface p-2.5 text-sm"
            >
              <option value="off">No persistent media</option>
              <option value="demand">Keep what is streamed</option>
              <option value="adaptive">Favour repeat watches</option>
            </select>
            <p className="mt-2 text-xs leading-relaxed text-fg-muted">
              {!enabled
                ? "New media bytes bypass disk. Unprotected existing bytes are released; playback and pinned bytes remain protected."
                : draft.mode === "adaptive"
                  ? "Repeat plays earn extra time before eviction. Popularity is learned from playback, never from background reads."
                  : "Keep recently used bytes. The oldest unprotected segments make room first."}
            </p>
          </div>
          <Field
            label="Expire after inactivity"
            hint="Days since last cache use. Zero keeps bytes until space is needed."
            htmlFor="cache-retention"
          >
            <NumberInput
              id="cache-retention"
              min={0}
              max={3650}
              value={draft.retentionDays ?? 0}
              onChange={(n) => patch({ retentionDays: n })}
            />
          </Field>
          <div className="border-t border-border pt-5">
            <p className="text-[10px] font-semibold uppercase tracking-[.2em] text-fg-subtle">
              03 / Think ahead
            </p>
            <Toggle
              title="Allow opening and resume warming"
              description="Let Ether fetch small windows before Play. Turn off for strictly on-demand media."
              checked={speculate}
              disabled={!enabled}
              onChange={(v) => patch({ disableSpeculation: !v })}
            />
            <Toggle
              title="Warm detail pages"
              description="Opening a movie or episode prepares its source and opening bytes. Shows warm their On Deck episode when supported."
              checked={draft.detailWarmup !== "off"}
              disabled={!speculate}
              onChange={(v) => patch({ detailWarmup: v ? "on" : "off" })}
            />
            <Toggle
              title="Prepare the next episode"
              description="Warm the opening near the end of the current episode."
              checked={draft.prefetchNextEpisode}
              disabled={!speculate}
              onChange={(v) => patch({ prefetchNextEpisode: v })}
            />
            <Toggle
              title="Keep resume points warm"
              description="Periodically refresh a small window around Continue Watching."
              checked={draft.keepInProgressWarm}
              disabled={!speculate}
              onChange={(v) => patch({ keepInProgressWarm: v })}
            />
          </div>
          <div className="border-t border-border pt-5">
            <p className="text-[10px] font-semibold uppercase tracking-[.2em] text-fg-subtle">
              04 / Your local library
            </p>
            <Toggle
              title="Preload complete movies"
              description="Proactively fill a dedicated part of your cache from the local catalogue using your sources and quality rules. This downloads full movie sources."
              checked={draft.preloadEnabled ?? false}
              disabled={!enabled}
              onChange={(v) =>
                patch({
                  preloadEnabled: v,
                  ...(v && !draft.preloadBudgetBytes
                    ? { preloadBudgetBytes: Math.min(draft.budgetBytes || 100 * GiB, 100 * GiB) }
                    : {}),
                })
              }
            />
            {draft.preloadEnabled && (
              <div className="space-y-4 rounded-xl border border-primary/20 bg-primary/5 p-3">
                <Field
                  label="Preload allocation (GiB)"
                  hint="Part of the total budget, not additional storage."
                  htmlFor="preload-budget"
                >
                  <NumberInput
                    id="preload-budget"
                    min={1}
                    value={(draft.preloadBudgetBytes ?? 0) / GiB}
                    onChange={(n) => patch({ preloadBudgetBytes: Math.round(n * GiB) })}
                  />
                </Field>
                <Field label="Choose movies by" htmlFor="preload-source">
                  <select
                    id="preload-source"
                    className="w-full rounded border border-border bg-surface p-2 text-sm"
                    value={draft.preloadSource || "popular"}
                    onChange={(e) => patch({ preloadSource: e.target.value })}
                  >
                    <option value="popular">Most popular</option>
                    <option value="recent">Newest releases</option>
                  </select>
                </Field>
                <p className="text-[11px] text-fg-muted">
                  About {Math.floor((draft.preloadBudgetBytes ?? 0) / (8 * GiB))} movies at 8 GiB
                  each, before overhead. Actual sizes and source availability vary.
                </p>
              </div>
            )}
          </div>
          <Button variant="secondary" className="w-full" onClick={() => setAdvanced(true)}>
            <Settings2 size={15} /> Fine tune policy <ArrowUpRight size={14} />
          </Button>
          <p className="text-[11px] leading-relaxed text-fg-subtle">
            Live streams and pinned releases can exceed the budget. Expiry never removes protected
            bytes. This budget covers media segments; artwork, metadata and transcode output use
            separate storage.
          </p>
        </div>
      }
      main={
        <div className="space-y-6 p-4 sm:p-6 xl:p-8">
          {error ? <ErrorPanel error={error} /> : null}
          {live.error ? <ErrorPanel error={live.error} onRetry={() => void live.reload()} /> : null}
          <section className="relative overflow-hidden rounded-2xl border border-primary/20 bg-gradient-to-br from-primary/10 via-surface to-surface p-6 sm:p-8">
            <div className="pointer-events-none absolute -right-16 -top-24 h-64 w-64 rounded-full bg-primary/10 blur-3xl" />
            <div className="relative flex flex-wrap items-center gap-8">
              <div className="min-w-0 flex-1">
                <span className="inline-flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-[.2em] text-primary">
                  <Sparkles size={13} /> Space, thoughtfully used
                </span>
                <h2 className="mt-4 max-w-sm text-3xl font-semibold leading-tight tracking-tight">
                  The next watch
                  <br />
                  starts here.
                </h2>
                <p className="mt-3 max-w-md text-sm leading-relaxed text-fg-muted">
                  A rolling memory of what gets watched. Keep the useful bytes, prepare the next
                  moment, and let the rest make room.
                </p>
                <div className="mt-5 flex flex-wrap gap-2">
                  <Badge variant="secondary">
                    <ShieldCheck size={12} /> Playback protected
                  </Badge>
                  <Badge variant="secondary">
                    {config.data?.cache.mode === "off"
                      ? "Disk writes off"
                      : config.data?.cache.mode === "adaptive"
                        ? "Adaptive retention"
                        : "On-demand retention"}
                  </Badge>
                </div>
              </div>
              <div
                className="relative grid h-40 w-40 shrink-0 place-items-center"
                role="img"
                aria-label={
                  snap
                    ? `${formatBytes(used)} cached${budget ? ` of ${formatBytes(budget)}` : ", unlimited budget"}`
                    : "Loading cache usage"
                }
              >
                <svg
                  viewBox="0 0 160 160"
                  className="absolute inset-0 h-full w-full -rotate-90"
                  aria-hidden="true"
                >
                  <circle
                    cx="80"
                    cy="80"
                    r="70"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="7"
                    className="text-primary/10"
                  />
                  <circle
                    cx="80"
                    cy="80"
                    r="70"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="7"
                    strokeLinecap="round"
                    strokeDasharray={`${percent * 4.398} 439.8`}
                    className="text-primary transition-all duration-700"
                  />
                </svg>
                <div className="text-center">
                  <span className="block text-2xl font-semibold tracking-tight">
                    {snap ? formatBytes(used) : "—"}
                  </span>
                  <span className="mt-1 block text-xs text-fg-muted">
                    {config.data?.cache.mode === "off"
                      ? "disk writes off"
                      : budget
                        ? `of ${formatBytes(budget)}`
                        : "no size limit"}
                  </span>
                </div>
              </div>
            </div>
            <div className="mt-7 grid grid-cols-2 gap-5 border-t border-primary/10 pt-5 sm:grid-cols-4">
              {[
                ["Stored releases", snap ? String(snap.releases.length) : "—"],
                ["Protected bytes", snap ? formatBytes(snap.protectedBytes) : "—"],
                [
                  "Disk hit rate",
                  reads ? `${Math.round((snap!.metrics.hits / reads) * 100)}%` : "—",
                ],
                ["Served from disk", snap ? formatBytes(snap.metrics.servedBytes) : "—"],
              ].map(([label, value]) => (
                <div key={label}>
                  <p className="text-xl font-semibold tabular-nums">{value}</p>
                  <p className="mt-1 text-[11px] text-fg-muted">{label}</p>
                </div>
              ))}
            </div>
            <p className="mt-3 text-[10px] text-fg-subtle">
              Hit rate and served bytes are disk-cache counters since server startup.
            </p>
          </section>
          <section className="overflow-hidden rounded-xl border border-primary/20 bg-surface">
            <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border p-4">
              <div>
                <p className="text-[10px] font-semibold uppercase tracking-[.18em] text-primary">
                  Managed local library
                </p>
                <h2 className="mt-1 text-lg font-semibold">A shelf that fills itself.</h2>
              </div>
              <Button
                variant="secondary"
                size="sm"
                disabled={busy !== null || !config.data?.cache.preloadEnabled}
                onClick={() => void action("preload", { paused: !snap?.preload.paused })}
              >
                {snap?.preload.paused ? "Resume preloading" : "Pause preloading"}
              </Button>
            </div>
            <div className="space-y-4 p-4">
              <div className="grid grid-cols-3 gap-3">
                <div>
                  <p className="text-2xl font-semibold">{snap?.preload.ready ?? 0}</p>
                  <p className="text-xs text-fg-muted">Sources preloaded</p>
                </div>
                <div>
                  <p className="text-2xl font-semibold">
                    {formatBytes(snap?.preload.allocated ?? 0)}
                  </p>
                  <p className="text-xs text-fg-muted">Reserved, including overhead</p>
                </div>
                <div>
                  <p className="text-2xl font-semibold">
                    {formatBytes(config.data?.cache.preloadBudgetBytes ?? 0)}
                  </p>
                  <p className="text-xs text-fg-muted">Preload allocation</p>
                </div>
              </div>
              <div className="rounded-lg bg-bg-subtle p-3">
                <p className="text-sm font-medium">
                  {snap?.preload.paused
                    ? "Paused"
                    : snap?.preload.state === "filling"
                      ? snap.preload.title
                      : snap?.preload.state === "resolving"
                        ? `Finding a source for ${snap.preload.title}`
                        : "Library autopilot"}
                </p>
                <p className="mt-1 text-xs text-fg-muted">
                  {snap?.preload.message ||
                    "Enable complete-movie preloading to build a local shelf."}
                </p>
                {snap?.preload.state === "filling" && (
                  <>
                    <div
                      role="progressbar"
                      aria-label="Current preload"
                      aria-valuenow={snap.preload.fetched}
                      aria-valuemax={snap.preload.size}
                      className="mt-3 h-1.5 overflow-hidden rounded bg-primary/10"
                    >
                      <div
                        className="h-full bg-primary"
                        style={{
                          width: `${snap.preload.size ? Math.min(100, (snap.preload.fetched / snap.preload.size) * 100) : 0}%`,
                        }}
                      />
                    </div>
                    <p className="mt-1 text-[10px] text-fg-subtle">
                      {formatBytes(snap.preload.fetched)} / {formatBytes(snap.preload.size)}
                    </p>
                  </>
                )}
              </div>
              <p className="text-[11px] leading-relaxed text-fg-muted">
                One title at a time, with a bandwidth cap and free-space reserve. Pauses for
                playback by default. Complete sources stay protected inside your allocation; startup
                may still require a provider link or another release for a different client.
              </p>
              {(snap?.preload.failed ?? 0) > 0 && (
                <p className="text-xs text-fg-muted">
                  {snap?.preload.failed} attempts skipped or interrupted this run. Unavailable or
                  oversized titles do not stop the shelf.
                </p>
              )}
            </div>
          </section>
          <section className="rounded-xl border border-border bg-surface">
            <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border p-4">
              <div>
                <h2 className="flex items-center gap-2 text-sm font-semibold">
                  <Zap size={16} className="text-primary" /> Ahead of Play
                </h2>
                <p className="mt-1 text-xs text-fg-muted">
                  Background prefetch jobs · live playback has priority
                </p>
              </div>
              <Button
                variant="secondary"
                size="sm"
                disabled={
                  busy !== null ||
                  !config.data?.cache.keepInProgressWarm ||
                  config.data?.cache.disableSpeculation ||
                  config.data?.cache.mode === "off"
                }
                onClick={() => void action("warm")}
              >
                <RefreshCw size={13} /> Warm resumes
              </Button>
            </div>
            <div className="grid grid-cols-3 gap-3 p-4 sm:grid-cols-6">
              {[
                ["Queued", snap?.prefetch.queued],
                ["Running", snap?.prefetch.running],
                ["Completed", snap?.prefetch.completed],
                ["Failed", snap?.prefetch.failed],
                ["Dropped", snap?.prefetch.dropped],
                ["Reclaimed", snap ? formatBytes(snap.metrics.evictedBytes) : "—"],
              ].map(([label, value]) => (
                <div key={label}>
                  <p className="text-lg font-semibold tabular-nums">{value ?? "—"}</p>
                  <p className="text-[10px] text-fg-muted">{label}</p>
                </div>
              ))}
            </div>
          </section>
          <section className="overflow-hidden rounded-xl border border-border bg-surface">
            <div className="space-y-3 border-b border-border p-4">
              <div className="flex items-baseline justify-between gap-2">
                <h2 className="text-sm font-semibold">Your retained media</h2>
                <span className="text-xs text-fg-subtle">Refreshes every 5 seconds</span>
              </div>
              <div className="flex flex-wrap gap-2">
                <Input
                  aria-label="Search cached releases"
                  placeholder="Find a title or release…"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  className="min-w-36 flex-1"
                />
                <select
                  aria-label="Filter cached releases"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  className="rounded-md border border-border bg-surface px-3 text-sm"
                >
                  <option value="all">All releases</option>
                  <option value="replayed">Repeat watches</option>
                  <option value="protected">Protected</option>
                </select>
              </div>
            </div>
            {!snap ? (
              <div className="p-8 text-center text-sm text-fg-muted">Loading retained media…</div>
            ) : releases.length === 0 ? (
              <div className="flex flex-col items-center px-6 py-14 text-center">
                <div className="mb-4 rounded-2xl bg-primary/10 p-4">
                  <Play size={24} className="text-primary" />
                </div>
                <h3 className="text-base font-medium">
                  {search || filter !== "all"
                    ? "No matching releases"
                    : "Your next stream plants the first seed"}
                </h3>
                <p className="mt-2 max-w-sm text-sm leading-relaxed text-fg-muted">
                  {search || filter !== "all"
                    ? "Try another search or filter."
                    : "As viewers watch, retained segments appear here. Nothing needs to be downloaded in advance."}
                </p>
              </div>
            ) : (
              <div className="divide-y divide-border">
                {releases.slice(0, 100).map((r) => (
                  <div
                    key={r.ReleaseID}
                    className="flex items-start gap-3 p-4 transition-colors hover:bg-surface-hover"
                  >
                    <div className="mt-1 rounded-lg bg-primary/10 p-2 text-primary">
                      <Play size={16} />
                    </div>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium" title={r.Title || r.ReleaseID}>
                        {r.Title || r.ReleaseID}
                      </p>
                      <p className="mt-1 truncate text-[10px] text-fg-subtle" title={r.ReleaseID}>
                        {r.ReleaseID}
                      </p>
                      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-fg-muted">
                        <span>
                          {formatBytes(r.Bytes)} retained
                          {r.Size > 0
                            ? ` · ${Math.min(100, Math.round((r.Bytes / r.Size) * 100))}% of source bytes`
                            : ""}
                        </span>
                        <span>
                          {r.Plays} {r.Plays === 1 ? "play" : "plays"}
                        </span>
                        <span className="inline-flex items-center gap-1">
                          <Clock3 size={11} />
                          <Age value={r.LastUsed} />
                        </span>
                        {r.Pins?.map((p) => (
                          <Badge key={p} variant="secondary">
                            {p === "owner" ? "Kept by you" : p}
                          </Badge>
                        ))}
                      </div>
                      {r.Size > 0 && (
                        <div className="mt-2 h-1 w-full max-w-xs overflow-hidden rounded-full bg-primary/10">
                          <div
                            className="h-full rounded-full bg-primary/50"
                            style={{ width: `${Math.min(100, (r.Bytes / r.Size) * 100)}%` }}
                          />
                        </div>
                      )}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      title={
                        r.Pins?.includes("owner") ? "Release owner pin" : "Keep until unpinned"
                      }
                      aria-label={`${r.Pins?.includes("owner") ? "Unpin" : "Pin"} ${r.Title || r.ReleaseID}`}
                      disabled={busy !== null}
                      onClick={() =>
                        void action("pin", {
                          releaseId: r.ReleaseID,
                          pinned: !r.Pins?.includes("owner"),
                        })
                      }
                    >
                      <Pin
                        size={15}
                        className={r.Pins?.includes("owner") ? "fill-primary text-primary" : ""}
                      />
                    </Button>
                  </div>
                ))}
                {releases.length > 100 && (
                  <p className="p-4 text-xs text-fg-muted">
                    Showing the 100 most recent matches. Search to narrow the list.
                  </p>
                )}
              </div>
            )}
            <p className="border-t border-border bg-bg-subtle px-4 py-3 text-[11px] leading-relaxed text-fg-muted">
              Coverage describes stored bytes. Managed preloads carry a preloaded pin; they are not
              a standalone offline library. Seeking, another release, or an expired provider link
              can still need your source.
            </p>
          </section>
          <p className="break-all text-[11px] text-fg-subtle">
            <HardDrive size={12} className="mr-1 inline" />
            {snap?.directory ?? config.data?.cache.dir}
          </p>
          <Drawer
            open={advanced}
            onClose={() => setAdvanced(false)}
            title="Fine tune your cache"
            size="lg"
          >
            <div className="space-y-6 p-1">
              <p className="text-sm text-fg-muted">
                Changes join your draft. Save the policy when you’re ready.
              </p>
              <Field
                label="Maximum preloaded movies"
                hint="Up to 5,000. The allocation still limits how many fit."
                htmlFor="preload-count"
              >
                <NumberInput
                  id="preload-count"
                  min={1}
                  max={5000}
                  value={draft.preloadMaxTitles || 200}
                  onChange={(n) => patch({ preloadMaxTitles: n })}
                />
              </Field>
              <Field
                label="Preload speed (MiB/s)"
                hint="Average payload cap, with short bursts for each 16 MiB window. Zero removes the cap."
                htmlFor="preload-rate"
              >
                <NumberInput
                  id="preload-rate"
                  min={0}
                  max={10000}
                  value={draft.preloadRateMiB ?? 0}
                  onChange={(n) => patch({ preloadRateMiB: n })}
                />
              </Field>
              <Field
                label="Keep disk space free (GiB)"
                hint="Preloading waits below this reserve. Zero uses the 5 GiB default."
                htmlFor="preload-reserve"
              >
                <NumberInput
                  id="preload-reserve"
                  min={0}
                  value={(draft.preloadMinFreeBytes ?? 5 * GiB) / GiB}
                  onChange={(n) => patch({ preloadMinFreeBytes: Math.round(n * GiB) })}
                />
              </Field>
              <Toggle
                title="Preload during playback"
                description="Otherwise, complete-title filling waits while viewers are streaming. Opening and next-episode warming use their separate controls."
                checked={draft.preloadDuringPlayback ?? false}
                onChange={(v) => patch({ preloadDuringPlayback: v })}
              />
              <Field
                label="Opening window (MiB)"
                hint="2–64 MiB for detail pages. Larger windows use more bandwidth; startup still depends on the source and client."
                htmlFor="cache-opening"
              >
                <NumberInput
                  id="cache-opening"
                  min={2}
                  max={64}
                  value={(draft.detailBytes || 2 * MiB) / MiB}
                  onChange={(n) => patch({ detailBytes: Math.round(n * MiB) })}
                />
              </Field>
              <Field label="Next episode / resume window (MiB)" htmlFor="cache-prefetch">
                <NumberInput
                  id="cache-prefetch"
                  min={1}
                  value={draft.prefetchBytes / MiB}
                  onChange={(n) => patch({ prefetchBytes: Math.round(n * MiB) })}
                />
              </Field>
              <Field label="Prepare next episode at (%)" htmlFor="cache-trigger">
                <NumberInput
                  id="cache-trigger"
                  min={1}
                  max={99}
                  value={Math.round(draft.nextEpisodeAt * 100)}
                  onChange={(n) => patch({ nextEpisodeAt: n / 100 })}
                />
              </Field>
              <Field
                label="Replay credit (hours per repeat play)"
                hint="Adaptive mode gives up to eight repeat plays a recency credit. It never overrides expiry or pins. Credit fades out after 30 days without a play."
                htmlFor="cache-boost"
              >
                <NumberInput
                  id="cache-boost"
                  min={0}
                  max={720}
                  value={draft.replayBoostHours ?? 0}
                  onChange={(n) => patch({ replayBoostHours: n })}
                />
              </Field>
              <Field
                label="Resume protection share (%)"
                hint="Maximum share of the budget assigned to in-progress releases. The most recently watched are considered first."
                htmlFor="cache-share"
              >
                <NumberInput
                  id="cache-share"
                  min={1}
                  max={100}
                  value={draft.inProgressPinPercent || 50}
                  onChange={(n) => patch({ inProgressPinPercent: n })}
                />
              </Field>
              <Field
                label="Resume protection age"
                hint="A duration such as 720h (30 days). Abandoned titles lose their resume pin after this interval."
                htmlFor="cache-age"
              >
                <Input
                  id="cache-age"
                  value={draft.inProgressPinMaxAge || "720h"}
                  onChange={(e) => patch({ inProgressPinMaxAge: e.target.value })}
                />
              </Field>
              <Field
                label="Cache directory"
                hint="Changing the folder requires a server restart. Existing bytes are not moved. The live overview shows the active folder."
                htmlFor="cache-dir"
              >
                <Input
                  id="cache-dir"
                  value={draft.dir}
                  onChange={(e) => patch({ dir: e.target.value })}
                />
              </Field>
              <p className="text-xs leading-relaxed text-fg-muted">
                Detail warming is rate limited and pauses during active playback. Trailer and
                post-play warming also require background warming here and their own feature
                settings.
              </p>
              <Button onClick={() => setAdvanced(false)}>Done</Button>
            </div>
          </Drawer>
        </div>
      }
    />
  );
}
