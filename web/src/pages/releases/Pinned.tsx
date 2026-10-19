import {
  Badge,
  Button,
  EmptyState,
  PagedTable,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { HeartPulse, Pin as PinIcon, PinOff, RefreshCw } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { errorMessage } from "../../lib/api";
import { formatBytes, formatDateTime, relativeTime } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import { PIN_STATUS_LABEL, qualityLabel, releaseConsole, type Pin } from "../../lib/releases";

/** Last outcome of a pin: playing, failing, not playable for an app, or not tried yet. */
export function PinStatusBadge({ pin }: { pin: Pin }) {
  const s = pin.lastStatus;
  if (!s) return <Badge variant="secondary">Not tried yet</Badge>;
  const variant =
    s === "healthy" ? "success" : s === "unhealthy" ? "destructive" : ("warning" as const);
  return <Badge variant={variant}>{PIN_STATUS_LABEL[s]}</Badge>;
}

export function searchLink(key: string, title: string) {
  return `/releases/search?key=${encodeURIComponent(key)}&title=${encodeURIComponent(title)}`;
}

/** Releases the owner pinned to titles, newest first. */
export function PinnedTab() {
  const state = useAsync((signal) => releaseConsole.pins(signal), []);
  const [busy, setBusy] = useState<string | null>(null);

  const unpin = async (p: Pin) => {
    const ok = await confirmDialog({
      title: `Unpin ${p.titleName}?`,
      message: "Ether goes back to picking the best release automatically.",
      confirmLabel: "Unpin",
    });
    if (!ok) return;
    setBusy(p.titleKey);
    try {
      await releaseConsole.unpin(p.titleKey);
      toast.success("Pin removed");
      await state.reload();
    } catch (e) {
      toast.error("Couldn't unpin", { description: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  };

  const check = async (p: Pin) => {
    setBusy(p.titleKey);
    try {
      const h = await releaseConsole.checkHealth(p.release.id, p.titleKey);
      if (h.healthy) toast.success("Pinned release is healthy");
      else toast.error("Pinned release failed its health check", { description: h.reason });
      await state.reload();
    } catch (e) {
      toast.error("Health check failed", { description: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-2xl text-sm text-fg-muted">
          A pinned release plays first for its title, ahead of the automatic ranking. When it fails,
          Ether falls back to the next best release and keeps the pin so you can see it.
        </p>
        <Button variant="outline" size="sm" onClick={() => void state.reload()}>
          <RefreshCw size={14} /> Refresh
        </Button>
      </div>
      {state.error ? (
        <ErrorPanel
          error={state.error}
          feature="Pinned releases"
          onRetry={() => void state.reload()}
        />
      ) : !state.data ? (
        <LoadingRows rows={3} />
      ) : (
        <PagedTable<Pin>
          rows={state.data}
          rowKey={(p) => p.titleKey}
          searchable
          pageSize={25}
          labels={{ searchLabel: "Search pins", searchPlaceholder: "Filter by title or release…" }}
          defaultSort={{ key: "pinned", dir: "desc" }}
          empty={
            <EmptyState
              icon={<PinIcon />}
              title="No pinned releases"
              description="Search a title and pin the release you want it to play."
              action={
                <Link to="/releases/search" className="text-sm text-primary underline">
                  Search releases
                </Link>
              }
            />
          }
          columns={[
            {
              key: "title",
              header: "Title",
              search: (p) => `${p.titleName} ${p.release.name} ${p.note ?? ""}`,
              sortValue: (p) => p.titleName,
              cell: (p) => (
                <span className="block">
                  <Link
                    to={searchLink(p.titleKey, p.titleName)}
                    className="font-medium text-fg hover:underline"
                  >
                    {p.titleName || p.titleKey}
                  </Link>
                  {p.note ? <span className="block text-xs text-fg-muted">{p.note}</span> : null}
                </span>
              ),
            },
            {
              key: "release",
              header: "Release",
              cell: (p) => (
                <span className="block">
                  <span className="block break-all font-mono text-xs text-fg">
                    {p.release.name}
                  </span>
                  <span className="text-xs text-fg-muted">
                    {qualityLabel(p.release.info)} · {formatBytes(p.release.size)} ·{" "}
                    {p.release.indexer}
                  </span>
                </span>
              ),
            },
            {
              key: "pinned",
              header: "Pinned",
              sortValue: (p) => p.pinnedAt,
              cell: (p) => (
                <span className="text-fg-muted" title={formatDateTime(p.pinnedAt)}>
                  {relativeTime(p.pinnedAt)}
                </span>
              ),
            },
            {
              key: "status",
              header: "Last status",
              sortValue: (p) => p.lastStatus ?? "",
              cell: (p) => (
                <span className="block">
                  <PinStatusBadge pin={p} />
                  {p.lastReason ? (
                    <span className="mt-1 block text-xs text-fg-muted">{p.lastReason}</span>
                  ) : null}
                  {p.lastStatus ? (
                    <span className="block text-[11px] text-fg-subtle">
                      {relativeTime(p.lastCheckedAt)}
                    </span>
                  ) : null}
                </span>
              ),
            },
            {
              key: "actions",
              header: <span className="sr-only">Actions</span>,
              align: "right",
              cell: (p) => (
                <span className="flex justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={busy === p.titleKey}
                    aria-label={`Check health of the pin for ${p.titleName}`}
                    onClick={() => void check(p)}
                  >
                    <HeartPulse size={14} /> Check
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={busy === p.titleKey}
                    aria-label={`Unpin ${p.titleName}`}
                    onClick={() => void unpin(p)}
                  >
                    <PinOff size={14} /> Unpin
                  </Button>
                </span>
              ),
            },
          ]}
        />
      )}
    </>
  );
}
