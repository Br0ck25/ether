import { Badge, Button, Drawer, EmptyState, PageHeader, Spinner, toast, cn } from "@braedonsaunders/appkit-ui";
import { Columns2, ListOrdered, RefreshCw, Save, Undo2, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ErrorPanel } from "../../components/feedback";
import { Segmented } from "../../components/fields";
import { useViewers, viewerName } from "../../components/pickers";
import { api, errorMessage } from "../../lib/api";
import type { LayoutAction, LayoutRule } from "../../lib/contract";
import { useAsync, useMediaQuery } from "../../lib/hooks";
import { rowActionFor, setRowAction, type LayoutScope } from "../../lib/layout";
import { ContextBar, type ViewContext } from "./ContextBar";
import { HomeCanvas } from "./HomeCanvas";
import { RulesPanel } from "./RulesPanel";

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

function initialContext(viewerId: number): ViewContext {
  const now = new Date();
  return { viewerId, device: "tv", day: now.getDay(), hour: now.getHours(), section: "home" };
}

export default function Designer() {
  const { viewers, error: viewersError } = useViewers();
  const layout = useAsync(() => api.layout(), []);
  const [ctx, setCtx] = useState<ViewContext | null>(null);
  const [ctxB, setCtxB] = useState<ViewContext | null>(null);
  const [compare, setCompare] = useState(false);
  const [rules, setRules] = useState<LayoutRule[]>([]);
  const [scopeMode, setScopeMode] = useState<"all" | "viewer">("all");
  const [saving, setSaving] = useState(false);
  const [nonce, setNonce] = useState(0);
  const [panelOpen, setPanelOpen] = useState(false);
  const wide = useMediaQuery("(min-width: 1280px)");

  useEffect(() => {
    if (viewers?.length && !ctx) {
      const first = viewers.find((v) => v.isOwner) ?? viewers[0]!;
      setCtx(initialContext(first.id));
      const other = viewers.find((v) => v.id !== first.id) ?? first;
      setCtxB({ ...initialContext(other.id) });
    }
  }, [viewers, ctx]);
  useEffect(() => { if (layout.data) setRules(layout.data.rules ?? []); }, [layout.data]);

  const saved = layout.data?.rules ?? [];
  const dirty = Boolean(layout.data) && !same(rules, saved);
  const available = layout.data?.availableRows ?? [];

  const scopeFor = (c: ViewContext): LayoutScope => (scopeMode === "all" ? "all" : c.viewerId);
  const handlers = (c: ViewContext) => ({
    actionFor: (hubId: string) => rowActionFor(rules, scopeFor(c), c.section, hubId)?.action,
    onAction: (hubId: string, action: LayoutAction | null, position?: number) => setRules((r) => setRowAction(r, scopeFor(c), c.section, hubId, action, position)),
    onReorder: (hubId: string, to: number) => setRules((r) => setRowAction(r, scopeFor(c), c.section, hubId, "pin", to)),
  });

  const save = async () => {
    setSaving(true);
    try {
      const doc = await api.putLayout(rules);
      layout.setData(doc);
      setRules(doc.rules ?? []);
      setNonce((n) => n + 1);
      toast.success("Layout saved", { description: "Plex Home updates for these viewers right away." });
    } catch (e) { toast.error("Couldn't save the layout", { description: errorMessage(e) }); }
    finally { setSaving(false); }
  };

  const refreshRows = async () => {
    if (!ctx) return;
    try {
      await Promise.all([api.refreshViewer(ctx.viewerId), compare && ctxB ? api.refreshViewer(ctxB.viewerId) : Promise.resolve(null)]);
      setNonce((n) => n + 1);
      toast.success("Rows rebuilt");
    } catch (e) { toast.error("Couldn't rebuild rows", { description: errorMessage(e) }); }
  };

  const viewerLabel = useMemo(() => {
    const v = viewers?.find((x) => x.id === ctx?.viewerId);
    return v ? viewerName(v) : "this viewer";
  }, [viewers, ctx]);

  const rulesPanel = layout.error ? <ErrorPanel error={layout.error} feature="Home Designer" onRetry={() => void layout.reload()} /> : !layout.data ? <Spinner label="Loading rules" /> : (
    <RulesPanel rules={rules} onChange={setRules} available={available} viewers={viewers ?? []} />
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 border-b border-border bg-surface px-3 py-3 sm:px-6">
        <PageHeader
          title="Home Designer"
          description="See each viewer's Plex Home exactly as Plex receives it, then shape it."
          actions={
            <div className="flex flex-wrap gap-2">
              {dirty ? <Badge variant="warning" className="self-center">Unsaved layout</Badge> : null}
              <Button variant="ghost" size="sm" onClick={() => void refreshRows()} disabled={!ctx}><RefreshCw size={14} /> Rebuild rows</Button>
              {!wide || compare ? <Button variant="outline" size="sm" onClick={() => setPanelOpen(true)}><ListOrdered size={14} /> Rules{rules.length ? ` (${rules.length})` : ""}</Button> : null}
              <Button variant="outline" size="sm" onClick={() => setRules(saved)} disabled={!dirty || saving}><Undo2 size={14} /> Discard</Button>
              <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}><Save size={14} /> {saving ? "Saving…" : "Save layout"}</Button>
            </div>
          }
        />
        {ctx && viewers ? (
          <div className="mt-3 flex flex-wrap items-center gap-3">
            <ContextBar ctx={ctx} onChange={setCtx} viewers={viewers} idPrefix={compare ? "Left" : "Preview"} compact={compare} />
            <div className="ml-auto flex flex-wrap items-center gap-2">
              <span className="text-xs text-fg-muted">Row changes apply to</span>
              <Segmented ariaLabel="Row changes apply to" size="sm" value={scopeMode} onChange={setScopeMode}
                options={[{ value: "all", label: "Everyone" }, { value: "viewer", label: compare ? "Each viewer" : `Only ${viewerLabel}` }]} />
              <Button variant={compare ? "subtle" : "ghost"} size="sm" aria-pressed={compare} onClick={() => setCompare((c) => !c)}><Columns2 size={14} /> Compare</Button>
            </div>
          </div>
        ) : null}
      </div>

      {viewersError ? <div className="p-6"><ErrorPanel error={viewersError} /></div> : !viewers || !ctx ? (
        <div className="grid flex-1 place-items-center"><Spinner size={22} label="Loading viewers" /></div>
      ) : viewers.length === 0 ? (
        <div className="p-6"><EmptyState icon={<Users />} title="No viewers yet" description="Share this server from plex.tv; each person appears here once they sign in." /></div>
      ) : (
        <div className="flex min-h-0 flex-1">
          <div className={cn("app-scroll min-h-0 min-w-0 flex-1 overflow-y-auto bg-bg", compare && "grid divide-border lg:grid-cols-2 lg:divide-x")}>
            <div className="min-w-0 p-3 sm:p-6">
              {compare ? <p className="mb-3 text-xs font-semibold uppercase tracking-wider text-fg-subtle">Left</p> : null}
              <HomeCanvas label="Home preview" ctx={ctx} rules={rules} dirty={dirty} refreshNonce={nonce} {...handlers(ctx)} />
            </div>
            {compare && ctxB ? (
              <div className="min-w-0 border-t border-border p-3 sm:p-6 lg:border-t-0">
                <div className="mb-4 space-y-2">
                  <p className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">Right</p>
                  <ContextBar ctx={ctxB} onChange={setCtxB} viewers={viewers} idPrefix="Right" compact />
                </div>
                <HomeCanvas label="Comparison preview" ctx={ctxB} rules={rules} dirty={dirty} refreshNonce={nonce} {...handlers(ctxB)} />
              </div>
            ) : null}
          </div>
          {wide && !compare ? (
            <aside className="app-scroll w-[26rem] shrink-0 overflow-y-auto border-l border-border bg-bg-subtle p-4" aria-label="Layout rules">
              <h2 className="mb-1 text-sm font-semibold text-fg">Layout rules</h2>
              <p className="mb-4 text-xs text-fg-muted">Server-wide rules apply to everyone; viewer rules override them. Schedules limit a change to certain days, hours or devices.</p>
              {rulesPanel}
            </aside>
          ) : null}
        </div>
      )}

      {(!wide || compare) ? (
        <Drawer open={panelOpen} onClose={() => setPanelOpen(false)} title="Layout rules" description="Server-wide rules apply to everyone; viewer rules override them." size="lg">
          {rulesPanel}
        </Drawer>
      ) : null}
    </div>
  );
}
