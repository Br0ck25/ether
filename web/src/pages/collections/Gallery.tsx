import {
  Button,
  EmptyState,
  Input,
  Skeleton,
  SubtabNav,
  confirmDialog,
  toast,
  cn,
} from "@braedonsaunders/appkit-ui";
import {
  ArrowRight,
  Copy,
  EyeOff,
  Globe2,
  Layers,
  LayoutTemplate,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  Trash2,
  Undo2,
} from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { ActionMenu } from "../../components/ActionMenu";
import { Artwork } from "../../components/Artwork";
import { ErrorPanel } from "../../components/feedback";
import { ToolbarPicker } from "../../components/ToolbarPicker";
import { PanelHead, Workspace } from "../../components/workspace";
import { api, errorMessage } from "../../lib/api";
import type { CollectionSpec } from "../../lib/contract";
import { relativeTime } from "../../lib/format";
import { clearDraft, draftKey, listDrafts } from "../../lib/history";
import { useAsync } from "../../lib/hooks";
import { normalizeSpec } from "../../lib/rules";
import { CollectionDetail } from "./CollectionDetail";
import { DescribeDialog } from "./DescribeDialog";
import { newCollectionHref } from "./links";

type Tab = "mine" | "templates" | "builtin";
type Filter = "all" | "live" | "draft";

export default function CollectionsGallery() {
  const [params, setParams] = useSearchParams();
  const rawTab = params.get("tab");
  const tab: Tab = rawTab === "templates" || rawTab === "builtin" ? rawTab : "mine";
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [category, setCategory] = useState("all");
  const [describe, setDescribe] = useState(false);
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();
  const collections = useAsync(() => api.collections(), []);
  const templates = useAsync(() => api.templates(), []);
  const [drafts, setDrafts] = useState(() =>
    listDrafts<CollectionSpec>("collection").filter((d) => d.id.startsWith("new-")),
  );
  const mine = (collections.data ?? []).filter((c) => !c.builtIn);
  const builtIn = (collections.data ?? []).filter((c) => c.builtIn);
  const needle = q.trim().toLowerCase();
  const match = (s: { name: string; description?: string }) =>
    !needle || `${s.name} ${s.description ?? ""}`.toLowerCase().includes(needle);
  const items = (tab === "builtin" ? builtIn : mine).filter(
    (c) => match(c) && (tab !== "mine" || filter === "all" || c.enabled === (filter === "live")),
  );
  const categories = [...new Set((templates.data ?? []).map((t) => t.category))];
  const shownTemplates = (templates.data ?? []).filter(
    (t) => match(t) && (category === "all" || t.category === category),
  );
  const template =
    tab === "templates"
      ? (shownTemplates.find((t) => t.id === params.get("template")) ?? shownTemplates[0])
      : undefined;
  const selected =
    tab !== "templates"
      ? (items.find((c) => c.id === params.get("collection")) ?? items[0])
      : undefined;
  const state = tab === "templates" ? templates : collections;
  const setTab = (next: Tab) => {
    const p = new URLSearchParams(params);
    p.set("tab", next);
    p.delete("collection");
    p.delete("template");
    setParams(p);
    setQ("");
  };
  const select = (key: "collection" | "template", id: string) => {
    const p = new URLSearchParams(params);
    p.set(key, id);
    setParams(p);
    if (window.matchMedia("(max-width: 1023px)").matches)
      document
        .querySelector('[aria-label="Collection preview"]')
        ?.scrollIntoView({ block: "start", behavior: "smooth" });
  };
  const edit = (c: CollectionSpec) => navigate(`/collections/${encodeURIComponent(c.id!)}`);
  const duplicate = (c: CollectionSpec) =>
    navigate(newCollectionHref(), {
      state: {
        spec: { ...c, id: undefined, builtIn: undefined, name: `${c.name} (copy)`, enabled: false },
      },
    });
  const act = async (c: CollectionSpec, action: "publish" | "unpublish" | "rebuild" | "delete") => {
    if (!c.id || busy) return;
    if (
      action === "delete" &&
      !(await confirmDialog({
        title: "Delete collection?",
        message: `“${c.name}” disappears from Plex for everyone.`,
        confirmLabel: "Delete",
        tone: "danger",
      }))
    )
      return;
    setBusy(true);
    try {
      if (action === "delete") {
        await api.deleteCollection(c.id);
        collections.setData((list) => (list ?? []).filter((x) => x.id !== c.id));
        toast.success("Collection deleted");
      } else if (action === "rebuild") {
        const r = await api.rebuildCollection(c.id);
        collections.setData((list) =>
          (list ?? []).map((x) =>
            x.id === c.id ? { ...x, itemCount: r.itemCount, lastBuiltAt: r.lastBuiltAt } : x,
          ),
        );
        toast.success("Rebuilt", { description: `${c.name}: ${r.itemCount} titles.` });
      } else {
        const saved = await api.updateCollection(c.id, {
          ...normalizeSpec(c),
          enabled: action === "publish",
        });
        collections.setData((list) => (list ?? []).map((x) => (x.id === c.id ? saved : x)));
        toast.success(action === "publish" ? "Published" : "Unpublished", { description: c.name });
      }
    } catch (e) {
      toast.error("That didn't work", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Workspace
        title="Collections"
        subtitle="Curate what shows up in Plex."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => setDescribe(true)}>
              <Sparkles size={14} /> Describe it
            </Button>
            <Button size="sm" onClick={() => navigate(newCollectionHref())}>
              <Plus size={14} /> New collection
            </Button>
          </>
        }
        asideLabel="Collection library"
        mainLabel="Collection preview"
        asideClassName="max-lg:max-h-[45vh]"
        aside={
          <>
            <PanelHead>
              <div className="flex items-center gap-1.5">
                <div className="relative min-w-0 flex-1">
                  <Search
                    size={13}
                    className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
                    aria-hidden
                  />
                  <Input
                    type="search"
                    value={q}
                    onChange={(e) => setQ(e.currentTarget.value)}
                    placeholder={tab === "templates" ? "Find a template…" : "Find a collection…"}
                    aria-label="Search collections"
                    className="h-8 pl-7 text-sm"
                  />
                </div>
                {tab === "mine" ? (
                  <ToolbarPicker
                    ariaLabel="Collection status"
                    value={filter}
                    onChange={setFilter}
                    options={[
                      { value: "all", label: "All statuses" },
                      { value: "live", label: "Published" },
                      { value: "draft", label: "Drafts" },
                    ]}
                  />
                ) : null}
                {tab === "templates" ? (
                  <ToolbarPicker
                    ariaLabel="Template category"
                    value={category}
                    onChange={setCategory}
                    options={[
                      { value: "all", label: "All categories" },
                      ...categories.map((c) => ({ value: c, label: c })),
                    ]}
                  />
                ) : null}
              </div>
              <SubtabNav
                ariaLabel="Collection views"
                active={tab}
                onSelect={(t) => setTab(t as Tab)}
                tabs={[
                  {
                    key: "mine",
                    label: "Yours",
                    count: collections.data ? mine.length : undefined,
                  },
                  { key: "templates", label: "Templates", count: templates.data?.length },
                  {
                    key: "builtin",
                    label: "Built-in",
                    count: collections.data ? builtIn.length : undefined,
                  },
                ]}
              />
            </PanelHead>
            <div className="app-scroll min-h-0 flex-1 space-y-4 overflow-y-auto p-2 sm:p-3">
              {tab === "mine" && drafts.length ? (
                <section
                  aria-label="Unsaved drafts"
                  className="rounded-lg border border-dashed border-border-strong p-2.5"
                >
                  <h2 className="mb-2 text-[11px] font-medium uppercase tracking-wide text-fg-subtle">
                    On this device
                  </h2>
                  {drafts
                    .filter((d) => match({ name: d.draft.value.name || "Untitled" }))
                    .map((d) => (
                      <div key={d.id} className="flex items-center gap-2 py-1">
                        <Link
                          to={`/collections/new?draft=${encodeURIComponent(d.id)}`}
                          className="min-w-0 flex-1 truncate text-xs font-medium text-fg hover:underline"
                        >
                          {d.draft.value.name || "Untitled collection"}
                          <span className="block font-normal text-fg-subtle">
                            {relativeTime(d.draft.savedAt)}
                          </span>
                        </Link>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-7"
                          aria-label={`Discard draft ${d.draft.value.name || "Untitled"}`}
                          onClick={() => {
                            clearDraft(draftKey("collection", d.id));
                            setDrafts((ds) => ds.filter((x) => x.id !== d.id));
                          }}
                        >
                          <Undo2 size={13} />
                        </Button>
                      </div>
                    ))}
                </section>
              ) : null}
              {state.error ? (
                <ErrorPanel error={state.error} onRetry={() => void state.reload()} />
              ) : !state.data ? (
                <div role="status" aria-label="Loading collections" className="space-y-2">
                  {Array.from({ length: 5 }, (_, i) => (
                    <Skeleton key={i} className="h-20 w-full rounded-lg" />
                  ))}
                </div>
              ) : tab === "templates" ? (
                <>
                  <p className="px-2 text-xs text-fg-muted">
                    A starting point for your next collection.
                  </p>
                  <ul className="space-y-1">
                    {shownTemplates.map((t) => (
                      <li key={t.id}>
                        <button
                          type="button"
                          aria-label={`Preview template ${t.name}`}
                          aria-current={template?.id === t.id ? "true" : undefined}
                          onClick={() => select("template", t.id)}
                          className={cn(ROW, template?.id === t.id ? ACTIVE : INACTIVE)}
                        >
                          <span className="grid size-10 shrink-0 place-items-center rounded-lg border border-border bg-surface text-primary">
                            <LayoutTemplate size={18} />
                          </span>
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-sm font-medium">{t.name}</span>
                            <span className="mt-0.5 block text-[11px] text-fg-muted">
                              {t.category} · {mediaLabel(t.spec)}
                            </span>
                          </span>
                          <ArrowRight size={14} className="shrink-0 text-fg-subtle" />
                        </button>
                      </li>
                    ))}
                  </ul>
                  {!shownTemplates.length ? <ListEmpty /> : null}
                </>
              ) : (
                <>
                  <div className="flex items-center justify-between px-2 text-[11px] text-fg-subtle">
                    <span>{tab === "builtin" ? "Managed by Ether" : "Your library"}</span>
                    <span>{items.length} collections</span>
                  </div>
                  <ul className="space-y-1">
                    {items.map((c) => (
                      <li key={c.id}>
                        <button
                          type="button"
                          onClick={() => select("collection", c.id!)}
                          aria-label={`Preview ${c.name}`}
                          aria-current={selected?.id === c.id ? "true" : undefined}
                          className={cn(ROW, selected?.id === c.id ? ACTIVE : INACTIVE)}
                        >
                          <Artwork
                            src={c.id ? api.artworkUrl(c.id, 120, c.updatedAt) : undefined}
                            alt=""
                            title={c.name}
                            className="aspect-[2/3] w-10 shrink-0 rounded shadow-sm ring-1 ring-border"
                          />
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-sm font-medium">{c.name}</span>
                            <span className="mt-1 block text-[11px] text-fg-muted">
                              {c.itemCount === undefined
                                ? "Not built yet"
                                : `${c.itemCount.toLocaleString()} titles`}{" "}
                              · {mediaLabel(c)}
                            </span>
                          </span>
                          <span
                            className={cn(
                              "size-1.5 shrink-0 rounded-full",
                              c.enabled ? "bg-success" : "bg-fg-subtle",
                            )}
                            aria-hidden
                          />
                          <span className="sr-only">{c.enabled ? "Published" : "Draft"}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                  {!items.length ? <ListEmpty /> : null}
                </>
              )}
            </div>
            <div className="mt-auto hidden shrink-0 border-t border-border px-4 py-3 lg:block">
              <Link
                to="/home"
                className="flex items-center justify-between gap-2 text-xs font-medium text-fg-muted hover:text-primary"
              >
                <span>Arrange your Plex Home rows</span>
                <ArrowRight size={13} />
              </Link>
            </div>
          </>
        }
        main={
          state.error ? (
            <div className="p-6">
              <ErrorPanel error={state.error} onRetry={() => void state.reload()} />
            </div>
          ) : !state.data ? (
            <div className="space-y-5 p-6">
              <Skeleton className="h-56 w-full rounded-xl" />
              <Skeleton className="h-60 w-full rounded-xl" />
            </div>
          ) : selected || template ? (
            <CollectionDetail
              key={`${tab}-${template?.id ?? selected?.id}`}
              spec={
                template
                  ? normalizeSpec({
                      ...template.spec,
                      name: template.name,
                      description: template.description,
                    })
                  : selected!
              }
              template={template}
              actions={
                template ? (
                  <Button
                    size="sm"
                    aria-label={`Use template ${template.name}`}
                    onClick={() => navigate(newCollectionHref({ template: template.id }))}
                  >
                    <Plus size={14} /> Use template
                  </Button>
                ) : (
                  <>
                    {selected?.builtIn ? (
                      <Button size="sm" onClick={() => duplicate(selected)}>
                        <Copy size={14} /> Duplicate to edit
                      </Button>
                    ) : (
                      <>
                        <Button size="sm" onClick={() => edit(selected!)}>
                          <Pencil size={14} /> Edit collection
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={busy}
                          onClick={() =>
                            void act(selected!, selected!.enabled ? "unpublish" : "publish")
                          }
                        >
                          {selected!.enabled ? <EyeOff size={14} /> : <Globe2 size={14} />}
                          {selected!.enabled ? "Unpublish" : "Publish"}
                        </Button>
                      </>
                    )}
                    <ActionMenu
                      label="Collection actions"
                      items={[
                        {
                          key: "duplicate",
                          label: "Duplicate",
                          icon: Copy,
                          onSelect: () => duplicate(selected!),
                        },
                        ...(!selected?.builtIn
                          ? [
                              {
                                key: "rebuild",
                                label: "Rebuild now",
                                icon: RefreshCw,
                                disabled: busy,
                                onSelect: () => void act(selected!, "rebuild"),
                              },
                              { key: "sep", separator: true as const },
                              {
                                key: "delete",
                                label: "Delete",
                                icon: Trash2,
                                danger: true,
                                disabled: busy,
                                onSelect: () => void act(selected!, "delete"),
                              },
                            ]
                          : []),
                      ]}
                    />
                  </>
                )
              }
            />
          ) : (
            <EmptyState
              icon={<Layers />}
              title={
                needle || filter !== "all" || category !== "all"
                  ? "No matches"
                  : tab === "mine"
                    ? "Make room for a great collection"
                    : "No collections yet"
              }
              description={
                needle || filter !== "all" || category !== "all"
                  ? "Try another search or filter."
                  : "Start with a template, describe an idea, or build a collection from scratch."
              }
              className="px-6 py-20"
              action={
                <Button variant="outline" onClick={() => setTab("templates")}>
                  <LayoutTemplate size={14} /> Browse templates
                </Button>
              }
            />
          )
        }
      />
      <DescribeDialog
        open={describe}
        onClose={() => setDescribe(false)}
        onApply={(spec) => navigate(newCollectionHref(), { state: { spec } })}
      />
    </>
  );
}

const ROW =
  "flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60";
const ACTIVE = "bg-primary-subtle text-fg ring-1 ring-inset ring-primary/30";
const INACTIVE = "text-fg hover:bg-surface-hover";
function mediaLabel(c: CollectionSpec) {
  return c.mediaType === "show" ? "TV shows" : c.mediaType === "mixed" ? "Movies & TV" : "Movies";
}
function ListEmpty() {
  return <p className="px-3 py-8 text-center text-xs text-fg-muted">No collections to show.</p>;
}
