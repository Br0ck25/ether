import { Badge, Button, EmptyState, SubtabNav, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { RefreshCw, Save, Undo2, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ErrorPanel } from "../../components/feedback";
import { useViewers, viewerName } from "../../components/pickers";
import { api, errorMessage } from "../../lib/api";
import type { HomeLayout } from "../../lib/contract";
import { useAsync } from "../../lib/hooks";
import { Workspace, PanelHead } from "../../components/workspace";
import { EMPTY_LAYOUT, overrideCount, prune, sameLayout, type LayerKey } from "../../lib/layout";
import { ContextBar, type ViewContext } from "./ContextBar";
import { HomeCanvas } from "./HomeCanvas";
import { LayerSwitcher } from "./LayerSwitcher";
import { OverrideList } from "./OverrideList";
import { PageSettings } from "./PageSettings";
import { RowPool } from "./RowPool";
import { AnchoredSlots } from "./AnchoredSlots";

function initialContext(): ViewContext {
  const now = new Date();
  return { device: "tv", day: now.getDay(), hour: now.getHours(), section: "home" };
}

/**
 * HomeScreen: a Global Home everyone starts from, personalization
 * within it, and per-viewer overrides on top. Controls on the left third,
 * the live Plex Home (rendered server-side from the draft) on the right.
 */
export default function Designer() {
  const { viewers, error: viewersError } = useViewers();
  const doc = useAsync(() => api.layout(), []);
  const [draft, setDraft] = useState<HomeLayout>(EMPTY_LAYOUT);
  const [layer, setLayer] = useState<LayerKey>("global");
  const [spotCheck, setSpotCheck] = useState<number | null>(null);
  const [ctx, setCtx] = useState<ViewContext>(initialContext);
  const [saving, setSaving] = useState(false);
  const [nonce, setNonce] = useState(0);
  const [tools, setTools] = useState("layout");
  const [focusRow, setFocusRow] = useState<string | null>(null);

  const saved = doc.data?.layout ?? EMPTY_LAYOUT;
  useEffect(() => {
    if (doc.data) setDraft(prune(doc.data.layout));
  }, [doc.data]);
  const dirty = Boolean(doc.data) && !sameLayout(draft, saved);
  const available = doc.data?.availableRows ?? [];
  const defaults = doc.data?.defaults.maxRows ?? { home: 14, movies: 10, shows: 10 };

  const viewerId = layer === "global" ? spotCheck : layer;
  const overrideTotal = (viewers ?? []).reduce((n, v) => n + overrideCount(draft, v.id), 0);
  const viewerById = useMemo(() => new Map((viewers ?? []).map((v) => [v.id, v])), [viewers]);
  const layerName =
    layer === "global"
      ? "Global Home"
      : viewerById.get(layer)
        ? viewerName(viewerById.get(layer)!)
        : `Viewer ${layer}`;

  const save = async () => {
    setSaving(true);
    try {
      const next = await api.putLayout(draft);
      doc.setData(next);
      setDraft(prune(next.layout));
      setNonce((n) => n + 1);
      toast.success("Home layout saved", {
        description: "Plex Homes follow it on their next load.",
      });
    } catch (e) {
      toast.error("Couldn't save the layout", { description: errorMessage(e) });
    } finally {
      setSaving(false);
    }
  };

  const refreshRows = async () => {
    try {
      if (viewerId !== null) await api.refreshViewer(viewerId);
      setNonce((n) => n + 1);
      toast.success(viewerId !== null ? "Rows rebuilt" : "Preview refreshed");
    } catch (e) {
      toast.error("Couldn't rebuild rows", { description: errorMessage(e) });
    }
  };

  const actions = (
    <div className="flex flex-wrap items-center gap-2">
      {dirty ? <Badge variant="warning">Unsaved changes</Badge> : null}
      <Button variant="ghost" size="sm" onClick={() => void refreshRows()}>
        <RefreshCw size={14} /> {viewerId !== null ? "Rebuild rows" : "Refresh"}
      </Button>
      <Button
        variant="outline"
        size="sm"
        onClick={() => setDraft(prune(saved))}
        disabled={!dirty || saving}
      >
        <Undo2 size={14} /> Discard
      </Button>
      <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}>
        <Save size={14} /> {saving ? "Saving…" : "Save layout"}
      </Button>
    </div>
  );

  const editing = {
    draft,
    onChange: setDraft,
    layer,
    section: ctx.section,
    viewers: viewers ?? [],
  };

  const switchLayer = (l: LayerKey) => {
    setLayer(l);
    setFocusRow(null);
  };

  const state = viewersError ? (
    <div className="p-6">
      <ErrorPanel error={viewersError} />
    </div>
  ) : doc.error ? (
    <div className="p-6">
      <ErrorPanel error={doc.error} onRetry={() => void doc.reload()} />
    </div>
  ) : !viewers || !doc.data ? (
    <div className="grid flex-1 place-items-center">
      <Spinner size={22} label="Loading the Home layout" />
    </div>
  ) : viewers.length === 0 ? (
    <div className="p-6">
      <EmptyState
        icon={<Users />}
        title="No viewers yet"
        description="Share this server from plex.tv; each person appears here once they sign in."
      />
    </div>
  ) : null;

  return (
    <Workspace
      title="HomeScreen"
      subtitle="The Global Home everyone starts from, personalized per viewer, with per-viewer overrides on top."
      actions={actions}
      asideLabel="Designer controls"
      mainLabel="Home preview"
      mainClassName="p-3 sm:p-6"
      aside={
        viewers ? (
          <>
            <PanelHead>
              <div
                className="flex min-w-0 flex-wrap items-center gap-1"
                aria-label="Editing and context"
              >
                <LayerSwitcher
                  layer={layer}
                  onChange={switchLayer}
                  viewers={viewers}
                  draft={draft}
                />
                <ContextBar ctx={ctx} onChange={setCtx} className="shrink-0" />
              </div>
              <SubtabNav
                ariaLabel="Designer tools"
                active={tools}
                onSelect={setTools}
                tabs={[
                  { key: "layout", label: "Layout" },
                  { key: "rows", label: "Rows" },
                  {
                    key: "overrides",
                    label: "Overrides",
                    count: overrideTotal || undefined,
                  },
                ]}
              />
            </PanelHead>
            <div className="space-y-5 p-3 sm:p-4">
              {tools === "layout" && (
                <>
                  <PageSettings {...editing} defaults={defaults} />
                  <AnchoredSlots {...editing} available={available} />
                </>
              )}
              {tools === "rows" && (
                <RowPool
                  {...editing}
                  available={available}
                  focusRow={focusRow}
                  onFocusRow={setFocusRow}
                />
              )}
              {tools === "overrides" && <OverrideList {...editing} onOpenLayer={switchLayer} />}
            </div>
          </>
        ) : null
      }
      main={
        viewers ? (
          <HomeCanvas
            draft={draft}
            dirty={dirty}
            saved={saved}
            layer={layer}
            layerName={layerName}
            ctx={ctx}
            viewers={viewers}
            viewerId={viewerId}
            onSpotCheck={setSpotCheck}
            refreshNonce={nonce}
            onChange={setDraft}
            onEditRow={(row) => {
              setTools("rows");
              setFocusRow(row);
            }}
          />
        ) : null
      }
    >
      {state}
    </Workspace>
  );
}
