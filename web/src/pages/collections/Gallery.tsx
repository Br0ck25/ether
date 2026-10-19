import {
  Badge, Button, Card, EmptyState, Input, ListPageLayout, PageHeader, Skeleton, SubtabNav, confirmDialog, toast, cn,
} from "@braedonsaunders/appkit-ui";
import {
  Copy, Globe2, EyeOff, LayoutTemplate, Layers, Pencil, Plus, RefreshCw, Search, Sparkles, Trash2, Undo2,
} from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { ActionMenu } from "../../components/ActionMenu";
import { Artwork } from "../../components/Artwork";
import { ErrorPanel } from "../../components/feedback";
import { ChipGroup } from "../../components/fields";
import { api, errorMessage } from "../../lib/api";
import type { CollectionSpec, CollectionTemplate } from "../../lib/contract";
import { relativeTime } from "../../lib/format";
import { clearDraft, draftKey, listDrafts } from "../../lib/history";
import { useAsync } from "../../lib/hooks";
import { describeRule, describeSource, describeTargets, normalizeSpec } from "../../lib/rules";
import { DescribeDialog } from "./DescribeDialog";
import { newCollectionHref } from "./links";

type Tab = "mine" | "templates" | "builtin";

export default function CollectionsGallery() {
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") as Tab) || "mine";
  const setTab = (t: Tab) => { const p = new URLSearchParams(params); p.set("tab", t); setParams(p, { replace: true }); };
  const [q, setQ] = useState("");
  const [describe, setDescribe] = useState(false);
  const navigate = useNavigate();
  const collections = useAsync(() => api.collections(), []);
  const templates = useAsync(() => api.templates(), []);
  const [drafts, setDrafts] = useState(() => listDrafts<CollectionSpec>("collection").filter((d) => d.id.startsWith("new-")));

  const mine = (collections.data ?? []).filter((c) => !c.builtIn);
  const builtIn = (collections.data ?? []).filter((c) => c.builtIn);
  const needle = q.trim().toLowerCase();
  const match = (s: { name: string; description?: string }) => !needle || s.name.toLowerCase().includes(needle) || (s.description ?? "").toLowerCase().includes(needle);

  return (
    <ListPageLayout
      header={
        <>
          <PageHeader
            title="Collections"
            description="Design collections and rows that show up in Plex for the people you choose."
            actions={
              <>
                <Button variant="outline" size="sm" onClick={() => setDescribe(true)}><Sparkles size={15} /> Describe it</Button>
                <Button size="sm" onClick={() => navigate(newCollectionHref())}><Plus size={15} /> New collection</Button>
              </>
            }
          />
          <div className="flex flex-wrap items-end justify-between gap-3">
            <SubtabNav ariaLabel="Collection views" active={tab} onSelect={(k) => setTab(k as Tab)} tabs={[
              { key: "mine", label: "Your collections", count: collections.data ? mine.length : undefined },
              { key: "templates", label: "Templates", count: templates.data?.length },
              { key: "builtin", label: "Built-in", count: collections.data ? builtIn.length : undefined },
            ]} />
            <div className="relative w-full pb-2 sm:w-64">
              <Search size={14} className="pointer-events-none absolute left-3 top-[calc(50%-4px)] -translate-y-1/2 text-fg-subtle" aria-hidden />
              <Input value={q} onChange={(e) => setQ(e.currentTarget.value)} placeholder="Search…" aria-label="Search collections" className="h-9 pl-8" />
            </div>
          </div>
        </>
      }
    >
      {tab === "mine" ? (
        <MineTab state={collections} items={mine.filter(match)} total={mine.length} drafts={drafts.filter((d) => match({ name: d.draft.value.name || "Untitled" }))}
          onDiscardDraft={(id) => { clearDraft(draftKey("collection", id)); setDrafts((d) => d.filter((x) => x.id !== id)); }}
          onOpenTemplates={() => setTab("templates")} />
      ) : tab === "templates" ? (
        <TemplatesTab state={templates} q={needle} />
      ) : (
        <BuiltInTab state={collections} items={builtIn.filter(match)} />
      )}
      <DescribeDialog open={describe} onClose={() => setDescribe(false)} onApply={(spec) => navigate(newCollectionHref(), { state: { spec } })} />
    </ListPageLayout>
  );
}

const GRID = "grid grid-cols-[repeat(auto-fill,minmax(10.5rem,1fr))] gap-5";

function GallerySkeleton() {
  return <div className={GRID} role="status" aria-label="Loading collections">{Array.from({ length: 8 }, (_, i) => <div key={i}><Skeleton className="aspect-[2/3] w-full rounded-lg" /><Skeleton className="mt-2 h-4 w-3/4" /></div>)}</div>;
}

function MineTab({ state, items, total, drafts, onDiscardDraft, onOpenTemplates }: {
  state: ReturnType<typeof useAsync<CollectionSpec[]>>; items: CollectionSpec[]; total: number;
  drafts: { id: string; draft: { value: CollectionSpec; savedAt: string } }[]; onDiscardDraft: (id: string) => void; onOpenTemplates: () => void;
}) {
  const navigate = useNavigate();
  if (state.error) return <ErrorPanel error={state.error} feature="Collection Studio" onRetry={() => void state.reload()} />;
  if (!state.data) return <GallerySkeleton />;

  const act = async (c: CollectionSpec, action: "publish" | "unpublish" | "rebuild" | "delete") => {
    if (!c.id) return;
    try {
      if (action === "delete") {
        const ok = await confirmDialog({ title: "Delete collection?", message: `“${c.name}” disappears from Plex for everyone.`, confirmLabel: "Delete", tone: "danger" });
        if (!ok) return;
        await api.deleteCollection(c.id);
        state.setData((list) => (list ?? []).filter((x) => x.id !== c.id));
        toast.success("Collection deleted");
      } else if (action === "rebuild") {
        const r = await api.rebuildCollection(c.id);
        state.setData((list) => (list ?? []).map((x) => (x.id === c.id ? { ...x, itemCount: r.itemCount, lastBuiltAt: r.lastBuiltAt } : x)));
        toast.success("Rebuilt", { description: `${c.name}: ${r.itemCount} titles.` });
      } else {
        const saved = await api.updateCollection(c.id, { ...normalizeSpec(c), enabled: action === "publish" });
        state.setData((list) => (list ?? []).map((x) => (x.id === c.id ? saved : x)));
        toast.success(action === "publish" ? "Published" : "Unpublished", { description: c.name });
      }
    } catch (e) { toast.error("That didn't work", { description: errorMessage(e) }); }
  };

  return (
    <div className="space-y-8">
      {drafts.length ? (
        <section aria-labelledby="drafts-h">
          <h2 id="drafts-h" className="mb-3 text-sm font-semibold text-fg">Unsaved drafts on this device</h2>
          <div className="flex flex-wrap gap-2">
            {drafts.map((d) => (
              <div key={d.id} className="flex items-center gap-2 rounded-lg border border-dashed border-border-strong bg-surface py-1.5 pl-3 pr-1.5">
                <Link to={`/collections/new?draft=${encodeURIComponent(d.id)}`} className="text-sm font-medium text-fg hover:underline">{d.draft.value.name || "Untitled collection"}</Link>
                <span className="text-xs text-fg-subtle">{relativeTime(d.draft.savedAt)}</span>
                <Button variant="ghost" size="icon" className="size-7" onClick={() => onDiscardDraft(d.id)} aria-label={`Discard draft ${d.draft.value.name || "Untitled"}`}><Undo2 size={13} /></Button>
              </div>
            ))}
          </div>
        </section>
      ) : null}
      {total === 0 ? (
        <EmptyState icon={<Layers />} title="No collections yet"
          description="Start from one of the templates, describe what you want in plain English, or build one from scratch."
          action={<div className="flex flex-wrap justify-center gap-2"><Button onClick={onOpenTemplates}><LayoutTemplate size={15} /> Browse templates</Button><Button variant="outline" onClick={() => navigate(newCollectionHref())}><Plus size={15} /> Start from scratch</Button></div>} />
      ) : items.length === 0 ? (
        <EmptyState icon={<Search />} title="No matches" description="No collection matches your search." />
      ) : (
        <div className={GRID}>
          {items.map((c) => (
            <CollectionCard key={c.id} c={c} onOpen={() => navigate(`/collections/${encodeURIComponent(c.id!)}`)} menu={[
              { key: "edit", label: "Edit", icon: Pencil, onSelect: () => navigate(`/collections/${encodeURIComponent(c.id!)}`) },
              { key: "dup", label: "Duplicate", icon: Copy, onSelect: () => navigate(newCollectionHref(), { state: { spec: { ...c, id: undefined, name: `${c.name} (copy)`, enabled: false } } }) },
              { key: "rebuild", label: "Rebuild now", icon: RefreshCw, onSelect: () => void act(c, "rebuild") },
              c.enabled ? { key: "unpub", label: "Unpublish", icon: EyeOff, onSelect: () => void act(c, "unpublish") } : { key: "pub", label: "Publish", icon: Globe2, onSelect: () => void act(c, "publish") },
              { key: "sep", separator: true },
              { key: "del", label: "Delete", icon: Trash2, danger: true, onSelect: () => void act(c, "delete") },
            ]} />
          ))}
        </div>
      )}
    </div>
  );
}

function CollectionCard({ c, onOpen, menu }: { c: CollectionSpec; onOpen?: () => void; menu?: Parameters<typeof ActionMenu>[0]["items"] }) {
  const targets = describeTargets(c);
  return (
    <article className="group relative">
      <button type="button" onClick={onOpen} className="block w-full rounded-lg text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60" aria-label={`Open ${c.name}`}>
        <div className="relative aspect-[2/3] overflow-hidden rounded-lg shadow-sm ring-1 ring-border-subtle transition duration-200 group-hover:shadow-md motion-safe:group-hover:-translate-y-0.5">
          <Artwork src={c.id ? api.artworkUrl(c.id, 360, c.updatedAt) : undefined} alt={`${c.name} artwork`} title={c.name} className="size-full" />
          <div className="absolute left-2 top-2 flex gap-1">
            {c.builtIn ? <Badge variant="secondary">Built-in</Badge> : c.enabled ? <Badge variant="success">Live</Badge> : <Badge variant="secondary">Draft</Badge>}
          </div>
        </div>
      </button>
      <div className="mt-2 flex items-start gap-1">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium text-fg" title={c.name}>{c.name}</p>
          <p className="truncate text-xs text-fg-muted">
            {typeof c.itemCount === "number" ? `${c.itemCount.toLocaleString()} titles` : "Not built yet"}
            {c.lastBuiltAt ? ` · ${relativeTime(c.lastBuiltAt)}` : ""}
          </p>
          {targets.length ? <p className="truncate text-[11px] text-fg-subtle">{targets.join(" · ")}</p> : null}
        </div>
        {menu ? <ActionMenu items={menu} label={`Actions for ${c.name}`} /> : null}
      </div>
    </article>
  );
}

function TemplatesTab({ state, q }: { state: ReturnType<typeof useAsync<CollectionTemplate[]>>; q: string }) {
  const navigate = useNavigate();
  const [cats, setCats] = useState<string[]>([]);
  const categories = useMemo(() => [...new Set((state.data ?? []).map((t) => t.category))], [state.data]);
  if (state.error) return <ErrorPanel error={state.error} feature="Collection templates" onRetry={() => void state.reload()} />;
  if (!state.data) return <GallerySkeleton />;
  const shown = state.data.filter((t) => (!cats.length || cats.includes(t.category)) && (!q || `${t.name} ${t.description} ${t.category}`.toLowerCase().includes(q)));
  const grouped = categories.map((c) => ({ c, items: shown.filter((t) => t.category === c) })).filter((g) => g.items.length);
  return (
    <div className="space-y-6">
      <ChipGroup ariaLabel="Template categories" size="sm" value={cats} onChange={setCats} options={categories.map((c) => ({ value: c, label: c }))} />
      {grouped.length === 0 ? <EmptyState icon={<Search />} title="No templates match" description="Try another search or category." /> : grouped.map((g) => (
        <section key={g.c} aria-labelledby={`cat-${g.c}`}>
          <h2 id={`cat-${g.c}`} className="mb-3 text-sm font-semibold text-fg">{g.c} <span className="font-normal text-fg-subtle">{g.items.length}</span></h2>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(16rem,1fr))] gap-3">
            {g.items.map((t) => (
              <Card key={t.id} interactive onClick={() => navigate(newCollectionHref({ template: t.id }))} className="flex flex-col p-4" aria-label={`Use template ${t.name}`}>
                <div className="flex items-start justify-between gap-2">
                  <p className="text-sm font-semibold text-fg">{t.name}</p>
                  <Badge variant="secondary" className="shrink-0">{t.spec.mediaType === "show" ? "TV" : t.spec.mediaType === "mixed" ? "Mixed" : "Movies"}</Badge>
                </div>
                <p className="mt-1 text-sm text-fg-muted">{t.description}</p>
                <div className="mt-3 flex flex-wrap gap-1">
                  {t.spec.sources.filter((s) => s.kind !== "catalog").map((s, i) => <span key={`s${i}`} className="rounded bg-info-subtle px-1.5 py-0.5 text-[11px] text-info">{describeSource(s)}</span>)}
                  {t.spec.rules.slice(0, 3).map((r, i) => <span key={`r${i}`} className="rounded bg-bg-subtle px-1.5 py-0.5 text-[11px] text-fg-muted">{describeRule(r)}</span>)}
                  {t.spec.rules.length > 3 ? <span className="px-1 text-[11px] text-fg-subtle">+{t.spec.rules.length - 3} more</span> : null}
                </div>
                <span className={cn("mt-auto pt-3 text-xs font-medium text-primary")}>Use template →</span>
              </Card>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function BuiltInTab({ state, items }: { state: ReturnType<typeof useAsync<CollectionSpec[]>>; items: CollectionSpec[] }) {
  const navigate = useNavigate();
  if (state.error) return <ErrorPanel error={state.error} feature="Collection Studio" onRetry={() => void state.reload()} />;
  if (!state.data) return <GallerySkeleton />;
  if (items.length === 0) return <EmptyState icon={<Layers />} title="No built-in collections" description="Franchise and system collections Ether manages for you appear here." />;
  return (
    <div className={GRID}>
      {items.map((c) => <CollectionCard key={c.id} c={c} onOpen={() => navigate(`/collections/${encodeURIComponent(c.id!)}`)} />)}
    </div>
  );
}
