import {
  Avatar,
  Badge,
  EmptyState,
  ListPageLayout,
  PageHeader,
  PagedTable,
  Skeleton,
} from "@braedonsaunders/appkit-ui";
import { Users } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { ErrorPanel } from "../../components/feedback";
import { loadViewers, viewerName } from "../../components/pickers";
import type { Viewer } from "../../lib/contract";
import { formatDateTime, formatNumber, parseTime, relativeTime } from "../../lib/format";
import { useAsync } from "../../lib/hooks";

export default function ViewersList() {
  const navigate = useNavigate();
  const state = useAsync(() => loadViewers(true), []);
  const viewers = state.data ?? [];
  return (
    <ListPageLayout
      header={
        <PageHeader
          title="Viewers"
          description="Everyone with access to this server, what they watch, and what Ether is learning about their taste."
        />
      }
    >
      {state.error ? (
        <ErrorPanel error={state.error} onRetry={() => void state.reload()} />
      ) : !state.data ? (
        <div className="space-y-2" role="status" aria-label="Loading viewers">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-14 w-full rounded-lg" />
          ))}
        </div>
      ) : (
        <PagedTable<Viewer>
          rows={viewers}
          rowKey={(v) => String(v.id)}
          searchable
          pageSize={15}
          onRowClick={(v) => navigate(`/viewers/${v.id}`)}
          defaultSort={{ key: "plays", dir: "desc" }}
          empty={
            <EmptyState
              icon={<Users />}
              title="No viewers yet"
              description="Share this server from plex.tv. People appear here after they sign in to Plex."
            />
          }
          labels={{ searchPlaceholder: "Search viewers…" }}
          columns={[
            {
              key: "name",
              header: "Viewer",
              search: (v) => `${v.title} ${v.username}`,
              sortValue: (v) => viewerName(v).toLowerCase(),
              cell: (v) => (
                <span className="flex items-center gap-3">
                  <Avatar src={v.thumb} name={viewerName(v)} size={34} />
                  <span className="min-w-0">
                    <span className="flex items-center gap-1.5 font-medium text-fg">
                      {viewerName(v)} {v.isOwner ? <Badge variant="default">Owner</Badge> : null}
                    </span>
                    <span className="block text-xs text-fg-muted">{v.username}</span>
                  </span>
                </span>
              ),
            },
            {
              key: "active",
              header: "Last active",
              sortValue: (v) => parseTime(v.lastActive) ?? null,
              cell: (v) => (
                <span title={formatDateTime(v.lastActive)} className="text-fg-muted">
                  {relativeTime(v.lastActive)}
                </span>
              ),
            },
            {
              key: "plays",
              header: "Plays · 30d",
              align: "right",
              sortValue: (v) => v.plays30d,
              cell: (v) => <span className="tabular-nums">{formatNumber(v.plays30d, 0)}</span>,
            },
            {
              key: "hours",
              header: "Hours · 30d",
              align: "right",
              sortValue: (v) => v.hoursWatched30d,
              cell: (v) => (
                <span className="tabular-nums">{formatNumber(v.hoursWatched30d, 1)}</span>
              ),
            },
          ]}
        />
      )}
    </ListPageLayout>
  );
}
