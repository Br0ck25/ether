import { Badge, Button, EmptyState, Skeleton, Spinner, cn } from "@braedonsaunders/appkit-ui";
import { ArrowDownToLine, ArrowUpToLine, EyeOff, Home as HomeIcon, Pin, Sparkle } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ErrorPanel } from "../../components/feedback";
import { Shelf } from "../../components/Poster";
import { DragHandle, SortableList } from "../../components/Sortable";
import { api, isAbort } from "../../lib/api";
import type { HomeRow, LayoutAction, LayoutRule, ViewerHome } from "../../lib/contract";
import { useDebouncedValue } from "../../lib/hooks";
import { ACTION_LABELS, atFor, previewLayout, type PreviewRow } from "../../lib/layout";
import type { ViewContext } from "./ContextBar";

const ACTIONS: { action: LayoutAction; label: string; icon: React.ReactNode; hint: string }[] = [
  { action: "pin", label: "Pin", icon: <Pin size={14} />, hint: "Always show this row, at this position" },
  { action: "boost", label: "Boost", icon: <ArrowUpToLine size={14} />, hint: "Rank this row higher" },
  { action: "bury", label: "Bury", icon: <ArrowDownToLine size={14} />, hint: "Rank this row lower" },
  { action: "hide", label: "Hide", icon: <EyeOff size={14} />, hint: "Never show this row" },
];

export const SOURCE_LABEL: Record<HomeRow["source"], string> = { engine: "Personalized", owner: "Your collection", plex: "Plex" };

/** Fetches a viewer's Home for a context and renders it as Plex rows, with row controls. */
export function HomeCanvas({ ctx, rules, dirty, actionFor, onAction, onReorder, refreshNonce, editable = true, label }: {
  ctx: ViewContext;
  rules: LayoutRule[];
  dirty: boolean;
  actionFor: (hubId: string) => LayoutAction | undefined;
  onAction: (hubId: string, action: LayoutAction | null, position?: number) => void;
  onReorder: (hubId: string, to: number) => void;
  refreshNonce: number;
  editable?: boolean;
  label: string;
}) {
  const key = useDebouncedValue(JSON.stringify(ctx), 250);
  const [home, setHome] = useState<ViewerHome | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const c = JSON.parse(key) as ViewContext;
    if (!c.viewerId) return;
    const ctl = new AbortController();
    setLoading(true);
    api.viewerHome(c.viewerId, { device: c.device, at: atFor(c.day, c.hour).toISOString(), section: c.section === "home" ? undefined : c.section }, ctl.signal)
      .then((h) => { setHome(h); setError(null); })
      .catch((e) => { if (!isAbort(e) && !ctl.signal.aborted) setError(e); })
      .finally(() => { if (!ctl.signal.aborted) setLoading(false); });
    return () => ctl.abort();
  }, [key, refreshNonce]);

  const rows: PreviewRow[] = useMemo(() => {
    if (!home) return [];
    if (!dirty) return home.rows.map((row) => ({ row, hidden: Boolean(row.hidden), overLimit: false, action: row.pinned ? "pin" : row.hidden ? "hide" : undefined }));
    return previewLayout(home.rows, rules, { userId: ctx.viewerId, section: ctx.section, device: ctx.device, day: ctx.day, hour: ctx.hour });
  }, [home, rules, dirty, ctx]);

  const visible = rows.filter((r) => !r.hidden);
  const hidden = rows.filter((r) => r.hidden);

  if (error) return <ErrorPanel error={error} feature="Home preview" />;
  if (!home) {
    return (
      <div className="space-y-6" role="status" aria-label="Loading Home">
        {Array.from({ length: 3 }, (_, i) => (
          <div key={i}><Skeleton className="mb-3 h-5 w-48" /><div className="flex gap-3">{Array.from({ length: 6 }, (_, j) => <Skeleton key={j} className="aspect-[2/3] w-28 shrink-0 rounded-md" />)}</div></div>
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-2" aria-label={label}>
      <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
        <Badge variant="secondary" className="capitalize">{home.context.daypart}</Badge>
        <Badge variant="secondary">{home.context.weekend ? "Weekend" : "Weekday"}</Badge>
        <Badge variant="secondary" className="uppercase">{home.context.device}</Badge>
        <span>{visible.length} rows{hidden.length ? ` · ${hidden.length} hidden` : ""}</span>
        {dirty ? <Badge variant="warning">Previewing unsaved changes</Badge> : null}
        {loading ? <Spinner size={13} label="Refreshing" /> : null}
      </div>
      {visible.length === 0 ? (
        <EmptyState icon={<HomeIcon />} title="No rows for this context" description="Nothing would show on this viewer's Home right now. Unhide a row or loosen a schedule." />
      ) : editable ? (
        <SortableList
          items={visible}
          getId={(r) => r.row.hubId}
          onReorder={(_next, { from, to }) => { const moved = visible[from]; if (moved) onReorder(moved.row.hubId, to); }}
          className={cn("space-y-1 transition-opacity", loading && "opacity-60")}
          renderItem={(r, handle, i) => (
            <RowView row={r} index={i} action={actionFor(r.row.hubId)} onAction={(a) => onAction(r.row.hubId, a, a === "pin" ? i : undefined)}
              handle={<DragHandle handle={handle} label={`Move row ${r.row.title}`} />} />
          )}
        />
      ) : (
        <div className="space-y-1">{visible.map((r, i) => <RowView key={r.row.hubId} row={r} index={i} />)}</div>
      )}
      {hidden.length ? (
        <div className="mt-4 rounded-lg border border-dashed border-border p-3">
          <p className="mb-2 text-xs font-medium text-fg-muted">Hidden in this context</p>
          <div className="flex flex-wrap gap-1.5">
            {hidden.map((r) => (
              <span key={r.row.hubId} className="inline-flex items-center gap-1 rounded-full border border-border bg-surface py-0.5 pl-2.5 pr-1 text-xs text-fg">
                {r.row.title}
                {editable ? <Button variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={() => onAction(r.row.hubId, null)}>Show</Button> : null}
              </span>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function RowView({ row, index, action, onAction, handle }: {
  row: PreviewRow; index: number; action?: LayoutAction; onAction?: (a: LayoutAction | null) => void; handle?: React.ReactNode;
}) {
  const r = row.row;
  const effective = row.action;
  return (
    <section className={cn("group/row rounded-xl px-2 py-3 transition-colors hover:bg-surface/60 focus-within:bg-surface/60", row.overLimit && "opacity-50")}
      aria-label={`Row ${index + 1}: ${r.title}`}>
      <div className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1">
        {handle}
        <h3 className="text-base font-semibold text-fg">{r.title}</h3>
        <Badge variant={r.source === "owner" ? "info" : r.source === "plex" ? "secondary" : "default"}>{SOURCE_LABEL[r.source]}</Badge>
        {effective ? <Badge variant={effective === "hide" ? "destructive" : effective === "bury" ? "warning" : "success"}>{effective === "pin" ? <Pin size={11} /> : <Sparkle size={11} />} {ACTION_LABELS[effective]}{row.actionScope === "viewer" ? " for this viewer" : ""}</Badge> : null}
        {row.overLimit ? <Badge variant="secondary">Over the row limit</Badge> : null}
        <span className="text-xs tabular-nums text-fg-subtle" title="Predicted engagement used for ordering">score {r.score.toFixed(2)}</span>
        {onAction ? (
          <div className="ml-auto flex gap-0.5 opacity-100 transition-opacity sm:opacity-0 sm:group-hover/row:opacity-100 sm:group-focus-within/row:opacity-100" role="group" aria-label={`Layout for ${r.title}`}>
            {ACTIONS.map((a) => (
              <Button key={a.action} type="button" variant={action === a.action ? "subtle" : "ghost"} size="sm" className="h-7 gap-1 px-2 text-xs"
                aria-pressed={action === a.action} aria-label={`${a.label}: ${r.title}`} title={a.hint} onClick={() => onAction(action === a.action ? null : a.action)}>
                {a.icon}<span className="hidden md:inline">{a.label}</span>
              </Button>
            ))}
          </div>
        ) : null}
      </div>
      <p className="mb-2 max-w-3xl text-xs text-fg-muted">{r.reason}</p>
      <Shelf items={r.items} reasons={r.itemReasons} ariaLabel={r.title} empty="This row has no titles right now." />
    </section>
  );
}
