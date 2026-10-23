import { Badge, Button, SettingsSection, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { Activity, Radio, RefreshCw, Settings2, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { ErrorPanel, MutationError } from "../../components/feedback";
import { Section } from "../../components/layout";
import type { AvailabilityConfig } from "../../lib/types";
import { api, apiFetch } from "../../lib/api";
import { useAsync, useInterval } from "../../lib/hooks";
import { BoolRow, NumRow, RecordDrawer } from "./form";

interface Feed {
  id: string;
  name: string;
  protocol: string;
  supported: boolean;
  reason?: string;
  selected: boolean;
  lastSync: number;
  gap: boolean;
  message: string;
  matched: number;
}
interface Observation {
  key: number;
  title: string;
  release: string;
  protocol: string;
  state: string;
  reason: string;
  firstSeen: number;
  verifiedAt: number;
}
interface Status {
  mode: string;
  message: string;
  feeds: Feed[];
  usage: Record<string, number>;
  counts: Record<string, number>;
  recent: Observation[];
  day: string;
}
const stamp = (n: number) => (n ? new Date(n * 1000).toLocaleString() : "Not yet synced");
const labels: Record<string, string> = {
  observed: "Discovered",
  verified: "Health checked",
  stale: "Needs refresh",
  policy: "Outside quality policy",
  unknown: "Unknown",
  unavailable: "Source unavailable",
};

export function Availability() {
  const status = useAsync((signal) => apiFetch<Status>("/availability/", { signal }), []);
  const config = useAsync(() => api.getConfig(), []);
  const [draft, setDraft] = useState<AvailabilityConfig | null>(null);
  const [saving, setSaving] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saveError, setSaveError] = useState<unknown>(null);
  useInterval(() => void status.reload(), 30000);
  const cfg = config.data?.availability as AvailabilityConfig | undefined;
  const s = status.data;
  if (status.error || config.error)
    return (
      <ErrorPanel
        error={status.error || config.error}
        onRetry={() => {
          void status.reload();
          void config.reload();
        }}
      />
    );
  if (!s || !cfg)
    return (
      <div className="py-12 text-center">
        <Spinner label="Loading availability" />
      </div>
    );
  const change = <K extends keyof AvailabilityConfig>(key: K, value: AvailabilityConfig[K]) =>
    setDraft((d) => d && { ...d, [key]: value });
  const save = async () => {
    if (!draft) return;
    setSaving(true);
    setSaveError(null);
    try {
      await api.putSection("availability", draft);
      await config.reload();
      await status.reload();
      setDraft(null);
      toast.success("Availability settings saved");
    } catch (e) {
      setSaveError(e);
    } finally {
      setSaving(false);
    }
  };
  const sync = async () => {
    setSyncing(true);
    setError(null);
    try {
      await apiFetch("/availability/sync", { method: "POST" });
      toast.success("Feed sync scheduled", {
        description: "The scheduler will pick it up within a minute, within your allowance.",
      });
      await status.reload();
    } catch (e) {
      setError(e);
    } finally {
      setSyncing(false);
    }
  };
  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="max-w-2xl">
          <div className="mb-2 flex items-center gap-2">
            <Radio size={19} className="text-accent" />
            <h2 className="text-lg font-semibold">Availability intelligence</h2>
            <Badge>
              {s.mode === "off"
                ? "Off"
                : s.mode === "checking"
                  ? cfg.monitor
                    ? "Monitoring & checking"
                    : "Checking"
                  : "Monitoring"}
            </Badge>
          </div>
          <p className="text-sm text-fg-muted">{s.message}</p>
          <p className="mt-2 text-xs text-fg-muted">
            Recent movies need release evidence for 60 days; episodes for 24 hours. Suitable
            releases unlock immediately. Older titles need no background scan.
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={!cfg.monitor || syncing}
            onClick={() => void sync()}
          >
            <RefreshCw size={14} />
            {syncing ? "Scheduling…" : "Sync feeds"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setSaveError(null);
              setDraft(structuredClone(cfg));
            }}
          >
            <Settings2 size={14} />
            Settings
          </Button>
        </div>
      </div>
      {error ? <MutationError error={error} /> : null}
      <p className="text-xs text-fg-muted">
        At this interval, {s.feeds.filter((f) => f.supported && f.selected).length} feeds need
        approximately{" "}
        {Math.ceil(1440 / Math.max(5, cfg.pollMinutes)) *
          s.feeds.filter((f) => f.supported && f.selected).length}{" "}
        requests per day for fresh arrivals. Catch-up uses only the remaining allowance and saves
        its progress across restarts.
        {cfg.feedRequests <
        Math.ceil(1440 / Math.max(5, cfg.pollMinutes)) *
          s.feeds.filter((f) => f.supported && f.selected).length
          ? " The current allowance cannot sustain this polling interval for every feed."
          : " Allow additional requests for busy feeds and downtime."}
      </p>
      <div className="grid gap-3 sm:grid-cols-3">
        {[
          [Radio, "Feed requests", s.usage.feedRequests ?? 0, cfg.feedRequests],
          [Activity, "Check API requests", s.usage.checkRequests ?? 0, cfg.checkRequests],
          [ShieldCheck, "Source checks", s.usage.checks ?? 0, cfg.checks],
        ].map(([Icon, title, used, cap]) => {
          const Glyph = Icon as typeof Radio;
          return (
            <div
              key={String(title)}
              className="rounded-xl border border-border bg-surface px-5 py-4"
            >
              <div className="flex items-center gap-2 text-xs text-fg-muted">
                <Glyph size={15} />
                {String(title)}
              </div>
              <p className="mt-3 text-2xl font-semibold tabular-nums">
                {Number(used).toLocaleString()}
                <span className="text-sm font-normal text-fg-muted">
                  {" "}
                  / {Number(cap).toLocaleString()}
                </span>
              </p>
              <progress
                aria-label={String(title)}
                value={Number(used)}
                max={Math.max(1, Number(cap))}
                className="mt-3 h-1 w-full accent-current"
              />
              <p className="mt-2 text-xs text-fg-muted">
                {Number(cap) === 0 ? "Disabled" : `Daily allowance · ${s.day} UTC`}
              </p>
            </div>
          );
        })}
      </div>
      <Section
        title="Release feeds"
        description="One request discovers many releases. Ether matches them against local catalogue metadata without searching every title."
      >
        {s.feeds.length ? (
          <ul className="divide-y divide-border">
            {s.feeds.map((f) => (
              <li key={f.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">
                    {f.name}{" "}
                    <span className="font-normal text-fg-muted">· {f.protocol || "Usenet"}</span>
                  </p>
                  <p className="mt-1 text-xs text-fg-muted">
                    {f.reason || f.message || "Ready to monitor"}
                  </p>
                  <p className="mt-1 text-xs text-fg-muted">{stamp(f.lastSync)}</p>
                </div>
                <Badge>
                  {!f.supported
                    ? "Search only"
                    : !f.selected
                      ? "Excluded"
                      : f.gap
                        ? "Coverage gap"
                        : f.lastSync
                          ? "Synced"
                          : "Waiting"}
                </Badge>
              </li>
            ))}
          </ul>
        ) : (
          <p className="py-4 text-sm text-fg-muted">
            Add a Newznab or Torznab indexer to discover recent releases. A debrid account alone
            does not provide a global release feed.
          </p>
        )}
      </Section>
      <Section
        title="Recent source evidence"
        description="Health checks apply to a specific source and the owner's quality policy. They expire after six hours; they cannot guarantee every byte or every device."
      >
        {s.recent.length ? (
          <ul className="divide-y divide-border">
            {s.recent.map((r, i) => (
              <li key={`${r.key}-${i}`} className="flex flex-wrap items-start gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{r.title}</p>
                  <p className="mt-1 break-all text-xs text-fg-muted">{r.release}</p>
                  <p className="mt-1 text-xs text-fg-muted">
                    {r.reason || "Waiting for a selective check"} ·{" "}
                    {stamp(r.verifiedAt || r.firstSeen)}
                  </p>
                </div>
                <Badge>{labels[r.state] || r.state}</Badge>
              </li>
            ))}
          </ul>
        ) : (
          <div className="py-8 text-center">
            <ShieldCheck className="mx-auto mb-3 text-fg-muted" size={28} />
            <p className="text-sm font-medium">Evidence will appear here</p>
            <p className="mx-auto mt-2 max-w-lg text-sm text-fg-muted">
              Enable release monitoring to follow new arrivals. Successful playback health checks
              also contribute, without extra indexer searches.
            </p>
          </div>
        )}
      </Section>
      <p className="text-xs text-fg-muted">
        Today: {s.usage.grabs ?? 0} / {cfg.grabs} Descriptor grabs ·{" "}
        {Math.ceil((s.usage.bytes ?? 0) / 1048576)} / {Math.ceil(cfg.checkBytes / 1048576)} MiB
        reserved for source validation. Reservations are conservative; actual transfers can be
        smaller.
      </p>
      <RecordDrawer
        open={draft !== null}
        title="Availability settings"
        description="Polling and health checks are independently optional. Recent titles need eligible local release evidence; older titles do not."
        onClose={() => {
          if (!saving) setDraft(null);
        }}
        onSave={() => void save()}
        saving={saving}
        saveLabel="Save settings"
        error={saveError}
      >
        {draft ? (
          <>
            <SettingsSection title="Discovery & presentation">
              <BoolRow
                label="Monitor release feeds"
                value={draft.monitor}
                onChange={(v) => change("monitor", v)}
                help="Use your configured indexers automatically. No separate RSS URL. Requests count toward API budgets; catch-up is bounded."
              />
              <BoolRow
                label="Check discovered sources"
                value={draft.verify}
                onChange={(v) => change("verify", v)}
                help="Spend a small allowance validating promising sources. Checks pause while playback is active."
              />
              <BoolRow
                label="Use debrid for checks"
                value={draft.debrid}
                onChange={(v) => change("debrid", v)}
                help="Use supported cache checks and existing account files. Provider capabilities vary; unknown is never treated as absent."
              />
              <BoolRow
                label="Prefer checked recommendations"
                value={draft.preferVerified}
                onChange={(v) => change("preferVerified", v)}
                help="Move fresh checked movies forward in discovery shelves. Recent-title visibility always uses eligible release evidence."
              />
              <BoolRow
                label="Show newly available shelves"
                value={draft.newlyAvailable}
                onChange={(v) => change("newlyAvailable", v)}
              />
            </SettingsSection>
            <SettingsSection
              title="Participating feeds"
              description="All supported enabled indexers are included by default. Search remains enabled for excluded feeds."
            >
              <BoolRow
                label="Use all supported feeds"
                value={!draft.indexerIds?.length}
                onChange={(v) => change("indexerIds", v ? [] : ["__none__"])}
              />
              {draft.indexerIds?.length
                ? s.feeds
                    .filter((f) => f.supported)
                    .map((f) => (
                      <BoolRow
                        key={f.id}
                        label={f.name}
                        value={draft.indexerIds!.includes(f.id)}
                        onChange={(v) => {
                          const ids = draft.indexerIds!.filter(
                            (id) => id !== f.id && id !== "__none__",
                          );
                          change(
                            "indexerIds",
                            v ? [...ids, f.id] : ids.length ? ids : ["__none__"],
                          );
                        }}
                      />
                    ))
                : null}
            </SettingsSection>
            <SettingsSection
              title="Daily allowances"
              description="Shared across the server, reset at midnight UTC. A zero allowance disables that work. Normal playback retains its existing provider budgets."
            >
              <NumRow
                label="Feed API requests"
                value={draft.feedRequests}
                onChange={(v) => change("feedRequests", v)}
                min={0}
                max={100000}
              />
              <NumRow
                label="Check API requests"
                value={draft.checkRequests}
                onChange={(v) => change("checkRequests", v)}
                min={0}
                max={100000}
                help="Indexer and debrid API calls used by source validation."
              />
              <NumRow
                label="Source checks"
                value={draft.checks}
                onChange={(v) => change("checks", v)}
                min={0}
                max={100000}
              />
              <NumRow
                label="Descriptor grabs"
                value={draft.grabs}
                onChange={(v) => change("grabs", v)}
                min={0}
                max={100000}
              />
              <NumRow
                label="Validation data"
                value={draft.checkBytes / 1048576}
                onChange={(v) => change("checkBytes", v * 1048576)}
                min={0}
                max={102400}
                unit="MiB"
              />
              <NumRow
                label="Reserve for playback"
                value={draft.reservePercent}
                onChange={(v) => change("reservePercent", v)}
                min={0}
                max={99}
                unit="%"
                help="Background requests stop before using this share of each indexer's configured daily cap. Set those caps on the Indexers tab."
              />
              <NumRow
                label="Poll interval"
                value={draft.pollMinutes}
                onChange={(v) => change("pollMinutes", v)}
                min={5}
                max={1440}
                unit="minutes"
              />
              <NumRow
                label="Maximum pages per sync"
                value={draft.maxPages}
                onChange={(v) => change("maxPages", v)}
                min={1}
                max={20}
                help="A page contains up to 100 releases. Unfinished catch-up resumes on later polls, within the daily allowance; fresh arrivals are checked first."
              />
            </SettingsSection>
          </>
        ) : null}
      </RecordDrawer>
    </>
  );
}
