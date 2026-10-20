import {
  Badge,
  Button,
  EmptyState,
  PageHeader,
  SubtabNav,
  Spinner,
  toast,
} from "@braedonsaunders/appkit-ui";
import { RefreshCw, Save, Undo2, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ErrorPanel } from "../../components/feedback";
import { useViewers, viewerName } from "../../components/pickers";
import { api, errorMessage } from "../../lib/api";
import type { HomeLayout } from "../../lib/contract";
import { useAsync, useMediaQuery } from "../../lib/hooks";
import { EMPTY_LAYOUT, prune, sameLayout, type LayerKey } from "../../lib/layout";
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
 * Home screens: a Global Home everyone starts from, personalization
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
  const wide = useMediaQuery("(min-width: 1024px)");

  const saved = doc.data?.layout ?? EMPTY_LAYOUT;
  useEffect(() => {
    if (doc.data) setDraft(prune(doc.data.layout));
  }, [doc.data]);
  const dirty = Boolean(doc.data) && !sameLayout(draft, saved);
  const available = doc.data?.availableRows ?? [];
  const defaults = doc.data?.defaults.maxRows ?? { home: 14, movies: 10, shows: 10 };

  const viewerId = layer === "global" ? spotCheck : layer;
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

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="shrink-0 border-b border-border bg-surface px-3 py-3 sm:px-6">
        <PageHeader
          title="Home screens"
          description="Shape the Global Home everyone starts from. Personalization fills it for each viewer, and per-viewer overrides sit on top."
          actions={wide ? actions : undefined}
        />
        {!wide ? <div className="mt-3">{actions}</div> : null}
      </div>

      {viewersError ? (
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
      ) : (
        <div className="app-scroll min-h-0 flex-1 overflow-y-auto lg:grid lg:grid-cols-3 lg:overflow-hidden">
          <aside
            className="app-scroll space-y-5 border-b border-border bg-bg-subtle p-3 sm:p-5 lg:col-span-1 lg:min-h-0 lg:overflow-y-auto lg:border-b-0 lg:border-r"
            aria-label="Designer controls"
          >
            <LayerSwitcher
              layer={layer}
              onChange={(l) => {
                setLayer(l);
                setFocusRow(null);
              }}
              viewers={viewers}
              draft={draft}
            />
            <section aria-label="Preview context" className="space-y-2">
              <h2 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">
                Context
              </h2>
              <ContextBar ctx={ctx} onChange={setCtx} />
            </section>
            <div className="border-b border-border">
              <SubtabNav
                ariaLabel="Designer tools"
                active={tools}
                onSelect={setTools}
                tabs={[
                  { key: "layout", label: "Layout" },
                  { key: "rows", label: "Rows" },
                  { key: "overrides", label: "Overrides" },
                ]}
              />
            </div>
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
            {tools === "overrides" && (
              <OverrideList
                {...editing}
                onOpenLayer={(l) => {
                  setLayer(l);
                  setFocusRow(null);
                }}
              />
            )}
          </aside>
          <main
            className="app-scroll min-w-0 bg-bg p-3 sm:p-6 lg:col-span-2 lg:min-h-0 lg:overflow-y-auto"
            aria-label="Home preview"
          >
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
          </main>
        </div>
      )}
    </div>
  );
}
