import { Drawer } from "../components/Drawer";
import {
  Badge, Button, EmptyState, ListPageLayout, PageHeader, PagedTable, Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@braedonsaunders/appkit-ui";
import { CheckCircle2, RefreshCw, Route, XCircle } from "lucide-react";
import { useMemo, useState } from "react";
import { ErrorPanel, LoadingRows } from "../components/feedback";
import { api, isUnavailable } from "../lib/api";
import { formatDateTime, formatNanos, isPlainObject, relativeTime } from "../lib/format";
import { useAsync } from "../lib/hooks";
import type { CandidateTrace, ResolverTrace } from "../lib/types";

/** Accept the resolver's typed traces, and wrap anything else so it still renders. */
export function normalizeTraces(data: unknown): ResolverTrace[] {
  const list = Array.isArray(data) ? data : isPlainObject(data) && Array.isArray(data.traces) ? data.traces : [];
  return list.filter(isPlainObject).map((t, i) => ({
    titleKey: String(t.titleKey ?? t.key ?? i),
    titleName: String(t.titleName ?? t.title ?? t.name ?? `Trace ${i + 1}`),
    profile: String(t.profile ?? ""),
    at: String(t.at ?? ""),
    candidates: (Array.isArray(t.candidates) ? t.candidates : Array.isArray(t.releases) ? t.releases : []).filter(isPlainObject).map((c) => ({
      releaseId: String(c.releaseId ?? c.id ?? ""), name: String(c.name ?? ""), indexer: String(c.indexer ?? ""), score: Number(c.score ?? 0),
      rejected: Boolean(c.rejected), reason: typeof c.reason === "string" ? c.reason : undefined, healthChecked: Boolean(c.healthChecked),
      healthy: Boolean(c.healthy), fetchDuration: Number(c.fetchDuration ?? 0), healthDuration: Number(c.healthDuration ?? 0),
    })),
    winner: typeof t.winner === "string" ? t.winner : undefined,
    err: typeof t.err === "string" ? t.err : typeof t.error === "string" ? t.error : undefined,
    searchDuration: Number(t.searchDuration ?? 0),
  }));
}

function outcome(t: ResolverTrace) {
  if (t.err) return <Badge variant="destructive"><XCircle size={11} /> Failed</Badge>;
  if (t.winner) return <Badge variant="success"><CheckCircle2 size={11} /> Picked</Badge>;
  return <Badge variant="secondary">No pick</Badge>;
}

export default function Traces() {
  const state = useAsync(() => api.traces(), []);
  const traces = useMemo(() => normalizeTraces(state.data?.traces).sort((a, b) => b.at.localeCompare(a.at)), [state.data]);
  const [open, setOpen] = useState<ResolverTrace | null>(null);

  return (
    <ListPageLayout header={<PageHeader title="Resolver traces" description="Why each release was picked or rejected: profile match, size sanity, health checks and CAM policy."
      actions={<Button variant="outline" size="sm" onClick={() => void state.reload()}><RefreshCw size={14} /> Refresh</Button>} />}>
      {state.error ? (
        isUnavailable(state.error)
          ? <EmptyState icon={<Route />} title="No traces on this server" description="Resolver decisions appear here once the resolver records them." />
          : <ErrorPanel error={state.error} onRetry={() => void state.reload()} />
      ) : !state.data ? <LoadingRows rows={5} /> : (
        <PagedTable<ResolverTrace>
          rows={traces}
          rowKey={(t, i) => `${t.titleKey}-${t.at}-${i}`}
          searchable
          pageSize={20}
          onRowClick={setOpen}
          labels={{ searchPlaceholder: "Filter by title, profile or release…" }}
          empty={<EmptyState icon={<Route />} title="No traces yet" description="Play a title in Plex; the resolver's decision appears here." />}
          columns={[
            { key: "title", header: "Title", search: (t) => `${t.titleName} ${t.profile} ${(t.candidates ?? []).map((c) => c.name).join(" ")}`, sortValue: (t) => t.titleName,
              cell: (t) => <span><span className="block font-medium text-fg">{t.titleName}</span>{t.err ? <span className="block truncate text-xs text-danger">{t.err}</span> : null}</span> },
            { key: "outcome", header: "Outcome", cell: outcome },
            { key: "profile", header: "Profile", sortValue: (t) => t.profile, cell: (t) => t.profile ? <Badge variant="secondary">{t.profile}</Badge> : "—" },
            { key: "candidates", header: "Releases", align: "right", sortValue: (t) => t.candidates?.length ?? 0, cell: (t) => <span className="tabular-nums">{t.candidates?.length ?? 0}</span> },
            { key: "took", header: "Search", align: "right", sortValue: (t) => t.searchDuration, cell: (t) => <span className="tabular-nums">{formatNanos(t.searchDuration)}</span> },
            { key: "at", header: "When", sortValue: (t) => t.at, cell: (t) => <span className="text-fg-muted" title={formatDateTime(t.at)}>{relativeTime(t.at)}</span> },
          ]}
          defaultSort={{ key: "at", dir: "desc" }}
        />
      )}
      <Drawer open={open !== null} onClose={() => setOpen(null)} size="xl" title={open?.titleName} description={open ? `${open.profile ? `Profile ${open.profile} · ` : ""}${formatDateTime(open.at)} · search took ${formatNanos(open.searchDuration)}` : undefined}>
        {open ? <TraceDetail trace={open} /> : null}
      </Drawer>
    </ListPageLayout>
  );
}

function TraceDetail({ trace }: { trace: ResolverTrace }) {
  const cands = [...(trace.candidates ?? [])].sort((a, b) => Number(a.rejected) - Number(b.rejected) || b.score - a.score);
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-2">{outcome(trace)}{trace.err ? <span className="text-sm text-danger">{trace.err}</span> : null}</div>
      {cands.length === 0 ? <EmptyState title="No releases found" description="The indexers returned nothing usable for this title." /> : (
        <Table>
          <TableHeader><TableRow><TableHead>Release</TableHead><TableHead className="text-right">Score</TableHead><TableHead>Health</TableHead><TableHead>Verdict</TableHead></TableRow></TableHeader>
          <TableBody>
            {cands.map((c: CandidateTrace) => (
              <TableRow key={c.releaseId || c.name}>
                <TableCell>
                  <span className="block break-all font-mono text-xs text-fg">{c.name}</span>
                  <span className="text-xs text-fg-muted">{c.indexer}{c.fetchDuration ? ` · fetched in ${formatNanos(c.fetchDuration)}` : ""}</span>
                </TableCell>
                <TableCell className="text-right tabular-nums">{c.score.toFixed(0)}</TableCell>
                <TableCell>{c.healthChecked ? (c.healthy ? <Badge variant="success">Healthy</Badge> : <Badge variant="destructive">Missing articles</Badge>) : <span className="text-xs text-fg-subtle">Not checked</span>}{c.healthDuration ? <span className="block text-[11px] text-fg-subtle">{formatNanos(c.healthDuration)}</span> : null}</TableCell>
                <TableCell>
                  {c.releaseId && c.releaseId === trace.winner ? <Badge variant="success"><CheckCircle2 size={11} /> Picked</Badge> : c.rejected ? <Badge variant="destructive">Rejected</Badge> : <Badge variant="secondary">Fallback</Badge>}
                  {c.reason ? <span className="mt-1 block text-xs text-fg-muted">{c.reason}</span> : null}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
