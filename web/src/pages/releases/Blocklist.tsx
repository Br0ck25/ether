import {
  Badge,
  Button,
  EmptyState,
  Input,
  PagedTable,
  Select,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Ban, RefreshCw, Undo2 } from "lucide-react";
import { useId, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { Field } from "../../components/fields";
import { errorMessage } from "../../lib/api";
import { formatDateTime, plural, relativeTime } from "../../lib/format";
import { useAsync, useDebouncedValue } from "../../lib/hooks";
import { releaseConsole, type BlockEntry } from "../../lib/releases";
import { searchLink } from "./Pinned";

/** Releases Ether will never pick: failed health checks and failovers, plus owner blocks. */
export function BlocklistTab() {
  const id = useId();
  const [params, setParams] = useSearchParams();
  const key = params.get("key") ?? "";
  const titleName = params.get("title") ?? "";
  const [source, setSource] = useState("");
  const [query, setQuery] = useState("");
  const q = useDebouncedValue(query.trim(), 300);
  const state = useAsync(
    (signal) => releaseConsole.blocklist({ key, source, q, limit: 500 }, signal),
    [key, source, q],
  );
  const [busy, setBusy] = useState<string | null>(null);
  const rows = state.data?.items ?? [];

  const unblock = async (e: BlockEntry) => {
    setBusy(e.releaseId);
    try {
      await releaseConsole.unblock(e.releaseId);
      toast.success("Release unblocked", {
        description: "It is health-checked afresh the next time it is a candidate.",
      });
      await state.reload();
    } catch (err) {
      toast.error("Couldn't unblock", { description: errorMessage(err) });
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-wrap items-end gap-3" role="group" aria-label="Filters">
          <Field label="Search" htmlFor={`${id}-q`} className="w-72">
            <Input
              id={`${id}-q`}
              value={query}
              onChange={(e) => setQuery(e.currentTarget.value)}
              placeholder="Title or release name…"
            />
          </Field>
          <Field label="Source" htmlFor={`${id}-source`} className="w-48">
            <Select
              id={`${id}-source`}
              aria-label="Source"
              value={source}
              onChange={(e) => setSource(e.target.value)}
            >
              <option value="">All sources</option>
              <option value="auto">Automatic</option>
              <option value="owner">Blocked by you</option>
            </Select>
          </Field>
        </div>
        <Button variant="outline" size="sm" onClick={() => void state.reload()}>
          <RefreshCw size={14} /> Refresh
        </Button>
      </div>
      {key ? (
        <p className="text-sm text-fg-muted">
          Blocked releases for <span className="font-medium text-fg">{titleName || key}</span>.{" "}
          <button type="button" className="text-primary underline" onClick={() => setParams({})}>
            Show every title
          </button>
        </p>
      ) : null}
      {state.error ? (
        <ErrorPanel error={state.error} onRetry={() => void state.reload()} />
      ) : !state.data ? (
        <LoadingRows rows={4} />
      ) : (
        <>
          {state.data.total > rows.length ? (
            <p className="text-xs text-fg-muted">
              Showing the newest {rows.length} of {plural(state.data.total, "blocked release")};
              narrow the search to find older ones.
            </p>
          ) : null}
          <PagedTable<BlockEntry>
            rows={rows}
            rowKey={(e) => e.releaseId}
            pageSize={25}
            defaultSort={{ key: "at", dir: "desc" }}
            empty={
              <EmptyState
                icon={<Ban />}
                title={q || source || key ? "Nothing matches" : "Nothing is blocked"}
                description="Releases that fail health checks or break during playback land here, as do releases you block from Search."
              />
            }
            columns={[
              {
                key: "title",
                header: "Title",
                sortValue: (e) => e.titleName ?? "",
                cell: (e) =>
                  e.titleName && e.titleKey !== "0" ? (
                    <Link
                      to={searchLink(e.titleKey, e.titleName)}
                      className="font-medium text-fg hover:underline"
                    >
                      {e.titleName}
                    </Link>
                  ) : (
                    <span className="text-fg-muted">Unknown title</span>
                  ),
              },
              {
                key: "release",
                header: "Release",
                sortValue: (e) => e.name ?? e.releaseId,
                cell: (e) => (
                  <span className="block">
                    <span className="block break-all font-mono text-xs text-fg">
                      {e.name || e.releaseId}
                    </span>
                    {e.indexer ? <span className="text-xs text-fg-muted">{e.indexer}</span> : null}
                  </span>
                ),
              },
              {
                key: "reason",
                header: "Reason",
                cell: (e) => <span className="text-sm text-fg-muted">{e.reason || "—"}</span>,
              },
              {
                key: "source",
                header: "Source",
                sortValue: (e) => e.source,
                cell: (e) =>
                  e.manual ? (
                    <Badge variant="info">You</Badge>
                  ) : (
                    <Badge variant="secondary">Automatic</Badge>
                  ),
              },
              {
                key: "at",
                header: "Blocked",
                sortValue: (e) => e.updatedAt,
                cell: (e) => (
                  <span className="text-fg-muted" title={formatDateTime(e.updatedAt)}>
                    {relativeTime(e.updatedAt)}
                  </span>
                ),
              },
              {
                key: "actions",
                header: <span className="sr-only">Actions</span>,
                align: "right",
                cell: (e) => (
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={busy === e.releaseId}
                    aria-label={`Unblock ${e.name || e.releaseId}`}
                    onClick={() => void unblock(e)}
                  >
                    <Undo2 size={14} /> Unblock
                  </Button>
                ),
              },
            ]}
          />
        </>
      )}
    </>
  );
}
