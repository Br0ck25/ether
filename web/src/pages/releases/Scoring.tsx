import { SearchSelect, SettingsSection, Switch, Textarea, toast } from "@braedonsaunders/appkit-ui";
import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useApp } from "../../app-context";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { Section } from "../../components/layout";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { api } from "../../lib/api";
import { formatNanos } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import type { EtherConfig } from "../../lib/types";

type ResolverPolicy = EtherConfig["resolver"] & {
  optimisticStart?: boolean;
  healthRace?: number;
  sourcePreference?: string;
  customFormats?: unknown[];
  customFormatScores?: Record<string, number>;
};

/** The release-selection config section, saved whole with custom formats normalised. */
function useResolver() {
  const { refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const resolver = (config.data?.resolver as ResolverPolicy | undefined) ?? null;
  const save = useCallback(
    async (patch: Partial<ResolverPolicy>, message: string) => {
      if (!resolver) return;
      await api.putSection("resolver", {
        ...resolver,
        customFormats: resolver.customFormats ?? [],
        customFormatScores: resolver.customFormatScores ?? {},
        ...patch,
      });
      toast.success(message);
      await config.reload();
      void refreshDashboard();
    },
    [config, refreshDashboard, resolver],
  );
  return { config, resolver, save };
}

/** Parse the two JSON editors; throws a readable error when either is invalid. */
function parseScoring(formats: string, scores: string) {
  let definitions: unknown;
  let overrides: unknown;
  try {
    definitions = JSON.parse(formats);
  } catch {
    throw Error("Custom format definitions aren't valid JSON.");
  }
  try {
    overrides = JSON.parse(scores);
  } catch {
    throw Error("Score overrides aren't valid JSON.");
  }
  if (!Array.isArray(definitions)) throw Error("Definitions must be a JSON array.");
  if (
    !overrides ||
    Array.isArray(overrides) ||
    typeof overrides !== "object" ||
    Object.values(overrides).some((v) => typeof v !== "number" || !Number.isInteger(v))
  )
    throw Error("Scores must map format IDs to whole numbers.");
  return {
    customFormats: definitions,
    customFormatScores: overrides as Record<string, number>,
  };
}

/** Custom release scoring (TRaSH custom formats), edited in place. */
export function ScoringTab() {
  const { config, resolver, save } = useResolver();
  const saved = useMemo(
    () =>
      resolver
        ? {
            formats: JSON.stringify(resolver.customFormats ?? [], null, 2),
            scores: JSON.stringify(resolver.customFormatScores ?? {}, null, 2),
          }
        : null,
    [resolver],
  );
  const { draft, setDraft, dirty, reset } = useDraft(saved);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!resolver || !draft) return <LoadingRows rows={3} />;

  async function submit() {
    if (!draft) return;
    setSaving(true);
    setError(null);
    try {
      await save(parseScoring(draft.formats, draft.scores), "Release scoring saved");
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }

  const count = resolver.customFormats?.length ?? 0;
  return (
    <>
      <Section
        title="Custom release scoring"
        description="Paste TRaSH custom formats and assign scores. Your quality profiles stay the starting point; matching formats add or subtract from a release's score."
      >
        <p className="text-sm text-fg-muted">
          {count === 0
            ? "No custom formats yet."
            : count === 1
              ? "1 custom format in use."
              : `${count} custom formats in use.`}
        </p>
        <label className="block space-y-2 text-sm font-medium text-fg">
          Custom format definitions
          <Textarea
            className="min-h-64 w-full font-mono text-xs"
            spellCheck={false}
            value={draft.formats}
            onChange={(e) => setDraft({ ...draft, formats: e.target.value })}
          />
        </label>
        <label className="block space-y-2 text-sm font-medium text-fg">
          Custom format score overrides
          <Textarea
            className="min-h-28 w-full font-mono text-xs"
            spellCheck={false}
            value={draft.scores}
            onChange={(e) => setDraft({ ...draft, scores: e.target.value })}
          />
        </label>
        <p className="text-xs text-fg-muted">
          Overrides map a format ID to a whole-number score, e.g.{" "}
          <code>{'{ "x265-hd": -500 }'}</code>.
        </p>
      </Section>
      <p className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
        <Link to="/releases/traces" className="text-primary underline">
          Inspect release scores and rejection reasons
        </Link>
        <Link to="/releases/selection" className="text-primary underline">
          Selection and blocklist settings
        </Link>
      </p>
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={submit}
        onDiscard={() => {
          reset();
          setError(null);
        }}
      />
    </>
  );
}

const RACE = [
  { value: "0", label: "Ranked selection" },
  ...[2, 3, 4].map((n) => ({ value: String(n), label: `First healthy of ${n}` })),
];

const SOURCE_PREFERENCE = [
  { value: "auto", label: "Fastest (automatic)" },
  { value: "usenet", label: "Prefer Usenet" },
  { value: "debrid", label: "Prefer debrid" },
];

/** Measured startup per source, as the resolver sees it. */
function SourceStartupNote() {
  const startup = useAsync(() => api.sourceStartup().catch(() => []), []);
  const rows = startup.data ?? [];
  if (rows.length === 0) return <>No plays measured yet.</>;
  return (
    <>
      Measured so far:{" "}
      {rows
        .map(
          (r) =>
            `${r.protocol === "torrent" ? "debrid" : "Usenet"} ${formatNanos(r.average)} (${r.samples} ${r.samples === 1 ? "play" : "plays"})`,
        )
        .join(", ")}
      .
    </>
  );
}

/** How playback starts: ranked health checks, or racing / optimistic start. */
export function StartupTab() {
  const { config, resolver, save } = useResolver();
  const saved = useMemo(
    () =>
      resolver
        ? {
            optimisticStart: resolver.optimisticStart ?? false,
            healthRace: resolver.healthRace ?? 0,
            sourcePreference: resolver.sourcePreference || "auto",
          }
        : null,
    [resolver],
  );
  const { draft, setDraft, dirty, reset } = useDraft(saved);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!resolver || !draft) return <LoadingRows rows={2} />;

  async function submit() {
    if (!draft) return;
    setSaving(true);
    setError(null);
    try {
      await save(draft, "Playback start saved");
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <SettingsSection
        title="Playback start"
        description="Ranked selection is the default. The other modes trade extra indexer requests for less waiting."
      >
        <SettingsRow
          title="Start before the health check finishes"
          description="Plays the top release straight away. Missing articles still trigger failover, and a failed background check marks the release bad for later requests."
          control={
            <Switch
              checked={draft.optimisticStart}
              onChange={(e) => setDraft({ ...draft, optimisticStart: e.currentTarget.checked })}
              aria-label="Start the top release before its health check completes"
            />
          }
        />
        <SettingsRow
          title="Concurrent checks"
          description="Checking several releases at once can use extra NZB downloads. Detail-page warming has its own small budget."
          control={
            <div className="w-48">
              <SearchSelect
                value={String(draft.healthRace)}
                onChange={(v) => setDraft({ ...draft, healthRace: Number(v) })}
                options={RACE}
                ariaLabel="Concurrent checks"
                searchable={false}
              />
            </div>
          }
        />
        <SettingsRow
          title="Source for equal-quality releases"
          description={
            <>
              Never trades quality for speed. Among releases of the same quality, automatic puts the
              source that starts faster first, and cached debrid first for popular titles until both
              sources have measurements. The other source is checked alongside as a fallback.{" "}
              <SourceStartupNote />
            </>
          }
          control={
            <div className="w-48">
              <SearchSelect
                value={draft.sourcePreference}
                onChange={(v) => setDraft({ ...draft, sourcePreference: v })}
                options={SOURCE_PREFERENCE}
                ariaLabel="Source for equal-quality releases"
                searchable={false}
              />
            </div>
          }
        />
      </SettingsSection>
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={submit}
        onDiscard={() => {
          reset();
          setError(null);
        }}
      />
    </>
  );
}
