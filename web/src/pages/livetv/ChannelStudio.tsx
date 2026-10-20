import {
  Avatar,
  Badge,
  Button,
  Input,
  SearchSelect,
  SubtabNav,
  Switch,
  cn,
} from "@braedonsaunders/appkit-ui";
import {
  Clapperboard,
  Film,
  KeyRound,
  Layers,
  Play,
  Plus,
  Radio,
  Sparkles,
  Square,
  Star,
  Tags,
  Trash2,
  Users,
  X,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Artwork } from "../../components/Artwork";
import { ToolbarPicker } from "../../components/ToolbarPicker";
import { DragHandle, SortableList } from "../../components/Sortable";
import { Field, Segmented } from "../../components/fields";
import { errorMessage, isAbort } from "../../lib/api";
import type { CollectionSpec } from "../../lib/contract";
import type { EtherUser } from "../../lib/types";
import { plural, relativeTime } from "../../lib/format";
import { useDebouncedValue } from "../../lib/hooks";
import { ChannelLogo } from "./ChannelLogo";
import { MapGuideButton } from "./GuideData";
import { LivePreview } from "./LivePreview";
import { ProgrammePicker } from "./ProgrammePicker";
import { Timeline } from "./Timeline";
import {
  MINUTE,
  clock,
  duration,
  hueOf,
  localBlockTime,
  minutesOfDay,
  onNow,
  programmingMode,
  progress,
  timeOfDay,
  tv,
  upNext,
  type Block,
  type Channel,
  type Coverage,
  type Programme,
  type Source,
  type SourceStatus,
  type StreamSource,
} from "./model";

export type Mode = "movies" | "collection" | "genre";
export type StudioDraft = { channel: Channel; mode: Mode; genre: string };
export type CollectionChoice = CollectionSpec & { ratingKey?: string };

export function studioDraft(c: Channel): StudioDraft {
  return {
    channel: structuredClone(c),
    mode: programmingMode(c),
    genre: Array.isArray(c.filter?.genres) ? (c.filter.genres as string[]).join(", ") : "",
  };
}

/** The channel the server would receive, or an error explaining what's missing. */
export function compose(d: StudioDraft): Channel {
  const c = d.channel;
  if (c.kind === "virtual") {
    if (d.mode === "collection" && !c.collection)
      throw new Error("Choose a collection for this channel.");
    if (
      d.mode === "genre" &&
      !d.genre.trim() &&
      !Object.keys(c.filter || {}).some((key) => key !== "genres" && key !== "type")
    )
      throw new Error("Enter at least one genre for this channel.");
    if (d.mode === "movies" && !c.items.length)
      throw new Error("Add at least one movie to this channel.");
    let end = 0;
    for (const block of [...(c.blocks ?? [])].sort((a, b) => a.start - b.start)) {
      if (
        !Number.isFinite(block.start) ||
        !Number.isFinite(block.end) ||
        block.start < end ||
        block.start < 0 ||
        block.end <= block.start ||
        block.end > 1440
      )
        throw new Error("Time blocks must have valid start and end times without overlapping.");
      if (!block.items.length)
        throw new Error("Add at least one movie to each time block, or remove the empty block.");
      end = block.end;
    }
    return {
      ...c,
      items: d.mode === "movies" ? c.items : c.items,
      blocks: c.blocks ?? [],
      collection: d.mode === "collection" ? c.collection : undefined,
      filter:
        d.mode === "genre"
          ? {
              ...c.filter,
              type: c.filter?.type === "show" ? "show" : "movie",
              genres: d.genre
                .split(",")
                .map((s) => s.trim())
                .filter(Boolean),
            }
          : undefined,
    };
  }
  return c;
}

/**
 * One channel, edited in place: a live hero (what's on, what's next), its
 * schedule as a ribbon, and tabs for programming, schedule, guide, access
 * and details.
 */
export function ChannelStudio({
  draft,
  onChange,
  isNew,
  dirty,
  guide,
  coverage,
  sources,
  users,
  collections,
  now,
  onDelete,
  onMapGuide,
}: {
  draft: StudioDraft;
  onChange: (d: StudioDraft) => void;
  isNew: boolean;
  /** Unsaved edits: the player still tunes the saved channel. */
  dirty?: boolean;
  guide: Programme[];
  coverage?: Coverage;
  sources: Source[];
  users: EtherUser[];
  collections: CollectionChoice[];
  now: number;
  onDelete: () => void;
  onMapGuide: (c: Channel) => Promise<void>;
}) {
  const c = draft.channel;
  const set = (patch: Partial<Channel>) => onChange({ ...draft, channel: { ...c, ...patch } });
  const virtual = c.kind === "virtual";
  const [tab, setTab] = useState(virtual ? "programming" : "stream");
  const [watching, setWatching] = useState(false);
  useEffect(() => setWatching(false), [c.id]);
  useEffect(() => {
    setTab(virtual ? "programming" : "stream");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [c.id, isNew]);

  // Virtual channels preview their draft schedule live; saved IPTV channels
  // show the guide the server already has.
  const composed = useMemo(() => {
    try {
      return { channel: compose(draft), error: "" };
    } catch (e) {
      return { channel: null, error: errorMessage(e) };
    }
  }, [draft]);
  const previewKey = useDebouncedValue(
    virtual && composed.channel ? JSON.stringify(composed.channel) : "",
    500,
  );
  const [preview, setPreview] = useState<Programme[] | null>(null);
  const [previewError, setPreviewError] = useState("");
  useEffect(() => {
    if (!previewKey) {
      setPreview(null);
      return;
    }
    const ctl = new AbortController();
    tv.preview(JSON.parse(previewKey) as Channel, ctl.signal)
      .then((p) => {
        setPreview(p);
        setPreviewError("");
      })
      .catch((e) => {
        if (!isAbort(e)) setPreviewError(errorMessage(e));
      });
    return () => ctl.abort();
  }, [previewKey]);

  const schedule = virtual ? (preview ?? (isNew ? [] : guide)) : guide;
  const live = onNow(schedule, now);
  const next = upNext(schedule, now);
  const windowFrom = now - 30 * MINUTE;
  const windowUntil = virtual ? now + 6 * 60 * MINUTE : now + 8 * 60 * MINUTE;
  const hue = hueOf(c.id || c.name || "new");
  const source = sources.find((s) => s.id === c.sourceId);

  const tabs = virtual
    ? [
        { key: "programming", label: "Programming" },
        { key: "schedule", label: "Schedule", count: c.blocks?.length || undefined },
        { key: "viewers", label: "Viewers" },
        { key: "details", label: "Details" },
      ]
    : [
        { key: "stream", label: "Stream" },
        { key: "guide", label: "Guide" },
        { key: "viewers", label: "Viewers" },
        { key: "details", label: "Details" },
      ];

  return (
    <div className="pb-10">
      {watching && c.id ? <LivePreview channel={c} onClose={() => setWatching(false)} /> : null}
      {/* hero */}
      <section
        className="relative overflow-hidden border-b border-border"
        style={{
          background: `radial-gradient(120% 140% at 0% 0%, hsl(${hue} 70% 45% / 0.28), transparent 60%), radial-gradient(120% 140% at 100% 100%, hsl(${(hue + 60) % 360} 70% 40% / 0.18), transparent 55%)`,
        }}
      >
        {live?.item.thumb ? (
          <Artwork
            src={live.item.thumb}
            alt=""
            title=""
            className="pointer-events-none absolute inset-y-0 right-0 hidden w-1/2 opacity-25 blur-[1px] [mask-image:linear-gradient(to_left,black,transparent)] sm:block"
            imgClassName="object-cover"
          />
        ) : null}
        <div className="relative flex flex-wrap items-start gap-4 p-4 sm:p-6">
          <ChannelLogo
            name={c.name || "New channel"}
            logo={c.logo}
            seed={c.id || c.name}
            size={72}
            className="rounded-2xl shadow-lg"
          />
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="rounded-md bg-fg/90 px-1.5 py-0.5 font-mono font-semibold tabular-nums text-bg">
                {c.number || "—"}
              </span>
              <Badge variant="secondary" className="gap-1">
                {virtual ? <Sparkles size={11} /> : <Radio size={11} />}
                {virtual ? "Virtual" : source?.name || "IPTV"}
              </Badge>
              {c.group ? <Badge variant="secondary">{c.group}</Badge> : null}
              {!c.enabled ? <Badge variant="warning">Off air</Badge> : null}
              {isNew ? <Badge variant="info">Not saved yet</Badge> : null}
            </div>
            <input
              value={c.name}
              onChange={(e) => set({ name: e.currentTarget.value })}
              placeholder="Name this channel"
              aria-label="Name"
              className="w-full min-w-0 bg-transparent text-2xl font-semibold tracking-tight text-fg placeholder:text-fg-subtle focus:outline-none"
            />
            {live ? (
              <div className="max-w-xl space-y-1.5">
                <div className="flex items-center gap-2 text-sm">
                  <span className="inline-flex items-center gap-1 rounded bg-danger px-1.5 py-0.5 text-[10px] font-bold uppercase tracking-wider text-white">
                    <span className="size-1.5 animate-pulse rounded-full bg-white" /> On now
                  </span>
                  <span className="min-w-0 truncate font-medium text-fg">{live.item.title}</span>
                  <span className="shrink-0 text-xs tabular-nums text-fg-muted">
                    {duration(Date.parse(live.end) - now)} left
                  </span>
                </div>
                <div className="h-1 overflow-hidden rounded-full bg-fg/10">
                  <div
                    className="h-full rounded-full bg-danger"
                    style={{ width: `${progress(live, now) * 100}%` }}
                  />
                </div>
                {next ? (
                  <p className="truncate text-xs text-fg-muted">
                    Up next at {clock(next.start)} ·{" "}
                    <span className="text-fg">{next.item.title}</span>
                  </p>
                ) : null}
              </div>
            ) : (
              <p className="text-sm text-fg-muted">
                {virtual
                  ? composed.error || previewError || "Building the schedule…"
                  : coverage && coverage.programmes === 0
                    ? "No listings for this channel yet. Map a guide channel on the Guide tab."
                    : "Live stream"}
              </p>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-3">
            {!isNew ? (
              <button
                type="button"
                onClick={() => setWatching((w) => !w)}
                aria-pressed={watching}
                title={dirty ? "Plays the saved channel" : undefined}
                className={cn(
                  "group inline-flex h-9 items-center gap-2 rounded-full pl-1 pr-3.5 text-sm font-semibold shadow-sm transition-all",
                  watching
                    ? "bg-danger text-white"
                    : "bg-fg text-bg hover:scale-[1.03] hover:shadow-md",
                )}
              >
                <span
                  className={cn(
                    "grid size-7 place-items-center rounded-full",
                    watching ? "bg-white/20" : "bg-bg/15",
                  )}
                >
                  {watching ? (
                    <Square size={11} className="fill-current" />
                  ) : (
                    <Play size={13} className="ml-0.5 fill-current" />
                  )}
                </span>
                {watching ? "Stop" : "Watch"}
              </button>
            ) : null}
            <button
              type="button"
              onClick={() => set({ favorite: !c.favorite })}
              aria-pressed={c.favorite}
              aria-label="Favourite"
              className={cn(
                "grid size-9 place-items-center rounded-full border transition-colors",
                c.favorite
                  ? "border-warning/50 bg-warning/15 text-warning"
                  : "border-border bg-surface text-fg-subtle hover:text-warning",
              )}
            >
              <Star size={16} className={cn(c.favorite && "fill-warning")} />
            </button>
            <label className="flex items-center gap-2 rounded-full border border-border bg-surface py-1 pl-3 pr-1.5 text-sm font-medium">
              On air
              <Switch
                aria-label="Enabled"
                checked={c.enabled}
                onChange={(e) => set({ enabled: e.currentTarget.checked })}
              />
            </label>
          </div>
        </div>
        <div className="relative px-4 pb-4 sm:px-6">
          <Timeline
            programmes={schedule}
            from={windowFrom}
            until={windowUntil}
            now={now}
            label={`${c.name || "Channel"} schedule`}
          />
        </div>
      </section>

      <div className="sticky top-0 z-20 border-b border-border bg-bg/95 px-4 backdrop-blur sm:px-6">
        <SubtabNav ariaLabel="Channel settings" active={tab} onSelect={setTab} tabs={tabs} />
      </div>

      <div className="space-y-5 p-4 sm:p-6">
        {tab === "programming" && virtual ? (
          <Programming draft={draft} onChange={onChange} collections={collections} />
        ) : null}
        {tab === "schedule" && virtual ? <Schedule channel={c} onChange={set} /> : null}
        {tab === "stream" && !virtual ? (
          <section className="max-w-2xl space-y-4">
            <StreamSources channel={c} sources={sources} onChange={set} />
            <Field label="Group" htmlFor="ch-group">
              <Input
                id="ch-group"
                value={c.group || ""}
                onChange={(e) => set({ group: e.currentTarget.value })}
              />
            </Field>
            <label className="flex items-start gap-3 rounded-xl border border-border bg-surface p-3">
              <Switch
                checked={c.transcode || false}
                onChange={(e) => set({ transcode: e.currentTarget.checked })}
                aria-label="Transcode IPTV codecs for client compatibility"
              />
              <span>
                <span className="block text-sm font-medium text-fg">
                  Transcode for compatibility
                </span>
                <span className="block text-xs text-fg-muted">
                  Convert unusual codecs so every Plex app can play this channel. Costs CPU while
                  someone watches.
                </span>
              </span>
            </label>
          </section>
        ) : null}
        {tab === "guide" && !virtual ? (
          <GuideTab
            channel={c}
            guide={guide}
            coverage={coverage}
            sources={sources}
            now={now}
            onMap={onMapGuide}
            isNew={isNew}
          />
        ) : null}
        {tab === "viewers" ? (
          <ViewerAccess
            allowed={c.allowedUsers ?? []}
            users={users}
            onChange={(allowedUsers) => set({ allowedUsers })}
          />
        ) : null}
        {tab === "details" ? (
          <section className="max-w-2xl space-y-4">
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label="Number" htmlFor="ch-number">
                <Input
                  id="ch-number"
                  value={c.number}
                  onChange={(e) => set({ number: e.currentTarget.value })}
                  className="font-mono tabular-nums"
                />
              </Field>
              <Field label="Content rating fallback" htmlFor="ch-rating" className="sm:col-span-2">
                <Input
                  id="ch-rating"
                  value={c.rating || ""}
                  onChange={(e) => set({ rating: e.currentTarget.value })}
                  placeholder="TV-PG"
                />
              </Field>
            </div>
            <Field
              label="Logo URL"
              htmlFor="ch-logo"
              hint="Leave blank and Ether draws one from the name."
            >
              <div className="flex items-center gap-3">
                <ChannelLogo name={c.name} logo={c.logo} seed={c.id || c.name} size={36} />
                <Input
                  id="ch-logo"
                  value={c.logo || ""}
                  onChange={(e) => set({ logo: e.currentTarget.value })}
                  placeholder="https://…/logo.png"
                />
              </div>
            </Field>
            {virtual ? (
              <details className="rounded-xl border border-border bg-surface p-4">
                <summary className="cursor-pointer text-sm font-medium text-fg">
                  Advanced schedule settings
                </summary>
                <div className="mt-4 grid gap-4 sm:grid-cols-2">
                  <Field
                    label="Schedule anchor (UTC)"
                    htmlFor="ch-epoch"
                    hint="Start time of the repeating running order."
                  >
                    <Input
                      id="ch-epoch"
                      type="datetime-local"
                      value={c.epoch.slice(0, 16)}
                      onChange={(e) => set({ epoch: `${e.currentTarget.value}:00Z` })}
                    />
                  </Field>
                  <Field label="Shuffle seed" htmlFor="ch-seed">
                    <Input
                      id="ch-seed"
                      type="number"
                      value={c.seed}
                      onChange={(e) => set({ seed: Number(e.currentTarget.value) })}
                    />
                  </Field>
                </div>
              </details>
            ) : null}
            {!isNew ? (
              <div className="flex items-center justify-between gap-3 rounded-xl border border-danger/30 bg-danger/5 p-4">
                <div>
                  <p className="text-sm font-medium text-fg">Delete channel</p>
                  <p className="text-xs text-fg-muted">
                    {virtual
                      ? "Removes it from every viewer's guide."
                      : "It comes back on the next source refresh unless you turn it off instead."}
                  </p>
                </div>
                <Button variant="outline" className="text-danger" onClick={onDelete}>
                  <Trash2 size={14} /> Delete channel
                </Button>
              </div>
            ) : null}
          </section>
        ) : null}
      </div>
    </div>
  );
}

function newSourceId(): string {
  return Math.random().toString(36).slice(2, 10);
}

/**
 * Ordered stream sources: the first enabled one plays and the rest take over
 * in order when it fails. A single source keeps using the legacy streamUrl.
 */
function StreamSources({
  channel: c,
  sources,
  onChange,
}: {
  channel: Channel;
  sources: Source[];
  onChange: (p: Partial<Channel>) => void;
}) {
  // Each input signs in with the login it names. "Default" means the
  // playlist's own entry uses the channel's source and added backups go
  // anonymous, so one provider's login never reaches another's URLs.
  const logins = sources.filter((x) => x.auth?.type);
  const loginOptions = [
    {
      value: "",
      label: "Default",
      hint: "Imported entry uses its source's login; added backups use none",
    },
    ...logins.map((x) => ({
      value: x.id,
      label: x.name,
      hint: x.id === c.sourceId ? "This channel's source" : "Use this source's login",
    })),
  ];
  const list: StreamSource[] = c.streamSources?.length
    ? [...c.streamSources].sort((a, b) => a.priority - b.priority)
    : [{ id: "primary", name: "Primary", url: c.streamUrl || "", enabled: true, priority: 0 }];
  const [status, setStatus] = useState<Map<string, SourceStatus>>(new Map());
  useEffect(() => {
    if (!c.id) return;
    const ctl = new AbortController();
    const load = () =>
      tv
        .sourceStatus(c.id, ctl.signal)
        .then((list) => setStatus(new Map(list.map((x) => [x.id, x]))))
        .catch(() => undefined);
    void load();
    const t = setInterval(load, 30_000);
    return () => {
      ctl.abort();
      clearInterval(t);
    };
  }, [c.id]);
  const commit = (next: StreamSource[]) => {
    const ranked = next.map((x, i) => ({ ...x, priority: i }));
    const first = ranked.find((x) => x.enabled && x.url.trim());
    onChange({
      // Once a channel has a source list, always send it (even 0/1 entries)
      // so removing a backup actually removes it server-side.
      streamSources: c.streamSources !== undefined || ranked.length > 1 ? ranked : undefined,
      streamUrl: first?.url ?? ranked[0]?.url ?? "",
    });
  };
  const update = (i: number, patch: Partial<StreamSource>) =>
    commit(list.map((x, n) => (n === i ? { ...x, ...patch } : x)));
  const labels = new Map<string, string>();
  let rank = 0;
  for (const x of list) {
    const live = x.enabled && x.url.trim() !== "";
    labels.set(x.id, !live ? "Off" : rank === 0 ? "Primary" : `Backup ${rank}`);
    if (live) rank++;
  }
  if (list.length === 1) labels.set(list[0]!.id, "Primary");
  return (
    <section className="space-y-3" aria-label="Stream sources">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-fg">Stream sources</h3>
          <p className="text-xs text-fg-muted">
            The first working source plays; if it drops, Ether moves down the list. Only you see
            these URLs.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() =>
            commit([
              ...list,
              {
                id: newSourceId(),
                name: `Backup ${list.length}`,
                url: "",
                enabled: true,
                priority: list.length,
              },
            ])
          }
        >
          <Plus size={13} /> Add backup
        </Button>
      </div>
      <SortableList
        items={list}
        getId={(x) => x.id}
        onReorder={(next) => commit(next)}
        className="space-y-2"
        renderItem={(x, handle, i) => {
          const label = labels.get(x.id) ?? "Off";
          return (
            <div
              className={cn(
                "flex items-center gap-2 rounded-xl border bg-surface p-2",
                label === "Primary" ? "border-primary/40" : "border-border",
                !x.enabled && "opacity-60",
              )}
            >
              {list.length > 1 ? (
                <DragHandle
                  handle={handle}
                  label={`Reorder ${x.name || "source"}`}
                  className="size-7"
                />
              ) : null}
              <span
                className={cn(
                  "w-[4.75rem] shrink-0 whitespace-nowrap rounded-md px-1.5 py-0.5 text-center text-[10.5px] font-semibold uppercase tracking-wider",
                  label === "Primary"
                    ? "bg-primary-subtle text-primary"
                    : label === "Off"
                      ? "bg-bg-subtle text-fg-subtle"
                      : "bg-info/15 text-info",
                )}
              >
                {label}
              </span>
              {list.length > 1 ? (
                <Input
                  value={x.name}
                  onChange={(e) => update(i, { name: e.currentTarget.value })}
                  aria-label={`Source ${i + 1} name`}
                  className="h-8 w-32 shrink-0 text-sm"
                />
              ) : null}
              <SourceHealth status={status.get(x.id)} />
              {logins.length ? (
                <ToolbarPicker
                  ariaLabel={`Login for source ${i + 1}`}
                  value={x.authSourceId ?? ""}
                  onChange={(v) => update(i, { authSourceId: v || undefined })}
                  options={loginOptions.map((o) => ({ ...o, icon: <KeyRound size={13} /> }))}
                  className="h-8 max-w-[9rem] shrink-0 text-xs"
                  menuClassName="w-64"
                />
              ) : null}
              <Input
                value={x.url}
                onChange={(e) => update(i, { url: e.currentTarget.value })}
                aria-label={list.length > 1 ? `Source ${i + 1} URL` : "Stream URL"}
                placeholder="https://…/stream.m3u8"
                className="h-8 min-w-0 flex-1 font-mono text-xs"
              />
              {list.length > 1 ? (
                <>
                  <Switch
                    checked={x.enabled}
                    onChange={(e) => update(i, { enabled: e.currentTarget.checked })}
                    aria-label={`Use source ${i + 1}`}
                  />
                  <Button
                    variant="ghost"
                    size="sm"
                    className="size-7 p-0 text-fg-subtle hover:text-danger"
                    aria-label={`Remove source ${i + 1}`}
                    onClick={() => commit(list.filter((_, n) => n !== i))}
                  >
                    <X size={14} />
                  </Button>
                </>
              ) : null}
            </div>
          );
        }}
      />
    </section>
  );
}

const HEALTH: Record<string, { label: string; className: string }> = {
  available: { label: "Healthy", className: "bg-success/15 text-success" },
  cooling_down: { label: "Cooling down", className: "bg-warning/15 text-warning" },
  retry_ready: { label: "Will retry", className: "bg-info/15 text-info" },
  disabled: { label: "Off", className: "bg-bg-subtle text-fg-subtle" },
  unchecked: { label: "Untested", className: "bg-bg-subtle text-fg-subtle" },
};

/** Go's zero time ("0001-01-01T00:00:00Z") means "never". */
const when = (t?: string) => (t && Date.parse(t) > 0 ? t : undefined);

/** Live health of one stream source, from the server's tune history. */
function SourceHealth({ status }: { status?: SourceStatus }) {
  if (!status) return null;
  const played = when(status.lastPlayed);
  const retry = when(status.retryAfter);
  const h = HEALTH[status.state] ?? {
    label: status.state,
    className: "bg-bg-subtle text-fg-subtle",
  };
  const detail = [
    status.failures ? `${plural(status.failures, "failure")}` : "",
    played ? `played ${relativeTime(played)}` : "",
    retry && status.state === "cooling_down" ? `retry ${relativeTime(retry)}` : "",
    status.lastError ?? "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <span
      title={detail || undefined}
      className={cn(
        "shrink-0 whitespace-nowrap rounded-full px-2 py-0.5 text-[11px] font-medium",
        h.className,
      )}
    >
      {h.label}
    </span>
  );
}

function Programming({
  draft,
  onChange,
  collections,
}: {
  draft: StudioDraft;
  onChange: (d: StudioDraft) => void;
  collections: CollectionChoice[];
}) {
  const c = draft.channel;
  const set = (patch: Partial<Channel>) => onChange({ ...draft, channel: { ...c, ...patch } });
  const modes: { value: Mode; label: string; hint: string; icon: React.ReactNode }[] = [
    {
      value: "movies",
      label: "Hand-picked",
      hint: "Choose the movies and their order",
      icon: <Film size={18} />,
    },
    {
      value: "collection",
      label: "Collection",
      hint: "Play a collection; shows expand into episodes",
      icon: <Layers size={18} />,
    },
    {
      value: "genre",
      label: "Genres",
      hint: "Everything in the catalogue that fits",
      icon: <Tags size={18} />,
    },
  ];
  const known = collections.filter((x) => x.ratingKey);
  const collectionValue = c.collection ? String(c.collection) : "";
  return (
    <div className="space-y-5">
      <div role="radiogroup" aria-label="Programming source" className="grid gap-2 sm:grid-cols-3">
        {modes.map((m) => {
          const on = draft.mode === m.value;
          return (
            <button
              key={m.value}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => onChange({ ...draft, mode: m.value })}
              className={cn(
                "flex items-start gap-3 rounded-xl border p-3 text-left transition-all",
                on
                  ? "border-primary/50 bg-primary-subtle ring-1 ring-primary/30"
                  : "border-border bg-surface hover:border-border-strong",
              )}
            >
              <span className={cn("mt-0.5", on ? "text-primary" : "text-fg-muted")}>{m.icon}</span>
              <span>
                <span
                  className={cn("block text-sm font-semibold", on ? "text-primary" : "text-fg")}
                >
                  {m.label}
                </span>
                <span className="block text-xs text-fg-muted">{m.hint}</span>
              </span>
            </button>
          );
        })}
      </div>

      {draft.mode === "movies" ? (
        <ProgrammePicker
          label="Base programming"
          items={c.items}
          onChange={(items) => set({ items })}
        />
      ) : null}

      {draft.mode === "collection" ? (
        <Field
          label="Collection"
          hint="Manage collections in Collection Studio. Shows expand into episodes."
        >
          <SearchSelect
            ariaLabel="Collection"
            value={collectionValue}
            onChange={(v) => set({ collection: v ? Number(v) : undefined })}
            placeholder="Choose a collection"
            options={[
              ...known.map((x) => ({
                value: String(x.ratingKey),
                label: `${x.name}${x.itemCount !== undefined ? ` · ${x.itemCount} titles` : ""}`,
              })),
              ...(collectionValue && !known.some((x) => x.ratingKey === collectionValue)
                ? [{ value: collectionValue, label: "Previously selected collection" }]
                : []),
            ]}
          />
        </Field>
      ) : null}

      {draft.mode === "genre" ? (
        <div className="grid gap-4 sm:grid-cols-[auto_1fr]">
          <Field label="Catalogue">
            <Segmented
              ariaLabel="Catalogue type"
              value={c.filter?.type === "show" ? "show" : "movie"}
              onChange={(v) => set({ filter: { ...c.filter, type: v } })}
              options={[
                { value: "movie", label: "Movies", icon: <Clapperboard size={13} /> },
                { value: "show", label: "TV shows" },
              ]}
            />
          </Field>
          <Field
            label="Genres"
            htmlFor="ch-genres"
            hint="Separate with commas. Template year and rating limits are kept."
          >
            <Input
              id="ch-genres"
              value={draft.genre}
              onChange={(e) => onChange({ ...draft, genre: e.currentTarget.value })}
              placeholder="Action, Adventure"
            />
          </Field>
          <FilterChips filter={c.filter} />
        </div>
      ) : null}

      {draft.mode !== "movies" ? (
        <p className="rounded-lg bg-bg-subtle px-3 py-2 text-xs text-fg-muted">
          Saving takes a snapshot of the catalogue for a stable schedule. Save again later to pick
          up new titles.
        </p>
      ) : null}
      <label className="flex items-center gap-3 text-sm">
        <Switch
          checked={c.shuffle}
          onChange={(e) => set({ shuffle: e.currentTarget.checked })}
          aria-label="Shuffle"
        />
        <span>
          <span className="font-medium text-fg">Shuffle</span>
          <span className="ml-2 text-xs text-fg-muted">
            A stable shuffle; the guide never jumps.
          </span>
        </span>
      </label>
    </div>
  );
}

function FilterChips({ filter }: { filter?: Record<string, unknown> }) {
  if (!filter) return null;
  const chips: string[] = [];
  if (filter.yearFrom || filter.yearTo)
    chips.push(`${filter.yearFrom ?? "…"}–${filter.yearTo ?? "…"}`);
  if (filter.maxContentRating) chips.push(`Up to ${String(filter.maxContentRating)}`);
  if (!chips.length) return null;
  return (
    <div className="flex flex-wrap gap-1.5 sm:col-span-2">
      {chips.map((c) => (
        <Badge key={c} variant="secondary">
          {c}
        </Badge>
      ))}
    </div>
  );
}

function Schedule({
  channel: c,
  onChange,
}: {
  channel: Channel;
  onChange: (p: Partial<Channel>) => void;
}) {
  const blocks = c.blocks ?? [];
  const setBlocks = (b: Block[]) => onChange({ blocks: b });
  const add = (start?: number) => {
    const s = start ?? blocks.at(-1)?.end ?? 0;
    setBlocks([...blocks, { start: s, end: Math.min(1440, s + 60), items: [], shuffle: false }]);
  };
  const offset = -new Date().getTimezoneOffset();
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold text-fg">Daily time blocks</h3>
          <p className="text-xs text-fg-muted">
            Pin programming to times of day (in UTC
            {offset ? `; your time is UTC${offset > 0 ? "+" : ""}${offset / 60}` : ""}). The base
            programming fills the rest of the day.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={blocks.some((b) => b.end === 1440)}
          onClick={() => add()}
        >
          <Plus size={13} /> Add time block
        </Button>
      </div>

      {/* 24h day bar: click a free hour to add a block there */}
      <div className="space-y-1">
        <div className="relative h-12 overflow-hidden rounded-xl border border-border bg-[repeating-linear-gradient(135deg,transparent,transparent_6px,rgb(var(--ch-border)/0.5)_6px,rgb(var(--ch-border)/0.5)_7px)]">
          {Array.from({ length: 24 }, (_, h) => (
            <button
              key={h}
              type="button"
              aria-label={`Add a block at ${timeOfDay(h * 60)} UTC`}
              onClick={() => {
                const s = h * 60;
                if (!blocks.some((b) => s < b.end && s + 60 > b.start)) add(s);
              }}
              className="absolute inset-y-0 border-r border-border/40 hover:bg-primary/10"
              style={{ left: `${(h / 24) * 100}%`, width: `${100 / 24}%` }}
            />
          ))}
          {blocks.map((b, i) => {
            const hue = (i * 67 + 200) % 360;
            return (
              <div
                key={i}
                className="pointer-events-none absolute inset-y-1 overflow-hidden rounded-lg px-2 py-1 text-[11px] font-medium text-white shadow-sm"
                style={{
                  left: `${(b.start / 1440) * 100}%`,
                  width: `${((b.end - b.start) / 1440) * 100}%`,
                  background: `linear-gradient(135deg, hsl(${hue} 65% 50%), hsl(${(hue + 30) % 360} 65% 40%))`,
                }}
              >
                <span className="block truncate">Block {i + 1}</span>
                <span className="block truncate opacity-80">{plural(b.items.length, "title")}</span>
              </div>
            );
          })}
        </div>
        <div className="relative h-4 text-[10.5px] tabular-nums text-fg-subtle" aria-hidden>
          {[0, 3, 6, 9, 12, 15, 18, 21].map((h) => (
            <span key={h} className="absolute" style={{ left: `${(h / 24) * 100}%` }}>
              {timeOfDay(h * 60)}
            </span>
          ))}
        </div>
      </div>

      {blocks.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-xs text-fg-muted">
          No time blocks: the base programming plays all day. Click an hour above to add one.
        </p>
      ) : null}
      {blocks.map((block, index) => {
        const update = (patch: Partial<Block>) =>
          setBlocks(blocks.map((b, n) => (n === index ? { ...b, ...patch } : b)));
        return (
          <section key={index} className="space-y-3 rounded-xl border border-border bg-surface p-4">
            <div className="flex flex-wrap items-end gap-3">
              <span
                className="mb-2 size-3 rounded-full"
                style={{ background: `hsl(${(index * 67 + 200) % 360} 65% 50%)` }}
                aria-hidden
              />
              <Field
                label="Start (UTC)"
                htmlFor={`blk-${index}-start`}
                hint={localBlockTime(block.start) + " your time"}
              >
                <Input
                  id={`blk-${index}-start`}
                  type="time"
                  value={timeOfDay(block.start)}
                  onChange={(e) => update({ start: minutesOfDay(e.currentTarget.value) })}
                />
              </Field>
              <Field
                label="End (UTC)"
                htmlFor={`blk-${index}-end`}
                hint={block.end === 1440 ? "Midnight" : localBlockTime(block.end) + " your time"}
              >
                <div className="flex items-center gap-2">
                  <Input
                    id={`blk-${index}-end`}
                    aria-label={`Time block ${index + 1} end time`}
                    type="time"
                    disabled={block.end === 1440}
                    value={block.end === 1440 ? "00:00" : timeOfDay(block.end)}
                    onChange={(e) => update({ end: minutesOfDay(e.currentTarget.value) })}
                  />
                  <label className="flex items-center gap-1.5 whitespace-nowrap text-xs text-fg-muted">
                    <input
                      type="checkbox"
                      checked={block.end === 1440}
                      aria-label={`Time block ${index + 1} runs to midnight`}
                      onChange={(e) =>
                        update({
                          end: e.currentTarget.checked ? 1440 : Math.min(1439, block.start + 60),
                        })
                      }
                    />
                    To midnight
                  </label>
                </div>
              </Field>
              <label className="mb-2 flex items-center gap-2 text-sm">
                <Switch
                  checked={block.shuffle}
                  onChange={(e) => update({ shuffle: e.currentTarget.checked })}
                  aria-label={`Shuffle block ${index + 1}`}
                />
                Shuffle
              </label>
              <Button
                variant="ghost"
                size="sm"
                className="mb-1 ml-auto text-fg-subtle hover:text-danger"
                aria-label={`Remove block ${index + 1}`}
                onClick={() => setBlocks(blocks.filter((_, n) => n !== index))}
              >
                <X size={14} /> Remove
              </Button>
            </div>
            <ProgrammePicker
              label={`Time block ${index + 1} programming`}
              items={block.items}
              onChange={(items) => update({ items })}
            />
          </section>
        );
      })}
    </div>
  );
}

function GuideTab({
  channel: c,
  guide,
  coverage,
  sources,
  now,
  onMap,
  isNew,
}: {
  channel: Channel;
  guide: Programme[];
  coverage?: Coverage;
  sources: Source[];
  now: number;
  onMap: (c: Channel) => Promise<void>;
  isNew: boolean;
}) {
  const src = sources.find((s) => s.id === (c.guideSourceId || c.sourceId));
  const upcoming = guide.filter((p) => Date.parse(p.end) > now).slice(0, 8);
  const ok = (coverage?.programmes ?? guide.length) > 0;
  return (
    <div className="max-w-3xl space-y-4">
      <section className="flex flex-wrap items-center gap-4 rounded-xl border border-border bg-surface p-4">
        <div className="min-w-0 flex-1">
          <p className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">
            Listings from
          </p>
          {c.guideId ? (
            <p className="mt-1 truncate text-sm text-fg">
              <span className="font-mono">{c.guideId}</span>
              <span className="text-fg-muted"> in {src?.name ?? "an unknown source"}</span>
            </p>
          ) : (
            <p className="mt-1 text-sm text-fg-muted">Not mapped to a guide channel.</p>
          )}
          <p className={cn("mt-1 text-xs", ok ? "text-success" : "text-warning")}>
            {ok
              ? `${plural(coverage?.programmes ?? guide.length, "listing")} in the next day`
              : "No listings in the next day"}
          </p>
        </div>
        {isNew ? null : (
          <MapGuideButton
            channel={c}
            label={c.guideId ? "Change guide channel" : "Map guide channel"}
            variant="default"
            onMapped={onMap}
          />
        )}
      </section>
      {upcoming.length ? (
        <ol className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-surface">
          {upcoming.map((p) => {
            const live = Date.parse(p.start) <= now;
            return (
              <li key={p.id} className="flex gap-3 px-4 py-2.5">
                <span
                  className={cn(
                    "w-20 shrink-0 text-xs tabular-nums",
                    live ? "font-semibold text-danger" : "text-fg-muted",
                  )}
                >
                  {live ? "Now" : clock(p.start)}
                </span>
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium text-fg">{p.item.title}</span>
                  {p.item.summary ? (
                    <span className="line-clamp-2 text-xs text-fg-muted">{p.item.summary}</span>
                  ) : null}
                </span>
              </li>
            );
          })}
        </ol>
      ) : null}
    </div>
  );
}

function ViewerAccess({
  allowed,
  users,
  onChange,
}: {
  allowed: number[];
  users: EtherUser[];
  onChange: (ids: number[]) => void;
}) {
  const everyone = allowed.length === 0;
  return (
    <section className="max-w-3xl space-y-3">
      <div className="flex flex-wrap items-center gap-3 rounded-xl border border-border bg-surface p-3">
        <span className="grid size-9 place-items-center rounded-full bg-primary-subtle text-primary">
          <Users size={16} />
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-fg">
            {everyone ? "Everyone with Live TV access" : `Only ${plural(allowed.length, "viewer")}`}
          </p>
          <p className="text-xs text-fg-muted">
            Pick viewers to restrict this channel. Their parental limits still apply; per-viewer
            Live TV settings live on each viewer's profile.
          </p>
        </div>
        {!everyone ? (
          <Button variant="ghost" size="sm" onClick={() => onChange([])}>
            Allow everyone
          </Button>
        ) : null}
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        {users.map((user) => {
          const on = allowed.includes(user.id);
          const name = user.title || user.username;
          return (
            <label
              key={user.id}
              className={cn(
                "flex cursor-pointer items-center gap-3 rounded-xl border p-2.5 transition-colors",
                on
                  ? "border-primary/40 bg-primary-subtle"
                  : "border-border bg-surface hover:border-border-strong",
              )}
            >
              <Avatar src={(user as { thumb?: string }).thumb} name={name} size={30} />
              <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg">{name}</span>
              <Switch
                aria-label={name}
                checked={on}
                onChange={(e) =>
                  onChange(
                    e.currentTarget.checked
                      ? [...allowed, user.id]
                      : allowed.filter((id) => id !== user.id),
                  )
                }
              />
            </label>
          );
        })}
      </div>
    </section>
  );
}
