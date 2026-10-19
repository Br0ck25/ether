import {
  Badge,
  Button,
  EmptyState,
  Input,
  PagedTable,
  Select,
  confirmDialog,
  promptDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Ban, HeartPulse, Pin as PinIcon, PinOff, RotateCcw, SearchX, Undo2 } from "lucide-react";
import { useId, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ActionMenu } from "../../components/ActionMenu";
import { ErrorPanel, LoadingInline, LoadingRows } from "../../components/feedback";
import { Field } from "../../components/fields";
import { Section } from "../../components/layout";
import { TitleSearch } from "../../components/pickers";
import { errorMessage } from "../../lib/api";
import type { TitleCard } from "../../lib/contract";
import { formatBytes, formatNanos, relativeTime } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import {
  PROFILE_DOWNLOAD,
  PROFILE_NONE,
  ageDays,
  qualityLabel,
  rangeLabel,
  releaseConsole,
  type ChildTitle,
  type ConsoleRelease,
  type ConsoleSearch,
  type HealthResult,
} from "../../lib/releases";
import { RecordDrawer } from "../sources/form";
import { PinStatusBadge } from "./Pinned";

const pad = (n: number | undefined) => String(n ?? 0).padStart(2, "0");

/** Pick a movie, or a show then a season then an episode. */
function TitlePicker({ onPick }: { onPick: (key: string, name: string) => void }) {
  const id = useId();
  const [show, setShow] = useState<TitleCard | null>(null);
  const [season, setSeason] = useState("");
  const seasons = useAsync(
    (signal) => (show ? releaseConsole.children(show.key, signal) : Promise.resolve([])),
    [show?.key],
  );
  const episodes = useAsync(
    (signal) => (season ? releaseConsole.children(season, signal) : Promise.resolve([])),
    [season],
  );
  const seasonList = (seasons.data ?? []).filter((s) => s.type !== "episode");
  const episodeList = episodes.data ?? [];

  return (
    <div className="space-y-3">
      <TitleSearch
        placeholder="Find a movie or show…"
        onPick={(t) => {
          if (t.type === "show") {
            setShow(t);
            setSeason("");
            return;
          }
          setShow(null);
          onPick(t.key, t.year ? `${t.title} (${t.year})` : t.title);
        }}
      />
      {show ? (
        <div className="flex flex-wrap items-end gap-3" role="group" aria-label="Episode">
          <p className="w-full text-sm font-medium text-fg">{show.title}</p>
          <Field label="Season" htmlFor={`${id}-season`} className="w-48">
            <Select
              id={`${id}-season`}
              aria-label="Season"
              value={season}
              onChange={(e) => setSeason(e.target.value)}
              disabled={!seasons.data}
            >
              <option value="">{seasons.data ? "Choose a season" : "Loading…"}</option>
              {seasonList.map((s: ChildTitle) => (
                <option key={s.key} value={s.key}>
                  {s.title || `Season ${s.season}`}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Episode" htmlFor={`${id}-episode`} className="min-w-64 flex-1">
            <Select
              id={`${id}-episode`}
              aria-label="Episode"
              value=""
              disabled={!season || !episodes.data}
              onChange={(e) => {
                const ep = episodeList.find((x) => x.key === e.target.value);
                if (ep)
                  onPick(
                    ep.key,
                    `${show.title} S${pad(ep.season)}E${pad(ep.episode)}${ep.title ? ` · ${ep.title}` : ""}`,
                  );
              }}
            >
              <option value="">Choose an episode</option>
              {episodeList.map((ep) => (
                <option key={ep.key} value={ep.key}>
                  {`E${pad(ep.episode)} ${ep.title}${ep.pinned ? " (pinned)" : ""}`}
                </option>
              ))}
            </Select>
          </Field>
          {seasons.error ? <ErrorPanel error={seasons.error} className="w-full" /> : null}
          {episodes.error ? <ErrorPanel error={episodes.error} className="w-full" /> : null}
        </div>
      ) : null}
    </div>
  );
}

export function HealthBadge({ health }: { health?: HealthResult | null }) {
  if (!health) return <span className="text-xs text-fg-subtle">Not checked</span>;
  if (health.source === "blocked")
    return (
      <span>
        <Badge variant="destructive">Blocked</Badge>
      </span>
    );
  return (
    <span>
      {health.healthy ? (
        <Badge variant="success">Healthy</Badge>
      ) : health.blocked ? (
        <Badge variant="destructive">Failed</Badge>
      ) : (
        <Badge variant="warning">Check failed</Badge>
      )}
      <span className="mt-1 block text-[11px] text-fg-subtle">
        {health.source === "verified" ? "Verified " : "Checked "}
        {relativeTime(health.checkedAt)}
      </span>
      {health.reason && !health.healthy ? (
        <span className="block text-[11px] text-fg-muted">{health.reason}</span>
      ) : null}
    </span>
  );
}

function verdict(r: ConsoleRelease) {
  if (r.rejected)
    return (
      <span>
        <Badge variant="destructive">Rejected</Badge>
        {r.reason ? <span className="mt-1 block text-xs text-fg-muted">{r.reason}</span> : null}
      </span>
    );
  return (
    <span>
      <Badge variant={r.winner ? "success" : "secondary"}>
        {r.winner ? "Plays first" : `Fallback #${r.rank ?? "?"}`}
      </Badge>
      {r.compatibility ? (
        <span className="mt-1 block text-[11px] text-fg-subtle">{r.compatibility}</span>
      ) : null}
    </span>
  );
}

/** Interactive search: every release for one title, ranked exactly as playback ranks them. */
export function SearchTab() {
  const id = useId();
  const [params, setParams] = useSearchParams();
  const key = params.get("key") ?? "";
  const titleName = params.get("title") ?? "";
  const [profile, setProfile] = useState("");
  const profiles = useAsync(() => releaseConsole.profiles(), []);
  const result = useAsync<ConsoleSearch | null>(
    (signal) => (key ? releaseConsole.search(key, { profile }, signal) : Promise.resolve(null)),
    [key, profile],
  );
  const [busy, setBusy] = useState(false);
  const [pinning, setPinning] = useState<ConsoleRelease | null>(null);
  const [note, setNote] = useState("");
  const [pinError, setPinError] = useState<unknown>(null);
  const [pinSaving, setPinSaving] = useState(false);

  const data = result.data && result.data.titleKey === key ? result.data : null;
  const rows = data?.releases ?? [];
  const name = data?.titleName || titleName || key;

  const act = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    try {
      await fn();
      toast.success(done);
      await result.reload();
    } catch (e) {
      toast.error("That didn't work", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };

  const searchAgain = async () => {
    setBusy(true);
    try {
      await releaseConsole.forget(key);
      result.setData(await releaseConsole.search(key, { profile, fresh: true }));
      toast.success("Searched the indexers again");
    } catch (e) {
      toast.error("Search failed", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };

  const block = async (r: ConsoleRelease) => {
    const reason = await promptDialog({
      title: "Block this release?",
      label: "Reason",
      placeholder: "e.g. wrong language, bad encode",
      confirmLabel: "Block",
    });
    if (reason === null) return;
    await act(
      () =>
        releaseConsole.block({
          releaseId: r.release.id,
          key,
          reason,
          name: r.release.name,
          indexer: r.release.indexer,
        }),
      "Release blocked",
    );
  };

  const unpin = async () => {
    const ok = await confirmDialog({
      title: "Unpin this title?",
      message: "Ether goes back to picking the best release automatically.",
      confirmLabel: "Unpin",
    });
    if (ok) await act(() => releaseConsole.unpin(key), "Pin removed");
  };

  const check = async (r: ConsoleRelease) => {
    setBusy(true);
    try {
      const h = await releaseConsole.checkHealth(r.release.id, key);
      if (h.healthy) toast.success("Release is healthy");
      else
        toast.error(h.blocked ? "Release failed and was blocked" : "Health check failed", {
          description: h.reason,
        });
      await result.reload();
    } catch (e) {
      toast.error("Health check failed", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };

  const savePin = async () => {
    if (!pinning) return;
    setPinSaving(true);
    setPinError(null);
    try {
      await releaseConsole.pin(key, pinning.release.id, note);
      toast.success("Release pinned");
      setPinning(null);
      await result.reload();
    } catch (e) {
      setPinError(e);
    } finally {
      setPinSaving(false);
    }
  };

  const def = profiles.data?.default;

  return (
    <>
      <Section
        title="Find a title"
        description="Search the indexers for one movie or episode and see every release, scored and judged exactly as playback would."
      >
        <TitlePicker onPick={(k, n) => setParams({ key: k, title: n })} />
        <div className="flex flex-wrap items-end gap-3">
          <Field label="Quality profile" htmlFor={`${id}-profile`} className="w-64">
            <Select
              id={`${id}-profile`}
              aria-label="Quality profile"
              value={profile}
              onChange={(e) => setProfile(e.target.value)}
            >
              <option value="">{def ? `Default (${def})` : "Default"}</option>
              {(profiles.data?.profiles ?? []).map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
              <option value={PROFILE_NONE}>No profile limits</option>
              <option value={PROFILE_DOWNLOAD}>Offline download</option>
            </Select>
          </Field>
        </div>
      </Section>

      {!key ? null : (
        <Section
          title={`Releases for ${name}`}
          description={
            data
              ? `${data.cached ? "Cached results" : "Fresh results"} from ${relativeTime(data.searchedAt)} · profile ${data.profile}${data.cached ? "" : ` · took ${formatNanos(data.duration)}`}`
              : undefined
          }
          actions={
            <>
              {busy ? <LoadingInline label="Working…" /> : null}
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => void searchAgain()}
              >
                <RotateCcw size={14} /> Search again
              </Button>
            </>
          }
        >
          {data?.pin ? (
            <div className="flex flex-wrap items-start justify-between gap-3 rounded-lg border border-border bg-bg-subtle px-4 py-3">
              <div className="min-w-0 space-y-1">
                <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-fg">
                  <PinIcon size={14} aria-hidden /> Pinned release
                  <PinStatusBadge pin={data.pin} />
                </p>
                <p className="break-all font-mono text-xs text-fg-muted">{data.pin.release.name}</p>
                {data.pinNote ? <p className="text-xs text-warning">{data.pinNote}</p> : null}
                {data.pin.note ? <p className="text-xs text-fg-muted">{data.pin.note}</p> : null}
              </div>
              <Button variant="outline" size="sm" disabled={busy} onClick={() => void unpin()}>
                <PinOff size={14} /> Unpin
              </Button>
            </div>
          ) : null}
          {result.error ? (
            <ErrorPanel error={result.error} onRetry={() => void result.reload()} />
          ) : !data ? (
            <LoadingRows rows={5} />
          ) : (
            <>
              <p className="text-sm text-fg-muted">
                {data.winner ? (
                  <>
                    Ether would try{" "}
                    <span className="break-all font-mono text-xs text-fg">
                      {rows.find((r) => r.release.id === data.winner)?.release.name}
                    </span>{" "}
                    first, then fall back in rank order if its health check fails.
                  </>
                ) : (
                  "No release is acceptable for this profile."
                )}{" "}
                <Link
                  className="text-primary underline"
                  to={`/releases/history?key=${encodeURIComponent(key)}&title=${encodeURIComponent(name)}`}
                >
                  Past decisions
                </Link>
              </p>
              <PagedTable<ConsoleRelease>
                rows={rows}
                rowKey={(r) => r.release.id}
                searchable
                pageSize={25}
                labels={{
                  searchLabel: "Filter releases",
                  searchPlaceholder: "Filter by name, indexer or reason…",
                }}
                empty={
                  <EmptyState
                    icon={<SearchX />}
                    title="No releases found"
                    description="The indexers returned nothing for this title. Try Search again later."
                  />
                }
                defaultSort={{ key: "rank", dir: "asc" }}
                columns={[
                  {
                    key: "release",
                    header: "Release",
                    search: (r) =>
                      `${r.release.name} ${r.release.indexer} ${r.reason ?? ""} ${(r.customFormats ?? []).map((f) => f.name).join(" ")}`,
                    sortValue: (r) => r.release.name,
                    cell: (r) => {
                      const age = ageDays(r.release.pubDate);
                      return (
                        <span className="block min-w-0">
                          <span className="block break-all font-mono text-xs text-fg">
                            {r.release.name}
                          </span>
                          <span className="text-xs text-fg-muted">
                            {r.release.indexer}
                            {age !== null ? ` · ${age} days old` : ""}
                            {r.release.grabs ? ` · ${r.release.grabs} grabs` : ""}
                          </span>
                          {r.pinned || r.blocked ? (
                            <span className="mt-1 flex flex-wrap gap-1">
                              {r.pinned ? (
                                <Badge variant="info">
                                  <PinIcon size={11} /> Pinned
                                </Badge>
                              ) : null}
                              {r.blocked ? (
                                <Badge variant="destructive" title={r.blockReason}>
                                  <Ban size={11} /> Blocked
                                </Badge>
                              ) : null}
                            </span>
                          ) : null}
                        </span>
                      );
                    },
                  },
                  {
                    key: "quality",
                    header: "Quality",
                    sortValue: (r) => r.release.info?.height ?? 0,
                    cell: (r) => (
                      <span className="block text-xs">
                        <span className="block text-fg">{qualityLabel(r.release.info)}</span>
                        {rangeLabel(r.release.info) || r.release.info?.edition ? (
                          <span className="mt-1 flex flex-wrap gap-1">
                            {rangeLabel(r.release.info) ? (
                              <Badge variant="outline">{rangeLabel(r.release.info)}</Badge>
                            ) : null}
                            {r.release.info?.edition ? (
                              <Badge variant="outline">{r.release.info.edition}</Badge>
                            ) : null}
                          </span>
                        ) : null}
                      </span>
                    ),
                  },
                  {
                    key: "size",
                    header: "Size",
                    align: "right",
                    sortValue: (r) => r.release.size,
                    cell: (r) => (
                      <span className="tabular-nums">{formatBytes(r.release.size)}</span>
                    ),
                  },
                  {
                    key: "score",
                    header: "Score",
                    align: "right",
                    sortValue: (r) => r.score,
                    cell: (r) => (
                      <span className="block">
                        <span className="tabular-nums">{r.score.toFixed(0)}</span>
                        {(r.customFormats ?? []).length ? (
                          <span className="mt-1 flex flex-wrap justify-end gap-1">
                            {(r.customFormats ?? []).map((f) => (
                              <Badge key={f.id} variant="outline">
                                {f.name} {f.score > 0 ? `+${f.score}` : f.score}
                              </Badge>
                            ))}
                          </span>
                        ) : null}
                      </span>
                    ),
                  },
                  {
                    key: "rank",
                    header: "Verdict",
                    sortValue: (r) => (r.rank ? r.rank : 100000 - r.score),
                    cell: verdict,
                  },
                  {
                    key: "health",
                    header: "Health",
                    cell: (r) => <HealthBadge health={r.health} />,
                  },
                  {
                    key: "actions",
                    header: <span className="sr-only">Actions</span>,
                    align: "right",
                    cell: (r) => (
                      <ActionMenu
                        label={`Actions for ${r.release.name}`}
                        items={[
                          r.pinned
                            ? {
                                key: "unpin",
                                label: "Unpin",
                                icon: PinOff,
                                onSelect: () => void unpin(),
                                disabled: busy,
                              }
                            : {
                                key: "pin",
                                label: "Pin to this title",
                                icon: PinIcon,
                                onSelect: () => {
                                  setNote("");
                                  setPinError(null);
                                  setPinning(r);
                                },
                                disabled: busy || r.blocked,
                              },
                          {
                            key: "health",
                            label: "Check health",
                            icon: HeartPulse,
                            onSelect: () => void check(r),
                            disabled: busy,
                          },
                          r.blocked
                            ? {
                                key: "unblock",
                                label: "Unblock",
                                icon: Undo2,
                                onSelect: () =>
                                  void act(
                                    () => releaseConsole.unblock(r.release.id),
                                    "Release unblocked",
                                  ),
                                disabled: busy,
                              }
                            : {
                                key: "block",
                                label: "Block…",
                                icon: Ban,
                                danger: true,
                                onSelect: () => void block(r),
                                disabled: busy,
                              },
                        ]}
                      />
                    ),
                  },
                ]}
              />
            </>
          )}
        </Section>
      )}

      <RecordDrawer
        open={pinning !== null}
        title="Pin release"
        description={name}
        onClose={() => setPinning(null)}
        onSave={() => void savePin()}
        saving={pinSaving}
        saveLabel="Pin release"
        error={pinError}
      >
        {pinning ? (
          <div className="space-y-4">
            <div className="space-y-1 rounded-lg border border-border bg-bg-subtle px-4 py-3">
              <p className="break-all font-mono text-xs text-fg">{pinning.release.name}</p>
              <p className="text-xs text-fg-muted">
                {qualityLabel(pinning.release.info)} · {formatBytes(pinning.release.size)} ·{" "}
                {pinning.release.indexer}
              </p>
            </div>
            <p className="text-sm text-fg-muted">
              Every viewer gets this release first, whatever their quality profile, unless their app
              can't decode it, a viewer picks another version, or it fails its health check. Offline
              downloads keep their own profile.
            </p>
            <Field label="Note" htmlFor={`${id}-note`} hint="Optional: why you pinned it.">
              <Input
                id={`${id}-note`}
                value={note}
                maxLength={500}
                onChange={(e) => setNote(e.currentTarget.value)}
                placeholder="e.g. the only encode with correct colours"
              />
            </Field>
          </div>
        ) : null}
      </RecordDrawer>
    </>
  );
}
