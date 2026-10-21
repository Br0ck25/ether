import {
  Badge,
  Button,
  Input,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@braedonsaunders/appkit-ui";
import { ChevronLeft, ChevronRight, Film, Pin, Search } from "lucide-react";
import { useState } from "react";
import { formatBytes } from "../../lib/format";
import type { CacheSnapshot } from "./Cache";

export function CacheExplorer({
  snapshot,
  busy,
  onPin,
}: {
  snapshot?: CacheSnapshot;
  busy: boolean;
  onPin: (id: string, pinned: boolean) => void;
}) {
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(0);
  const rows = (snapshot?.releases ?? []).filter(
    (r) =>
      (filter === "all" || (filter === "protected" ? (r.Pins?.length ?? 0) > 0 : r.Plays > 1)) &&
      `${r.Title} ${r.ReleaseID}`.toLowerCase().includes(search.toLowerCase()),
  );
  const pages = Math.max(1, Math.ceil(rows.length / 20)),
    current = Math.min(page, pages - 1);
  return (
    <section
      aria-label="Cache explorer"
      className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border border-border bg-surface"
    >
      <div className="flex shrink-0 flex-wrap items-center gap-3 border-b border-border p-4">
        <div className="relative min-w-48 flex-1">
          <Search size={14} className="pointer-events-none absolute left-3 top-3 text-fg-subtle" />
          <Input
            aria-label="Search cached releases"
            placeholder="Search titles or release IDs…"
            className="pl-9"
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setPage(0);
            }}
          />
        </div>
        <select
          aria-label="Filter cached releases"
          value={filter}
          onChange={(e) => {
            setFilter(e.target.value);
            setPage(0);
          }}
          className="h-10 rounded-md border border-border bg-surface px-3 text-sm"
        >
          <option value="all">All releases</option>
          <option value="protected">Protected</option>
          <option value="replayed">Repeat watches</option>
        </select>
        <span className="text-xs text-fg-muted">{rows.length} releases</span>
      </div>
      <div className="app-scroll min-h-0 flex-1 overflow-auto">
        <Table className="w-full table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead>Title / release</TableHead>
              <TableHead>Retained</TableHead>
              <TableHead className="hidden md:table-cell">Plays</TableHead>
              <TableHead className="hidden lg:table-cell">Last used</TableHead>
              <TableHead className="w-12">
                <span className="sr-only">Owner pin</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.slice(current * 20, (current + 1) * 20).map((r) => (
              <TableRow key={r.ReleaseID}>
                <TableCell className="max-w-0">
                  <div className="flex items-center gap-3">
                    <div className="hidden shrink-0 rounded-lg bg-primary/10 p-2 text-primary sm:block">
                      <Film size={16} />
                    </div>
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium" title={r.Title || r.ReleaseID}>
                        {r.Title || r.ReleaseID}
                      </p>
                      <p className="mt-0.5 truncate text-[10px] text-fg-subtle" title={r.ReleaseID}>
                        {r.ReleaseID}
                      </p>
                      {!!r.Pins?.length && (
                        <div className="mt-1 flex flex-wrap gap-1">
                          {r.Pins.map((p) => (
                            <Badge key={p} variant="secondary">
                              {p === "owner" ? "Kept by you" : p}
                            </Badge>
                          ))}
                        </div>
                      )}
                    </div>
                  </div>
                </TableCell>
                <TableCell className="w-28 sm:w-40">
                  <p className="text-xs font-medium tabular-nums">{formatBytes(r.Bytes)}</p>
                  {r.Size > 0 && (
                    <>
                      <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-primary/10">
                        <div
                          className="h-full bg-primary/60"
                          style={{ width: `${Math.min(100, (r.Bytes / r.Size) * 100)}%` }}
                        />
                      </div>
                      <p className="mt-1 text-[10px] text-fg-subtle">
                        {Math.min(100, Math.round((r.Bytes / r.Size) * 100))}% retained
                      </p>
                    </>
                  )}
                </TableCell>
                <TableCell className="hidden text-xs tabular-nums md:table-cell">
                  {r.Plays}
                </TableCell>
                <TableCell className="hidden text-xs text-fg-muted lg:table-cell">
                  {r.LastUsed && !r.LastUsed.startsWith("0001")
                    ? new Date(r.LastUsed).toLocaleDateString(undefined, {
                        month: "short",
                        day: "numeric",
                      })
                    : "—"}
                </TableCell>
                <TableCell>
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={`${r.Pins?.includes("owner") ? "Unpin" : "Pin"} ${r.Title || r.ReleaseID}`}
                    disabled={busy}
                    onClick={() => onPin(r.ReleaseID, !r.Pins?.includes("owner"))}
                  >
                    <Pin
                      size={14}
                      className={r.Pins?.includes("owner") ? "fill-primary text-primary" : ""}
                    />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {!rows.length && (
          <div className="px-5 py-16 text-center">
            <Film size={24} className="mx-auto mb-3 text-fg-subtle" />
            <p className="text-sm font-medium">
              {!snapshot
                ? "Loading cache…"
                : search || filter !== "all"
                  ? "No matching releases"
                  : "No retained media yet"}
            </p>
            <p className="mt-2 text-xs text-fg-muted">
              {search || filter !== "all"
                ? "Try a different search or filter."
                : "Cached movies and episodes appear here as viewers watch."}
            </p>
          </div>
        )}
      </div>
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-t border-border px-4 py-2.5">
        <p className="text-[11px] text-fg-muted">
          {rows.length
            ? `${current * 20 + 1}–${Math.min((current + 1) * 20, rows.length)} of ${rows.length}`
            : "0 releases"}{" "}
          · Pins protect data from eviction
        </p>
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label="Previous cache page"
            disabled={current === 0}
            onClick={() => setPage(current - 1)}
          >
            <ChevronLeft size={15} />
          </Button>
          <span className="px-1 text-xs">
            {current + 1} / {pages}
          </span>
          <Button
            variant="ghost"
            size="sm"
            aria-label="Next cache page"
            disabled={current + 1 >= pages}
            onClick={() => setPage(current + 1)}
          >
            <ChevronRight size={15} />
          </Button>
        </div>
      </div>
    </section>
  );
}
