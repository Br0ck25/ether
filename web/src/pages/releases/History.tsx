import {
  Badge,
  Button,
  EmptyState,
  PagedTable,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@braedonsaunders/appkit-ui";
import { CheckCircle2, Pin as PinIcon, RefreshCw, Repeat, Route, XCircle } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import {
  formatBytes,
  formatDateTime,
  formatNanos,
  isPlainObject,
  plural,
  relativeTime,
} from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import { releaseConsole, type HistoryRecord } from "../../lib/releases";
import type { CandidateTrace, ResolverTrace } from "../../lib/types";

function normalizeTrace(t: Record<string, unknown>, i: number): ResolverTrace {
  return {
    titleKey: String(t.titleKey ?? t.key ?? i),
    titleName: String(t.titleName ?? t.title ?? t.name ?? `Trace ${i + 1}`),
    profile: String(t.profile ?? ""),
    at: String(t.at ?? ""),
    candidates: (Array.isArray(t.candidates)
      ? t.candidates
      : Array.isArray(t.releases)
        ? t.releases
        : []
    )
      .filter(isPlainObject)
      .map((c) => ({
        releaseId: String(c.releaseId ?? c.id ?? ""),
        name: String(c.name ?? ""),
        indexer: String(c.indexer ?? ""),
        score: Number(c.score ?? 0),
        rejected: Boolean(c.rejected),
        reason: typeof c.reason === "string" ? c.reason : undefined,
        healthChecked: Boolean(c.healthChecked),
        healthy: Boolean(c.healthy),
        fetchDuration: Number(c.fetchDuration ?? 0),
        healthDuration: Number(c.healthDuration ?? 0),
        size: Number(c.size ?? 0) || undefined,
        pinned: Boolean(c.pinned),
        customFormats: Array.isArray(c.customFormats)
          ? c.customFormats.filter(isPlainObject).map((f) => ({
              id: String(f.id ?? ""),
              name: String(f.name ?? f.id ?? ""),
              score: Number(f.score ?? 0),
            }))
          : undefined,
        compatibilityReason:
          typeof c.compatibilityReason === "string" ? c.compatibilityReason : undefined,
      })),
    winner: typeof t.winner === "string" ? t.winner : undefined,
    err: typeof t.err === "string" ? t.err : typeof t.error === "string" ? t.error : undefined,
    searchDuration: Number(t.searchDuration ?? 0),
    failover: Boolean(t.failover),
    pinned: typeof t.pinned === "string" ? t.pinned : undefined,
    pinStatus: typeof t.pinStatus === "string" ? t.pinStatus : undefined,
    omitted: Number(t.omitted ?? 0) || undefined,
  };
}

/** Accept the resolver's typed traces, and wrap anything else so it still renders. */
export function normalizeTraces(data: unknown): ResolverTrace[] {
  const list = Array.isArray(data)
    ? data
    : isPlainObject(data) && Array.isArray(data.traces)
      ? data.traces
      : [];
  return list.filter(isPlainObject).map(normalizeTrace);
}

function outcome(h: { error?: string; err?: string; releaseId?: string; winner?: string }) {
  if (h.error || h.err)
    return (
      <Badge variant="destructive">
        <XCircle size={11} /> Failed
      </Badge>
    );
  if (h.releaseId || h.winner)
    return (
      <Badge variant="success">
        <CheckCircle2 size={11} /> Picked
      </Badge>
    );
  return <Badge variant="secondary">No pick</Badge>;
}

function flags(h: { failover?: boolean; pinned?: boolean }) {
  return (
    <>
      {h.failover ? (
        <Badge variant="warning">
          <Repeat size={11} /> Failover
        </Badge>
      ) : null}
      {h.pinned ? (
        <Badge variant="info">
          <PinIcon size={11} /> Pinned
        </Badge>
      ) : null}
    </>
  );
}

/** Every release decision Ether has made, newest first; one opens in a drawer. */
export function HistoryTab() {
  const [params, setParams] = useSearchParams();
  const key = params.get("key") ?? "";
  const titleName = params.get("title") ?? "";
  const state = useAsync((signal) => releaseConsole.history({ key, limit: 500 }, signal), [key]);
  const [open, setOpen] = useState<HistoryRecord | null>(null);
  const detail = useAsync(
    () => (open ? releaseConsole.historyEntry(open.id) : Promise.resolve(null)),
    [open?.id],
  );
  const trace = useMemo(
    () =>
      detail.data && detail.data.id === open?.id && isPlainObject(detail.data.trace)
        ? normalizeTrace(detail.data.trace as unknown as Record<string, unknown>, 0)
        : null,
    [detail.data, open?.id],
  );
  const rows = state.data?.items ?? [];

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-2xl text-sm text-fg-muted">
          {key ? (
            <>
              Decisions for <span className="font-medium text-fg">{titleName || key}</span>.{" "}
              <button
                type="button"
                className="text-primary underline"
                onClick={() => setParams({})}
              >
                Show every title
              </button>
            </>
          ) : (
            "Why each release was picked or rejected: profile match, size, custom formats, pins, health checks and failovers."
          )}
        </p>
        <Button variant="outline" size="sm" onClick={() => void state.reload()}>
          <RefreshCw size={14} /> Refresh
        </Button>
      </div>
      {state.error ? (
        <ErrorPanel
          error={state.error}
          feature="Release history"
          onRetry={() => void state.reload()}
        />
      ) : !state.data ? (
        <LoadingRows rows={5} />
      ) : (
        <>
          {state.data.total > rows.length ? (
            <p className="text-xs text-fg-muted">
              Showing the newest {rows.length} of {plural(state.data.total, "decision")}.
            </p>
          ) : null}
          <PagedTable<HistoryRecord>
            rows={rows}
            rowKey={(h) => String(h.id)}
            searchable
            pageSize={25}
            onRowClick={setOpen}
            labels={{
              searchLabel: "Search release history",
              searchPlaceholder: "Filter by title, profile or release…",
            }}
            empty={
              <EmptyState
                icon={<Route />}
                title="No decisions yet"
                description="Play a title in Plex; the decision appears here."
              />
            }
            columns={[
              {
                key: "title",
                header: "Title",
                search: (h) => `${h.titleName} ${h.profile ?? ""} ${h.releaseName ?? ""}`,
                sortValue: (h) => h.titleName,
                cell: (h) => (
                  <span>
                    <span className="block font-medium text-fg">{h.titleName}</span>
                    {h.error ? (
                      <span className="block truncate text-xs text-danger">{h.error}</span>
                    ) : (
                      <span className="block max-w-md truncate font-mono text-xs text-fg-muted">
                        {h.releaseName}
                      </span>
                    )}
                  </span>
                ),
              },
              {
                key: "outcome",
                header: "Outcome",
                cell: (h) => (
                  <span className="flex flex-wrap gap-1">
                    {outcome(h)}
                    {flags(h)}
                  </span>
                ),
              },
              {
                key: "profile",
                header: "Profile",
                sortValue: (h) => h.profile ?? "",
                cell: (h) => (h.profile ? <Badge variant="secondary">{h.profile}</Badge> : "—"),
              },
              {
                key: "candidates",
                header: "Releases",
                align: "right",
                sortValue: (h) => h.candidates,
                cell: (h) => <span className="tabular-nums">{h.candidates}</span>,
              },
              {
                key: "at",
                header: "When",
                sortValue: (h) => h.at,
                cell: (h) => (
                  <span className="text-fg-muted" title={formatDateTime(h.at)}>
                    {relativeTime(h.at)}
                  </span>
                ),
              },
            ]}
            defaultSort={{ key: "at", dir: "desc" }}
          />
        </>
      )}
      <Drawer
        open={open !== null}
        onClose={() => setOpen(null)}
        size="xl"
        title={open?.titleName}
        description={
          open
            ? `${open.profile ? `Profile ${open.profile} · ` : ""}${formatDateTime(open.at)}${trace ? ` · search took ${formatNanos(trace.searchDuration)}` : ""}`
            : undefined
        }
      >
        {open ? (
          detail.error ? (
            <ErrorPanel error={detail.error} onRetry={() => void detail.reload()} />
          ) : detail.data?.id !== open.id ? (
            <LoadingRows rows={4} />
          ) : trace ? (
            <TraceDetail trace={trace} titleKey={open.titleKey} />
          ) : (
            <EmptyState title="No details stored" description="This decision has no trace." />
          )
        ) : null}
      </Drawer>
    </>
  );
}

function verdict(c: CandidateTrace, trace: ResolverTrace) {
  if (c.releaseId && c.releaseId === trace.winner)
    return (
      <Badge variant="success">
        <CheckCircle2 size={11} /> Picked
      </Badge>
    );
  if (c.rejected) return <Badge variant="destructive">Rejected</Badge>;
  return <Badge variant="secondary">Fallback</Badge>;
}

/** Every candidate of one decision and why it won or lost. */
export function TraceDetail({ trace, titleKey }: { trace: ResolverTrace; titleKey?: string }) {
  const cands = [...(trace.candidates ?? [])].sort(
    (a, b) =>
      Number(b.releaseId === trace.winner) - Number(a.releaseId === trace.winner) ||
      Number(a.rejected) - Number(b.rejected) ||
      b.score - a.score,
  );
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        {outcome(trace)}
        {flags({ failover: trace.failover, pinned: trace.pinStatus === "used" })}
        {trace.err ? <span className="text-sm text-danger">{trace.err}</span> : null}
      </div>
      {trace.pinStatus && trace.pinStatus !== "used" ? (
        <p className="text-sm text-fg-muted">
          <PinIcon size={13} className="mr-1 inline" aria-hidden />
          Owner pin: {trace.pinStatus}
        </p>
      ) : null}
      {titleKey ? (
        <p className="text-sm">
          <Link
            className="text-primary underline"
            to={`/releases/search?key=${encodeURIComponent(titleKey)}&title=${encodeURIComponent(trace.titleName)}`}
          >
            Search releases for this title
          </Link>
        </p>
      ) : null}
      {cands.length === 0 ? (
        <EmptyState
          title="No releases found"
          description="The indexers returned nothing usable for this title."
        />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Release</TableHead>
              <TableHead className="text-right">Score</TableHead>
              <TableHead>Health</TableHead>
              <TableHead>Verdict</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {cands.map((c: CandidateTrace, i) => (
              <TableRow key={`${c.releaseId || c.name}-${i}`}>
                <TableCell>
                  <span className="block break-all font-mono text-xs text-fg">{c.name}</span>
                  <span className="text-xs text-fg-muted">
                    {c.indexer}
                    {c.size ? ` · ${formatBytes(c.size)}` : ""}
                    {c.fetchDuration ? ` · fetched in ${formatNanos(c.fetchDuration)}` : ""}
                  </span>
                  {c.pinned || c.customFormats?.length ? (
                    <span className="mt-1 flex flex-wrap gap-1">
                      {c.pinned ? <Badge variant="info">Pinned</Badge> : null}
                      {(c.customFormats ?? []).map((f) => (
                        <Badge key={f.id} variant="outline">
                          {f.name} {f.score > 0 ? `+${f.score}` : f.score}
                        </Badge>
                      ))}
                    </span>
                  ) : null}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {c.rejected && !c.score ? "—" : c.score.toFixed(0)}
                </TableCell>
                <TableCell>
                  {c.healthChecked ? (
                    c.healthy ? (
                      <Badge variant="success">Healthy</Badge>
                    ) : (
                      <Badge variant="destructive">Failed</Badge>
                    )
                  ) : (
                    <span className="text-xs text-fg-subtle">Not checked</span>
                  )}
                  {c.healthDuration ? (
                    <span className="block text-[11px] text-fg-subtle">
                      {formatNanos(c.healthDuration)}
                    </span>
                  ) : null}
                </TableCell>
                <TableCell>
                  {verdict(c, trace)}
                  {c.reason ? (
                    <span className="mt-1 block text-xs text-fg-muted">{c.reason}</span>
                  ) : null}
                  {c.compatibilityReason ? (
                    <span className="mt-1 block text-[11px] text-fg-subtle">
                      {c.compatibilityReason}
                    </span>
                  ) : null}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {trace.omitted ? (
        <p className="text-xs text-fg-muted">
          {plural(trace.omitted, "more rejected release")} not stored.
        </p>
      ) : null}
    </div>
  );
}
