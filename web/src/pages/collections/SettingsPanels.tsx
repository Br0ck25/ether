import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  Input,
  MultiSelect,
  SearchSelect,
  Switch,
  cn,
} from "@braedonsaunders/appkit-ui";
import {
  Check,
  Clapperboard,
  Grid2x2,
  Image as ImageIcon,
  Link2,
  RefreshCw,
  Sparkles,
  Wand2,
} from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { Artwork } from "../../components/Artwork";
import { ChipGroup, Field, NumberInput, Segmented } from "../../components/fields";
import { useViewers, viewerName } from "../../components/pickers";
import { api } from "../../lib/api";
import type { ArtworkMode, CollectionSpec, DeviceClass, TitleCard } from "../../lib/contract";
import { WEEKDAYS, formatDateTime, formatHour, relativeTime } from "../../lib/format";
import { REFRESH_OPTIONS } from "../../lib/rules";
import type { SpecUpdate } from "./BuildPanel";

// ---- Schedule ---------------------------------------------------------------------

export function SchedulePanel({
  spec,
  update,
  onRebuild,
  rebuilding,
}: {
  spec: CollectionSpec;
  update: SpecUpdate;
  onRebuild?: () => void;
  rebuilding?: boolean;
}) {
  const rotate = spec.rotate ?? null;
  return (
    <div className="space-y-7">
      <Field label="Refresh" hint={REFRESH_OPTIONS.find((r) => r.value === spec.refresh)?.hint}>
        <Segmented
          ariaLabel="Refresh"
          value={spec.refresh}
          onChange={(v) => update({ refresh: v })}
          options={REFRESH_OPTIONS.map((r) => ({ value: r.value, label: r.label }))}
        />
      </Field>

      <div className="space-y-3">
        <ToggleRow
          title="Rotate members"
          description="Show a fresh subset each period so the row never goes stale."
          checked={Boolean(rotate)}
          onChange={(on) =>
            update({ rotate: on ? { size: Math.min(20, spec.limit || 20), every: "daily" } : null })
          }
        />
        {rotate ? (
          <div className="flex flex-wrap items-end gap-3 rounded-lg border border-border-subtle bg-bg-subtle p-3">
            <Field label="Titles shown">
              <NumberInput
                aria-label="Titles shown per rotation"
                value={rotate.size}
                min={1}
                className="w-24"
                onChange={(n) =>
                  update({ rotate: { ...rotate, size: Math.max(1, Math.round(n)) } }, "rotate-size")
                }
              />
            </Field>
            <Field label="Rotate">
              <Segmented
                ariaLabel="Rotation period"
                value={rotate.every}
                onChange={(v) => update({ rotate: { ...rotate, every: v } })}
                options={[
                  { value: "daily", label: "Daily" },
                  { value: "weekly", label: "Weekly" },
                ]}
              />
            </Field>
          </div>
        ) : null}
      </div>

      <div className="rounded-lg border border-border bg-surface p-4">
        <p className="text-sm font-medium text-fg">Build status</p>
        {spec.id ? (
          <>
            <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
              <dt className="text-fg-muted">Titles</dt>
              <dd className="tabular-nums text-fg">{spec.itemCount ?? "—"}</dd>
              <dt className="text-fg-muted">Last built</dt>
              <dd className="text-fg" title={formatDateTime(spec.lastBuiltAt)}>
                {relativeTime(spec.lastBuiltAt)}
              </dd>
              <dt className="text-fg-muted">Saved</dt>
              <dd className="text-fg" title={formatDateTime(spec.updatedAt)}>
                {relativeTime(spec.updatedAt)}
              </dd>
            </dl>
            {onRebuild ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={onRebuild}
                disabled={rebuilding}
              >
                <RefreshCw size={14} className={rebuilding ? "animate-spin" : ""} />{" "}
                {rebuilding ? "Rebuilding…" : "Rebuild now"}
              </Button>
            ) : null}
          </>
        ) : (
          <p className="mt-1 text-sm text-fg-muted">
            Save the collection to build it for the first time.
          </p>
        )}
      </div>
    </div>
  );
}

function ToggleRow({
  title,
  description,
  checked,
  onChange,
  disabled,
}: {
  title: ReactNode;
  description?: ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <label className="flex cursor-pointer items-start justify-between gap-4 rounded-lg border border-border bg-surface p-3">
      <span className="min-w-0">
        <span className="block text-sm font-medium text-fg">{title}</span>
        {description ? (
          <span className="mt-0.5 block text-xs text-fg-muted">{description}</span>
        ) : null}
      </span>
      <Switch
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.currentTarget.checked)}
        aria-label={typeof title === "string" ? title : undefined}
      />
    </label>
  );
}

// ---- Artwork ------------------------------------------------------------------------

const MODES: { value: ArtworkMode; label: string; hint: string; icon: ReactNode }[] = [
  {
    value: "auto",
    label: "Automatic",
    hint: "Ether picks the best style for the titles inside.",
    icon: <Wand2 size={16} />,
  },
  {
    value: "collage",
    label: "Poster collage",
    hint: "A grid of the top posters with the name.",
    icon: <Grid2x2 size={16} />,
  },
  {
    value: "backdrop",
    label: "Backdrop",
    hint: "The lead title's backdrop with the name set over it.",
    icon: <Clapperboard size={16} />,
  },
  {
    value: "tmdb",
    label: "TMDB artwork",
    hint: "The official poster of the TMDB collection or lead title.",
    icon: <ImageIcon size={16} />,
  },
  {
    value: "custom",
    label: "Custom image",
    hint: "Any image URL you choose.",
    icon: <Link2 size={16} />,
  },
];

const ACCENTS = [
  "rose",
  "amber",
  "lime",
  "emerald",
  "teal",
  "cyan",
  "sky",
  "indigo",
  "violet",
] as const;
const ACCENT_BG: Record<(typeof ACCENTS)[number], string> = {
  rose: "bg-accent-rose",
  amber: "bg-accent-amber",
  lime: "bg-accent-lime",
  emerald: "bg-accent-emerald",
  teal: "bg-accent-teal",
  cyan: "bg-accent-cyan",
  sky: "bg-accent-sky",
  indigo: "bg-accent-indigo",
  violet: "bg-accent-violet",
};

/** The accent palette is the categorical token set, resolved to hex for the server's renderer. */
function accentHex(name: string): string {
  if (typeof window === "undefined") return "";
  const raw = getComputedStyle(document.documentElement)
    .getPropertyValue(`--ch-accent-${name}`)
    .trim();
  const parts = raw.split(/\s+/).map(Number);
  if (parts.length !== 3 || parts.some((n) => !Number.isFinite(n))) return "";
  return `#${parts.map((n) => n.toString(16).padStart(2, "0")).join("")}`;
}

export function ArtworkPanel({
  spec,
  update,
  preview,
}: {
  spec: CollectionSpec;
  update: SpecUpdate;
  preview: TitleCard[];
}) {
  const swatches = useMemo(
    () => ACCENTS.map((a) => ({ name: a, hex: accentHex(a) })).filter((s) => s.hex),
    [],
  );
  const accent = spec.artwork.accent ?? "";
  return (
    <div className="space-y-6">
      <div className="grid gap-5 sm:grid-cols-[1fr_11rem]">
        <div role="radiogroup" aria-label="Artwork style" className="space-y-1.5">
          {MODES.map((m) => {
            const on = spec.artwork.mode === m.value;
            return (
              <button
                key={m.value}
                type="button"
                role="radio"
                aria-checked={on}
                onClick={() => update({ artwork: { ...spec.artwork, mode: m.value } })}
                className={cn(
                  "flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                  on
                    ? "border-primary/50 bg-primary-subtle"
                    : "border-border bg-surface hover:border-border-strong",
                )}
              >
                <span className={cn("mt-0.5", on ? "text-primary" : "text-fg-subtle")}>
                  {m.icon}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block text-sm font-medium text-fg">{m.label}</span>
                  <span className="block text-xs text-fg-muted">{m.hint}</span>
                </span>
                {on ? <Check size={16} className="text-primary" /> : null}
              </button>
            );
          })}
        </div>
        <div className="space-y-3">
          <p className="text-xs font-medium text-fg-muted">Live preview</p>
          <ArtworkPreview spec={spec} items={preview} />
          {spec.id ? (
            <div>
              <p className="mb-1.5 text-xs font-medium text-fg-muted">Published artwork</p>
              <Artwork
                src={api.artworkUrl(spec.id, 300, spec.updatedAt)}
                alt={`${spec.name} artwork`}
                title={spec.name}
                className="aspect-[2/3] w-full rounded-md ring-1 ring-border"
              />
            </div>
          ) : null}
        </div>
      </div>

      {spec.artwork.mode === "custom" ? (
        <Field
          label="Image URL"
          hint="A 2:3 poster works best. It's fetched by the server, so it must be reachable from it."
        >
          <Input
            type="url"
            value={spec.artwork.url ?? ""}
            placeholder="https://…"
            onChange={(e) =>
              update({ artwork: { ...spec.artwork, url: e.currentTarget.value } }, "artwork-url")
            }
            aria-label="Custom image URL"
          />
        </Field>
      ) : null}

      <Field label="Accent" hint="Tints generated artwork. Automatic matches the lead poster.">
        <div
          className="flex flex-wrap items-center gap-2"
          role="radiogroup"
          aria-label="Accent colour"
        >
          <button
            type="button"
            role="radio"
            aria-checked={!accent}
            onClick={() => update({ artwork: { ...spec.artwork, accent: undefined } })}
            className={cn(
              "h-8 rounded-full border px-3 text-xs font-medium",
              !accent
                ? "border-primary bg-primary-subtle text-primary"
                : "border-border text-fg-muted hover:text-fg",
            )}
          >
            <Sparkles size={12} className="mr-1 inline" />
            Automatic
          </button>
          {swatches.map((s) => (
            <button
              key={s.name}
              type="button"
              role="radio"
              aria-checked={accent.toLowerCase() === s.hex}
              aria-label={s.name}
              title={s.name}
              onClick={() => update({ artwork: { ...spec.artwork, accent: s.hex } })}
              className={cn(
                "size-8 rounded-full ring-offset-2 ring-offset-bg transition",
                ACCENT_BG[s.name],
                accent.toLowerCase() === s.hex ? "ring-2 ring-fg" : "hover:scale-110",
              )}
            />
          ))}
          {swatches.length ? (
            <label className="flex items-center gap-1.5 text-xs text-fg-muted">
              <input
                type="color"
                value={/^#[0-9a-f]{6}$/i.test(accent) ? accent : swatches[0]!.hex}
                onChange={(e) =>
                  update({ artwork: { ...spec.artwork, accent: e.currentTarget.value } }, "accent")
                }
                className="size-8 cursor-pointer rounded-full border border-border bg-transparent p-0"
                aria-label="Custom accent colour"
              />
              Custom
            </label>
          ) : null}
        </div>
      </Field>
    </div>
  );
}

/** Client-side approximation of the server's generated poster, from the preview titles. */
export function ArtworkPreview({
  spec,
  items,
  className,
  size = "md",
}: {
  spec: CollectionSpec;
  items: TitleCard[];
  className?: string;
  size?: "sm" | "md";
}) {
  const posters = items.filter((i) => i.poster);
  const mode =
    spec.artwork.mode === "auto"
      ? posters.length >= 4
        ? "collage"
        : "backdrop"
      : spec.artwork.mode;
  const accentStyle = spec.artwork.accent ? { backgroundColor: spec.artwork.accent } : undefined;
  const name = spec.name.trim() || "Untitled collection";
  return (
    <div
      className={cn(
        "relative aspect-[2/3] w-full overflow-hidden rounded-md bg-bg-subtle shadow-md ring-1 ring-border",
        className,
      )}
      aria-label="Artwork preview"
      role="img"
    >
      {mode === "collage" ? (
        <div className="grid size-full grid-cols-2 grid-rows-2 gap-px bg-border">
          {posters.slice(0, 4).map((p) => (
            <Artwork key={p.key} src={p.poster} alt="" title="" className="size-full" eager />
          ))}
        </div>
      ) : mode === "backdrop" ? (
        <Artwork
          src={items[0]?.backdrop ?? items[0]?.poster}
          alt=""
          title=""
          className="size-full"
          imgClassName="object-center"
          eager
        />
      ) : mode === "tmdb" ? (
        <Artwork src={items[0]?.poster} alt="" title={name} className="size-full" eager />
      ) : (
        <Artwork
          src={spec.artwork.url || undefined}
          alt=""
          title="Add an image URL"
          className="size-full"
          eager
        />
      )}
      {mode !== "tmdb" && mode !== "custom" ? (
        <div
          className={cn(
            "scrim-bottom absolute inset-x-0 bottom-0",
            size === "sm" ? "px-1.5 pb-1.5 pt-6" : "px-3 pb-3 pt-10",
          )}
        >
          <div
            className={cn(
              "rounded-full",
              size === "sm" ? "mb-1 h-0.5 w-4" : "mb-2 h-1 w-8",
              !accentStyle && "bg-primary",
            )}
            style={accentStyle}
          />
          <p
            className={cn(
              "text-on-scrim line-clamp-3 font-bold leading-tight",
              size === "sm" ? "text-[10px]" : "text-base",
            )}
          >
            {name}
          </p>
          {items.length && size !== "sm" ? (
            <p className="text-on-scrim mt-0.5 text-[10px] opacity-75">
              {spec.mediaType === "show" ? "Series" : "Collection"}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

// ---- Audience ------------------------------------------------------------------------

const DEVICES: { value: DeviceClass; label: string }[] = [
  { value: "tv", label: "TV" },
  { value: "mobile", label: "Phone & tablet" },
  { value: "web", label: "Web" },
  { value: "desktop", label: "Desktop app" },
];

export function AudiencePanel({ spec, update }: { spec: CollectionSpec; update: SpecUpdate }) {
  const { viewers } = useViewers();
  const t = spec.targets;
  const schedule = t.schedule ?? {};
  const setTargets = (patch: Partial<CollectionSpec["targets"]>) =>
    update({ targets: { ...t, ...patch } });
  const setSchedule = (patch: NonNullable<CollectionSpec["targets"]["schedule"]>) => {
    const next = { ...schedule, ...patch };
    const empty =
      !next.days?.length &&
      next.fromHour === undefined &&
      next.toHour === undefined &&
      !next.devices?.length;
    setTargets({ schedule: empty ? null : next });
  };
  const hourOptions = [
    { value: "", label: "Any time" },
    ...Array.from({ length: 24 }, (_, h) => ({ value: String(h), label: formatHour(h) })),
  ];
  const nowhere = !t.collectionsTab && !t.homeRow && !t.sectionRow;
  return (
    <div className="space-y-7">
      <section className="space-y-2" aria-labelledby="where-h">
        <h3 id="where-h" className="text-sm font-semibold text-fg">
          Where it appears
        </h3>
        <ToggleRow
          title="Collections tab"
          description="Listed with the other collections in the matching Plex libraries."
          checked={t.collectionsTab}
          onChange={(v) => setTargets({ collectionsTab: v })}
        />
        <ToggleRow
          title="Home row"
          description="Offered to the personalization engine as a row on Plex Home."
          checked={t.homeRow}
          onChange={(v) => setTargets({ homeRow: v })}
        />
        <ToggleRow
          title="Library row"
          description="Shown as a row at the top of the Movies or TV library."
          checked={t.sectionRow}
          onChange={(v) => setTargets({ sectionRow: v })}
        />
        {nowhere ? (
          <Alert variant="warning">
            <AlertDescription>
              Pick at least one place, or viewers won't see this collection.
            </AlertDescription>
          </Alert>
        ) : null}
      </section>

      <section className="space-y-2" aria-labelledby="who-h">
        <h3 id="who-h" className="text-sm font-semibold text-fg">
          Who sees it
        </h3>
        <Segmented
          ariaLabel="Audience"
          value={t.users === "all" ? "all" : "some"}
          onChange={(v) => setTargets({ users: v === "all" ? "all" : [] })}
          options={[
            { value: "all", label: "Everyone" },
            { value: "some", label: "Specific viewers" },
          ]}
        />
        {t.users !== "all" ? (
          <MultiSelect
            value={t.users.map(String)}
            onChange={(v) => setTargets({ users: v.map(Number) })}
            ariaLabel="Viewers"
            placeholder={viewers ? "Choose viewers…" : "Loading viewers…"}
            options={(viewers ?? []).map((v) => ({
              value: String(v.id),
              label: viewerName(v),
              description: v.isOwner ? "Owner" : v.username,
            }))}
          />
        ) : null}
        {t.users !== "all" && t.users.length === 0 ? (
          <p className="text-xs text-warning">No viewers chosen yet.</p>
        ) : null}
      </section>

      <section className="space-y-3" aria-labelledby="when-h">
        <div>
          <h3 id="when-h" className="text-sm font-semibold text-fg">
            When
          </h3>
          <p className="text-xs text-fg-muted">
            Limit Home and library rows to certain days, hours or devices. The Collections tab
            always lists it.
          </p>
        </div>
        <Field label="Days">
          <div className="space-y-2">
            <ChipGroup
              ariaLabel="Days"
              size="sm"
              value={schedule.days ?? []}
              onChange={(days) => setSchedule({ days: days.length ? [...days].sort() : undefined })}
              options={WEEKDAYS.map((d, i) => ({ value: i, label: d }))}
            />
            <div className="flex gap-1.5">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => setSchedule({ days: [1, 2, 3, 4] })}
              >
                Weeknights
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => setSchedule({ days: [0, 6] })}
              >
                Weekends
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => setSchedule({ days: undefined })}
              >
                Every day
              </Button>
            </div>
          </div>
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="From">
            <SearchSelect
              value={schedule.fromHour === undefined ? "" : String(schedule.fromHour)}
              onChange={(v) => setSchedule({ fromHour: v === "" ? undefined : Number(v) })}
              options={hourOptions}
              ariaLabel="From hour"
            />
          </Field>
          <Field label="Until">
            <SearchSelect
              value={schedule.toHour === undefined ? "" : String(schedule.toHour)}
              onChange={(v) => setSchedule({ toHour: v === "" ? undefined : Number(v) })}
              options={hourOptions}
              ariaLabel="Until hour"
            />
          </Field>
        </div>
        <Field label="Devices">
          <ChipGroup
            ariaLabel="Devices"
            size="sm"
            value={schedule.devices ?? []}
            onChange={(devices) => setSchedule({ devices: devices.length ? devices : undefined })}
            options={DEVICES}
          />
        </Field>
        <div className="flex flex-wrap gap-1.5">
          <Badge variant="secondary">{describeSchedule(spec)}</Badge>
        </div>
      </section>
    </div>
  );
}

export function describeSchedule(spec: CollectionSpec): string {
  const s = spec.targets.schedule;
  if (!s) return "Any day, any time, every device";
  const parts: string[] = [];
  parts.push(s.days?.length ? s.days.map((d) => WEEKDAYS[d]).join(", ") : "Any day");
  if (s.fromHour !== undefined || s.toHour !== undefined)
    parts.push(
      `${s.fromHour !== undefined ? formatHour(s.fromHour) : "midnight"}–${s.toHour !== undefined ? formatHour(s.toHour) : "midnight"}`,
    );
  else parts.push("any time");
  parts.push(
    s.devices?.length
      ? s.devices.map((d) => DEVICES.find((x) => x.value === d)?.label ?? d).join(", ")
      : "every device",
  );
  return parts.join(" · ");
}
