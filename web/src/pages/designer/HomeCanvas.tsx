import { Avatar, Badge, Button, EmptyState, SearchSelect, Skeleton, Spinner, cn } from "@braedonsaunders/appkit-ui";
import { Anchor, EyeOff, Home as HomeIcon, RotateCcw, SlidersHorizontal, Sparkles } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ErrorPanel } from "../../components/feedback";
import { Shelf } from "../../components/Poster";
import { DragHandle, SortableList } from "../../components/Sortable";
import { viewerName } from "../../components/pickers";
import { api, isAbort } from "../../lib/api";
import type { HomeLayout, HomeRow, RowPolicy, Viewer, ViewerHome } from "../../lib/contract";
import { WEEKDAYS, WEEKDAYS_LONG, formatHour } from "../../lib/format";
import { useDebouncedValue } from "../../lib/hooks";
import { CONTINUE, SECTIONS, atFor, policyChips, policyIn, removePolicy, setPolicy, type LayerKey } from "../../lib/layout";
import type { ViewContext } from "./ContextBar";
import { ImpactBar } from "./ImpactBar";

/**
 * The live Plex Home for the selected viewer (or the typical viewer) and
 * context, rendered server-side from the draft layout, with each row's
 * provenance: where it came from and why it sits at its slot.
 */
export function HomeCanvas({ draft, dirty, saved, layer, layerName, ctx, viewers, viewerId, onSpotCheck, refreshNonce, onChange, onEditRow, readOnly }: {
  draft: HomeLayout; dirty: boolean; saved: HomeLayout; layer: LayerKey; layerName: string; ctx: ViewContext; viewers: Viewer[];
  viewerId: number | null; onSpotCheck: (id: number | null) => void; refreshNonce: number;
  onChange: (d: HomeLayout) => void; onEditRow: (row: string) => void;
  /** Show a viewer's saved Home without layout controls (Viewers page). */
  readOnly?: boolean;
}) {
  const request = JSON.stringify({ ctx, viewerId, layout: dirty ? draft : null, refreshNonce });
  const key = useDebouncedValue(request, 300);
  const [home, setHome] = useState<ViewerHome | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const at = useMemo(() => atFor(ctx.day, ctx.hour).toISOString(), [ctx.day, ctx.hour]);

  useEffect(() => {
    const r = JSON.parse(key) as { ctx: ViewContext; viewerId: number | null; layout: HomeLayout | null };
    const ctl = new AbortController();
    setLoading(true);
    const when = { device: r.ctx.device, at: atFor(r.ctx.day, r.ctx.hour).toISOString(), section: r.ctx.section };
    (readOnly && r.viewerId !== null
      ? api.viewerHome(r.viewerId, { ...when, section: r.ctx.section === "home" ? undefined : r.ctx.section }, ctl.signal)
      : api.previewLayout({ viewerId: r.viewerId, ...when, ...(r.layout ? { layout: r.layout } : {}) }, ctl.signal))
      .then((h) => { setHome(h); setError(null); })
      .catch((e) => { if (!isAbort(e) && !ctl.signal.aborted) setError(e); })
      .finally(() => { if (!ctl.signal.aborted) setLoading(false); });
    return () => ctl.abort();
  }, [key, readOnly]);

  const viewer = viewers.find((v) => v.id === viewerId);
  const who = viewer ? viewerName(viewer) : "the typical viewer";
  const visible = (home?.rows ?? []).filter((r) => !r.hidden);
  const hidden = (home?.rows ?? []).filter((r) => r.hidden);
  const arranged = visible.filter((r) => r.slot);
  const fixed = visible.filter((r) => !r.slot);
  const stale = key !== request || loading;

  // The policy key an action edits: the row's own policy when it lives in
  // this layer, otherwise its kind.
  const keyFor = (r: HomeRow) => (r.policy && (r.policyLayer === (layer === "global" ? "global" : "override")) ? r.policy.row : r.kind);
  const baseFor = (r: HomeRow, row: string): RowPolicy => policyIn(draft, layer, ctx.section, row) ?? (r.policy?.row === row ? { ...r.policy } : { row, status: "eligible" });
  const act = (r: HomeRow, patch: Partial<RowPolicy>) => {
    const row = keyFor(r);
    onChange(setPolicy(draft, layer, ctx.section, { ...baseFor(r, row), ...patch, row }));
  };
  const actions = {
    anchor: (r: HomeRow, slot: number) => act(r, { status: "always", slot, minSlot: undefined, maxSlot: undefined }),
    always: (r: HomeRow) => act(r, { status: "always" }),
    eligible: (r: HomeRow) => act(r, { status: "eligible" }),
    off: (r: HomeRow) => act(r, { status: "off", slot: undefined }),
    reset: (r: HomeRow) => onChange(removePolicy(draft, layer, ctx.section, keyFor(r))),
    canReset: (r: HomeRow) => layer !== "global" && Boolean(policyIn(draft, layer, ctx.section, keyFor(r))),
    edit: (r: HomeRow) => onEditRow(keyFor(r)),
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-0 items-center gap-2">
          {viewer ? <Avatar src={viewer.thumb} name={viewerName(viewer)} size={32} /> : <span className="grid size-8 place-items-center rounded-full bg-primary-subtle text-primary"><Sparkles size={15} /></span>}
          <div className="min-w-0">
            <p className="text-xs text-fg-muted">Plex {SECTIONS.find((s) => s.value === ctx.section)?.label} as</p>
            {layer === "global" && !readOnly ? (
              <div className="w-56">
                <SearchSelect value={viewerId === null ? "" : String(viewerId)} onChange={(v) => onSpotCheck(v ? Number(v) : null)} ariaLabel="Preview as"
                  options={[{ value: "", label: "Typical viewer", hint: "The Home a new viewer gets" }, ...viewers.map((v) => ({ value: String(v.id), label: viewerName(v), hint: "Spot-check, with their overrides" }))]} />
              </div>
            ) : <p className="truncate text-base font-semibold text-fg">{who}</p>}
          </div>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
          <Badge variant="secondary">{WEEKDAYS_LONG[ctx.day]} {formatHour(ctx.hour)}</Badge>
          {home ? <Badge variant="secondary" className="capitalize">{home.context.daypart}{home.context.weekend ? " · weekend" : ""}</Badge> : null}
          <Badge variant="secondary" className="uppercase">{ctx.device}</Badge>
          {dirty ? <Badge variant="warning">Previewing unsaved changes</Badge> : null}
          {stale ? <Spinner size={13} label="Refreshing preview" /> : null}
        </div>
      </div>
      {readOnly ? null : <p className="text-xs text-fg-muted">
        Editing <strong className="font-medium text-fg">{layerName}</strong>.
        {layer === "global" ? ` Drag a row to anchor it to that slot for everyone; ${viewer ? `${viewerName(viewer)}'s overrides still apply here` : "the typical viewer has no overrides"}.` : " Drag a row to anchor it to that slot for this viewer only."}
      </p>}

      {layer === "global" && dirty && !readOnly ? <ImpactBar draft={draft} saved={saved} ctx={ctx} at={at} /> : null}

      {error ? <ErrorPanel error={error} feature="Home preview" /> : !home ? (
        <div className="space-y-6" role="status" aria-label="Loading Home">
          {Array.from({ length: 3 }, (_, i) => (
            <div key={i}><Skeleton className="mb-3 h-5 w-48" /><div className="flex gap-3">{Array.from({ length: 6 }, (_, j) => <Skeleton key={j} className="aspect-[2/3] w-28 shrink-0 rounded-md" />)}</div></div>
          ))}
        </div>
      ) : (
        <div className={cn("space-y-1 transition-opacity", stale && "opacity-70")} aria-label={`Plex Home for ${who}`} aria-busy={stale}>
          <p className="text-xs text-fg-muted">{arranged.length} rows on the page{hidden.length ? ` · ${hidden.length} not shown` : ""}</p>
          {fixed.map((r) => <RowCard key={r.hubId} row={r} who={who} />)}
          {arranged.length === 0 ? (
            <EmptyState icon={<HomeIcon />} title="No rows in this context" description="Nothing would show on this Home right now. Turn a row on or loosen a schedule." />
          ) : readOnly ? (
            <div className="space-y-1">{arranged.map((r) => <RowCard key={r.hubId} row={r} who={who} />)}</div>
          ) : (
            <SortableList
              items={arranged}
              getId={(r) => r.hubId}
              onReorder={(_next, { from, to }) => { const moved = arranged[from]; if (moved) actions.anchor(moved, arranged[to]?.slot ?? to + 1); }}
              className="space-y-1"
              renderItem={(r, handle) => (
                <RowCard row={r} who={who} actions={actions} layer={layer}
                  handle={<DragHandle handle={handle} label={`Anchor ${r.title} by dragging`} />} />
              )}
            />
          )}
          {hidden.length ? <HiddenRows rows={hidden} who={who} actions={readOnly ? undefined : actions} /> : null}
        </div>
      )}
    </div>
  );
}

type Actions = {
  anchor: (r: HomeRow, slot: number) => void; always: (r: HomeRow) => void; eligible: (r: HomeRow) => void; off: (r: HomeRow) => void;
  reset: (r: HomeRow) => void; canReset: (r: HomeRow) => boolean; edit: (r: HomeRow) => void;
};

export function provenance(r: HomeRow, who: string): { label: string; variant: "success" | "info" | "default" | "secondary" | "destructive" } {
  if (r.source === "plex" && !r.slot && !r.hidden) return { label: "Plex", variant: "secondary" };
  if (r.layer === "override") return { label: `Override for ${who}`, variant: "info" };
  if (r.layer === "global") {
    if (r.anchorSlot) return { label: "Global anchor", variant: "success" };
    if (r.hidden) return { label: "Global", variant: "secondary" };
    return { label: "Global · always", variant: "success" };
  }
  return { label: "Personalized", variant: "default" };
}

function RowCard({ row: r, who, actions, layer, handle }: { row: HomeRow; who: string; actions?: Actions; layer?: LayerKey; handle?: React.ReactNode }) {
  const prov = provenance(r, who);
  const chips = r.policy ? policyChips(r.policy, WEEKDAYS).filter((c) => !c.startsWith("Slot ")) : [];
  const plexCW = r.kind === CONTINUE;
  const empty = r.items.length === 0;
  return (
    <section className="group/row rounded-xl px-2 py-3 transition-colors hover:bg-surface/60 focus-within:bg-surface/60"
      aria-label={r.slot ? `Slot ${r.slot}: ${r.title}` : `Plex row: ${r.title}`}>
      <div className="mb-1.5 flex flex-wrap items-center gap-x-2 gap-y-1">
        {handle}
        {r.slot ? <span className={cn("grid size-6 shrink-0 place-items-center rounded-md text-xs font-semibold tabular-nums", r.anchorSlot ? "bg-success-subtle text-success" : "bg-bg-subtle text-fg-muted")}>{r.slot}</span> : null}
        <h3 className="text-base font-semibold text-fg">{r.title}</h3>
        <Badge variant={prov.variant}>{r.anchorSlot ? <Anchor size={11} /> : null} {prov.label}</Badge>
        {chips.map((c) => <Badge key={c} variant="outline" className="font-normal">{c}</Badge>)}
        <span className="text-xs tabular-nums text-fg-subtle" title="Predicted engagement used for ordering">score {r.score.toFixed(2)}</span>
        {actions ? (
          <div className="ml-auto flex flex-wrap gap-0.5 opacity-100 transition-opacity sm:opacity-0 sm:group-hover/row:opacity-100 sm:group-focus-within/row:opacity-100" role="group" aria-label={`Layout for ${r.title}`}>
            {r.slot && r.anchorSlot !== r.slot ? (
              <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" aria-label={`Anchor ${r.title} to slot ${r.slot}`} onClick={() => actions.anchor(r, r.slot!)}><Anchor size={13} /><span className="hidden md:inline">Anchor here</span></Button>
            ) : null}
            {<Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" aria-label={`Turn off ${r.title}`} onClick={() => actions.off(r)}><EyeOff size={13} /><span className="hidden md:inline">Off</span></Button>}
            <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" aria-label={`Edit constraints for ${r.title}`} onClick={() => actions.edit(r)}><SlidersHorizontal size={13} /><span className="hidden md:inline">Edit</span></Button>
            {layer !== "global" && actions.canReset(r) ? (
              <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" aria-label={`Reset ${r.title} to global`} onClick={() => actions.reset(r)}><RotateCcw size={13} /><span className="hidden md:inline">Reset to global</span></Button>
            ) : null}
          </div>
        ) : null}
      </div>
      {r.placement ? <p className="mb-0.5 max-w-3xl text-xs text-fg"><span className="font-medium">Why here: </span>{r.placement}</p> : null}
      <p className="mb-2 max-w-3xl text-xs text-fg-muted">{r.reason}</p>
      {plexCW && empty ? (
        <p className="rounded-lg border border-dashed border-border px-3 py-2 text-xs text-fg-muted">Nothing in progress for {who}; Plex hides an empty Continue Watching.</p>
      ) : <Shelf items={r.items} reasons={r.itemReasons} ariaLabel={r.title} empty="This row has no titles right now." />}
    </section>
  );
}

function HiddenRows({ rows, who, actions }: { rows: HomeRow[]; who: string; actions?: Actions }) {
  const [open, setOpen] = useState(true);
  return (
    <div className="mt-4 rounded-xl border border-dashed border-border p-3" role="region" aria-label="Not on this page">
      <button type="button" className="flex w-full items-center justify-between text-left" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="text-sm font-medium text-fg">Not on this page <span className="text-fg-muted">({rows.length})</span></span>
        <span className="text-xs text-fg-muted">{open ? "Hide" : "Show"}</span>
      </button>
      {open ? (
        <ul className="mt-2 divide-y divide-border-subtle">
          {rows.map((r) => {
            const prov = provenance(r, who);
            const off = r.policy?.status === "off" && r.layer !== "personalized";
            return (
              <li key={r.hubId} className="flex flex-wrap items-start gap-2 py-2" aria-label={`Not shown: ${r.title}`}>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-sm font-medium text-fg">{r.title}</span>
                    <Badge variant={prov.variant}>{prov.label}</Badge>
                  </div>
                  <p className="mt-0.5 text-xs text-fg-muted">{r.hiddenReason}</p>
                </div>
                {actions ? <div className="flex gap-1">
                  {off ? <Button variant="outline" size="sm" className="h-7 px-2 text-xs" aria-label={`Make ${r.title} eligible`} onClick={() => actions.eligible(r)}>Eligible</Button> : null}
                  <Button variant="outline" size="sm" className="h-7 px-2 text-xs" aria-label={`Always show ${r.title}`} onClick={() => actions.always(r)}>Always</Button>
                  {actions.canReset(r) ? <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" aria-label={`Reset ${r.title} to global`} onClick={() => actions.reset(r)}><RotateCcw size={12} /> Reset</Button> : null}
                </div> : null}
              </li>
            );
          })}
        </ul>
      ) : null}
    </div>
  );
}
