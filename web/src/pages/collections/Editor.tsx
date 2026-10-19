import {
  Alert, AlertDescription, AlertTitle, Badge, Button, EmptyState, Input, Spinner, SubtabNav, confirmDialog, toast, cn,
} from "@braedonsaunders/appkit-ui";
import {
  ArrowLeft, Copy, Eye, Globe2, History, PencilRuler, Redo2, RefreshCw, Save, SearchX, Sparkles, Trash2, Undo2, EyeOff,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { ActionMenu } from "../../components/ActionMenu";
import { ErrorPanel, PosterSkeletons } from "../../components/feedback";
import { Segmented } from "../../components/fields";
import { PosterGrid } from "../../components/Poster";
import { api, ApiError, errorMessage, isAbort } from "../../lib/api";
import type { CollectionPreview, CollectionSpec } from "../../lib/contract";
import { relativeTime } from "../../lib/format";
import { clearDraft, draftKey, loadDraft, saveDraft, useHistory, type StoredDraft } from "../../lib/history";
import { useAsync, useDebouncedValue, useMediaQuery } from "../../lib/hooks";
import { REFRESH_OPTIONS, blankSpec, describeTargets, normalizeSpec, previewableSpec, specProblems } from "../../lib/rules";
import { BuildPanel } from "./BuildPanel";
import { DescribeDialog } from "./DescribeDialog";
import { newCollectionHref } from "./links";
import { ArtworkPanel, ArtworkPreview, AudiencePanel, SchedulePanel, describeSchedule } from "./SettingsPanels";

type Tab = "build" | "schedule" | "artwork" | "audience";

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

function isTextTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  return el.isContentEditable || el.tagName === "TEXTAREA" || (el.tagName === "INPUT" && !["checkbox", "radio", "range", "color", "button"].includes((el as HTMLInputElement).type));
}

export default function CollectionEditor() {
  const { id } = useParams();
  const [search, setSearch] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const templateId = search.get("template");
  const incoming = (location.state as { spec?: CollectionSpec } | null)?.spec;
  const [newDraftId] = useState(() => search.get("draft") ?? `new-${Date.now().toString(36)}`);

  // Keep the draft id in the URL so a reload of a new collection restores it.
  useEffect(() => {
    if (!id && !search.get("draft")) {
      const next = new URLSearchParams(search);
      next.set("draft", newDraftId);
      setSearch(next, { replace: true, state: location.state });
    }
  }, [id, search, setSearch, newDraftId, location.state]);

  const source = useAsync(async () => {
    if (id) return normalizeSpec(await api.collection(id));
    if (incoming) return normalizeSpec({ ...incoming, id: undefined, enabled: incoming.enabled ?? false });
    if (templateId) {
      const t = (await api.templates()).find((x) => x.id === templateId);
      if (!t) throw new ApiError(404, "not_found", "That template no longer exists.");
      return normalizeSpec({ ...t.spec, id: undefined, enabled: false, templateId: t.id });
    }
    return blankSpec();
  }, [id, templateId]);

  const config = useAsync(() => api.getConfig(), []);
  const region = config.data?.catalog.region || "US";

  const history = useHistory<CollectionSpec>(blankSpec());
  const spec = history.value;
  const [baseline, setBaseline] = useState<CollectionSpec | null>(null);
  const [restored, setRestored] = useState<StoredDraft<CollectionSpec> | null>(null);
  const [savedLocallyAt, setSavedLocallyAt] = useState<string | null>(null);
  const dKey = draftKey("collection", id ?? newDraftId);

  // Initialise from the server copy (or template), restoring a newer local draft.
  useEffect(() => {
    if (!source.data) return;
    const base = source.data;
    const stored = loadDraft<CollectionSpec>(dKey);
    setBaseline(base);
    if (stored && !same(stored.value, base)) {
      history.reset(normalizeSpec(stored.value));
      setRestored(stored);
    } else {
      history.reset(base);
      setRestored(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source.data, dKey]);

  const ready = baseline !== null;
  const dirty = ready && !same(spec, baseline);
  const isNew = !id;
  const readOnly = Boolean(baseline?.builtIn);

  // Autosave the working copy locally.
  const debouncedSpec = useDebouncedValue(spec, 600);
  useEffect(() => {
    if (!ready || readOnly) return;
    if (!same(debouncedSpec, baseline)) {
      saveDraft(dKey, debouncedSpec, baseline?.updatedAt);
      setSavedLocallyAt(new Date().toISOString());
    } else {
      clearDraft(dKey);
      setSavedLocallyAt(null);
    }
  }, [debouncedSpec, baseline, dKey, ready, readOnly]);

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const update = useCallback((patch: Partial<CollectionSpec>, key?: string) => {
    history.set({ ...history.value, ...patch }, key);
  }, [history]);

  // ---- Live preview ---------------------------------------------------------
  const previewKey = useMemo(() => JSON.stringify(previewableSpec(spec)), [spec]);
  const debouncedPreview = useDebouncedValue(previewKey, 450);
  const [preview, setPreview] = useState<CollectionPreview | null>(null);
  const [previewError, setPreviewError] = useState<unknown>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewNonce, setPreviewNonce] = useState(0);
  useEffect(() => {
    if (!ready) return;
    const ctl = new AbortController();
    setPreviewLoading(true);
    api.previewCollection(JSON.parse(debouncedPreview) as CollectionSpec, ctl.signal)
      .then((p) => { setPreview(p); setPreviewError(null); })
      .catch((e) => { if (!isAbort(e) && !ctl.signal.aborted) setPreviewError(e); })
      .finally(() => { if (!ctl.signal.aborted) setPreviewLoading(false); });
    return () => ctl.abort();
  }, [debouncedPreview, ready, previewNonce]);

  // ---- Persistence ------------------------------------------------------------
  const [saving, setSaving] = useState(false);
  const [problems, setProblems] = useState<string[]>([]);
  const [rebuilding, setRebuilding] = useState(false);
  const [describeOpen, setDescribeOpen] = useState(false);
  const [tab, setTab] = useState<Tab>("build");
  const wide = useMediaQuery("(min-width: 1024px)");
  const [pane, setPane] = useState<"edit" | "preview">("edit");

  const save = useCallback(async (publish?: boolean) => {
    if (readOnly) return;
    const next = publish === undefined ? spec : { ...spec, enabled: publish };
    const found = specProblems(next);
    setProblems(found);
    if (found.length) { toast.error("Not saved yet", { description: found[0] }); return; }
    setSaving(true);
    try {
      const saved = normalizeSpec(id ? await api.updateCollection(id, next) : await api.createCollection(next));
      clearDraft(dKey);
      setRestored(null);
      setBaseline(saved);
      history.reset(saved);
      toast.success(publish === true ? "Published" : publish === false ? "Unpublished" : "Saved", {
        description: saved.enabled ? `${saved.name} is live in Plex.` : `${saved.name} is saved as a draft. Publish it when you're ready.`,
      });
      if (!id && saved.id) navigate(`/collections/${encodeURIComponent(saved.id)}`, { replace: true });
    } catch (e) {
      toast.error("Couldn't save", { description: errorMessage(e) });
    } finally { setSaving(false); }
  }, [readOnly, spec, id, dKey, history, navigate]);

  const rebuild = async () => {
    if (!id) return;
    setRebuilding(true);
    try {
      const r = await api.rebuildCollection(id);
      setBaseline((b) => (b ? { ...b, itemCount: r.itemCount, lastBuiltAt: r.lastBuiltAt } : b));
      history.reset({ ...spec, itemCount: r.itemCount, lastBuiltAt: r.lastBuiltAt });
      toast.success("Rebuilt", { description: `${r.itemCount} titles.` });
    } catch (e) { toast.error("Rebuild failed", { description: errorMessage(e) }); }
    finally { setRebuilding(false); }
  };

  const remove = async () => {
    if (!id) return;
    const ok = await confirmDialog({ title: "Delete collection?", message: `“${spec.name}” disappears from Plex for everyone. This can't be undone.`, confirmLabel: "Delete", tone: "danger" });
    if (!ok) return;
    try {
      await api.deleteCollection(id);
      clearDraft(dKey);
      toast.success("Collection deleted");
      navigate("/collections");
    } catch (e) { toast.error("Couldn't delete", { description: errorMessage(e) }); }
  };

  const duplicate = () => navigate(newCollectionHref(), { state: { spec: { ...spec, id: undefined, builtIn: undefined, name: `${spec.name} (copy)`, enabled: false } } });

  const discardLocal = () => {
    if (!baseline) return;
    clearDraft(dKey);
    setRestored(null);
    history.reset(baseline);
  };

  // Keyboard: undo/redo outside text fields, save anywhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (!mod) return;
      const k = e.key.toLowerCase();
      if (k === "s") { e.preventDefault(); void save(); return; }
      if (isTextTarget(e.target)) return;
      if (k === "z" && !e.shiftKey) { e.preventDefault(); history.undo(); }
      else if ((k === "z" && e.shiftKey) || k === "y") { e.preventDefault(); history.redo(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [history, save]);

  if (source.error) {
    return (
      <div className="p-6">
        <Link to="/collections" className="mb-4 inline-flex items-center gap-1 text-sm text-fg-muted hover:text-fg"><ArrowLeft size={14} /> Collections</Link>
        <ErrorPanel error={source.error} feature="Collection Studio" onRetry={() => void source.reload()} />
      </div>
    );
  }
  if (!ready) return <div className="grid flex-1 place-items-center"><Spinner size={22} label="Loading collection" /></div>;

  const showEdit = wide || pane === "edit";
  const showPreview = wide || pane === "preview";
  const targets = describeTargets(spec);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="shrink-0 border-b border-border bg-surface px-3 py-3 sm:px-6">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <Link to="/collections" className="inline-flex items-center gap-1 text-sm text-fg-muted hover:text-fg"><ArrowLeft size={14} /> Collections</Link>
          <span className="flex flex-wrap gap-1.5">
            {readOnly ? <Badge variant="secondary">Built-in</Badge> : spec.enabled ? <Badge variant="success"><Globe2 size={11} /> Published</Badge> : <Badge variant="secondary">Draft</Badge>}
            {isNew ? <Badge variant="info">New</Badge> : null}
            {dirty ? <Badge variant="warning">Unsaved changes</Badge> : null}
          </span>
          <span className="ml-auto text-xs text-fg-subtle" aria-live="polite">
            {savedLocallyAt && dirty ? <><History size={11} className="mr-1 inline" />Draft kept on this device · {relativeTime(savedLocallyAt)}</> : null}
          </span>
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Input value={spec.name} onChange={(e) => update({ name: e.currentTarget.value }, "name")} placeholder="Name your collection"
            aria-label="Collection name" disabled={readOnly}
            className="h-11 min-w-0 flex-1 border-transparent bg-transparent px-1 text-xl font-semibold shadow-none hover:border-border focus-visible:border-primary sm:text-xl" />
          <div className="flex flex-wrap items-center gap-1.5">
            <Button variant="ghost" size="icon" className="size-9" onClick={history.undo} disabled={!history.canUndo || readOnly} aria-label="Undo" title="Undo (⌘Z)"><Undo2 size={16} /></Button>
            <Button variant="ghost" size="icon" className="size-9" onClick={history.redo} disabled={!history.canRedo || readOnly} aria-label="Redo" title="Redo (⇧⌘Z)"><Redo2 size={16} /></Button>
            {!readOnly ? <Button variant="ghost" size="sm" onClick={() => setDescribeOpen(true)}><Sparkles size={15} /> Describe it</Button> : null}
            <ActionMenu label="Collection actions" variant="outline" size="icon" className="size-9" items={[
              { key: "dup", label: "Duplicate", icon: Copy, onSelect: duplicate },
              { key: "rebuild", label: "Rebuild now", icon: RefreshCw, onSelect: () => void rebuild(), disabled: isNew || dirty || readOnly },
              { key: "unpublish", label: "Unpublish", icon: EyeOff, onSelect: () => void save(false), disabled: isNew || !baseline?.enabled || readOnly },
              { key: "discard", label: "Discard unsaved changes", icon: Undo2, onSelect: discardLocal, disabled: !dirty },
              { key: "sep", separator: true },
              { key: "delete", label: "Delete", icon: Trash2, onSelect: () => void remove(), danger: true, disabled: isNew || readOnly },
            ]} />
            {!readOnly ? (
              <>
                <Button variant="outline" size="sm" className="h-9" onClick={() => void save()} disabled={saving || (!dirty && !isNew)}><Save size={15} /> {saving ? "Saving…" : "Save"}</Button>
                {!spec.enabled || dirty || isNew ? (
                  <Button size="sm" className="h-9" onClick={() => void save(true)} disabled={saving}><Globe2 size={15} /> {spec.enabled && !isNew ? "Save & publish" : "Publish"}</Button>
                ) : null}
              </>
            ) : <Button size="sm" className="h-9" onClick={duplicate}><Copy size={15} /> Duplicate to edit</Button>}
          </div>
        </div>
      </header>

      {restored || problems.length || readOnly ? (
        <div className="shrink-0 space-y-2 border-b border-border bg-surface px-3 py-2 sm:px-6">
          {readOnly ? <Alert variant="info"><AlertDescription>Built-in collections are managed by Ether. Duplicate this one to customise it.</AlertDescription></Alert> : null}
          {restored ? (
            <Alert variant="info">
              <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
                <span>Restored unsaved changes from {relativeTime(restored.savedAt)}.</span>
                <Button variant="ghost" size="sm" onClick={discardLocal}>Discard them</Button>
              </AlertDescription>
            </Alert>
          ) : null}
          {problems.length ? (
            <Alert variant="warning">
              <AlertTitle>Before you save</AlertTitle>
              <AlertDescription><ul className="list-disc pl-5">{problems.map((p) => <li key={p}>{p}</li>)}</ul></AlertDescription>
            </Alert>
          ) : null}
        </div>
      ) : null}

      {!wide ? (
        <div className="shrink-0 border-b border-border bg-surface px-3 py-2">
          <Segmented ariaLabel="Show" value={pane} onChange={setPane} className="w-full [&>button]:flex-1"
            options={[{ value: "edit", label: "Edit", icon: <PencilRuler size={14} /> }, { value: "preview", label: preview ? `Preview · ${preview.total}` : "Preview", icon: <Eye size={14} /> }]} />
        </div>
      ) : null}

      <div className="flex min-h-0 flex-1">
        {showEdit ? (
          <aside className={cn("app-scroll min-h-0 overflow-y-auto bg-surface", wide ? "w-[30rem] shrink-0 border-r border-border xl:w-[34rem]" : "w-full")} aria-label="Collection editor">
            <div className="sticky top-0 z-10 border-b border-border bg-surface px-3 sm:px-5">
              <SubtabNav ariaLabel="Editor sections" active={tab} onSelect={(k) => setTab(k as Tab)} tabs={[
                { key: "build", label: "Build" }, { key: "schedule", label: "Schedule" }, { key: "artwork", label: "Artwork" }, { key: "audience", label: "Audience" },
              ]} />
            </div>
            <fieldset disabled={readOnly} className="min-w-0 p-3 sm:p-5">
              {tab === "build" ? <BuildPanel spec={spec} update={update} region={region} /> : null}
              {tab === "schedule" ? <SchedulePanel spec={spec} update={update} onRebuild={!isNew && !dirty ? () => void rebuild() : undefined} rebuilding={rebuilding} /> : null}
              {tab === "artwork" ? <ArtworkPanel spec={spec} update={update} preview={preview?.items ?? []} /> : null}
              {tab === "audience" ? <AudiencePanel spec={spec} update={update} /> : null}
            </fieldset>
          </aside>
        ) : null}

        {showPreview ? (
          <section className="app-scroll min-h-0 min-w-0 flex-1 overflow-y-auto bg-bg-subtle" aria-label="Live preview">
            <div className="mx-auto max-w-6xl space-y-5 p-4 sm:p-6">
              <div className="flex flex-wrap items-start gap-4 rounded-xl border border-border bg-surface p-4">
                <ArtworkPreview spec={spec} items={preview?.items ?? []} className="w-20 shrink-0 sm:w-24" />
                <div className="min-w-0 flex-1">
                  <p className="text-xs font-medium uppercase tracking-wider text-fg-subtle">How it appears in Plex</p>
                  <p className="mt-1 truncate text-lg font-semibold text-fg">{spec.name.trim() || "Untitled collection"}</p>
                  {spec.description ? <p className="line-clamp-2 text-sm text-fg-muted">{spec.description}</p> : null}
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {targets.length ? targets.map((t) => <Badge key={t} variant="default">{t}</Badge>) : <Badge variant="warning">Not shown anywhere</Badge>}
                    <Badge variant="secondary">{spec.targets.users === "all" ? "Everyone" : `${spec.targets.users.length} viewer${spec.targets.users.length === 1 ? "" : "s"}`}</Badge>
                    {spec.targets.schedule ? <Badge variant="secondary">{describeSchedule(spec)}</Badge> : null}
                    <Badge variant="secondary">Refresh {REFRESH_OPTIONS.find((r) => r.value === spec.refresh)?.label.toLowerCase()}</Badge>
                    {spec.rotate ? <Badge variant="info">Rotates {spec.rotate.size} {spec.rotate.every}</Badge> : null}
                  </div>
                </div>
              </div>

              <div className="flex flex-wrap items-center justify-between gap-2">
                <h2 className="flex items-center gap-2 text-sm font-semibold text-fg">
                  Preview
                  <span className="font-normal text-fg-muted" aria-live="polite">
                    {preview ? `${preview.total.toLocaleString()} ${preview.total === 1 ? "title" : "titles"}` : ""}
                  </span>
                  {previewLoading ? <Spinner size={14} label="Updating preview" /> : null}
                </h2>
                {preview ? <span className="text-xs text-fg-subtle">{preview.items.length < preview.total ? `Showing the first ${preview.items.length} · ` : ""}{preview.tookMs} ms</span> : null}
              </div>
              {preview?.warnings?.map((w) => <Alert key={w} variant="warning"><AlertDescription>{w}</AlertDescription></Alert>)}
              {previewError ? (
                <ErrorPanel error={previewError} feature="Live preview" onRetry={() => setPreviewNonce((n) => n + 1)} />
              ) : !preview ? <PosterSkeletons count={18} /> : preview.items.length === 0 ? (
                <EmptyState icon={<SearchX />} title="No titles match" description="Loosen a rule, switch “all” to “any”, or add another source." />
              ) : (
                <div className={cn("transition-opacity", previewLoading && "opacity-60")}>
                  <PosterGrid items={preview.items} />
                </div>
              )}
            </div>
          </section>
        ) : null}
      </div>

      <DescribeDialog open={describeOpen} onClose={() => setDescribeOpen(false)} applyLabel="Replace rules"
        onApply={(g) => update({ sources: g.sources, rules: g.rules, exclude: g.exclude, match: g.match, sort: g.sort, sortDesc: g.sortDesc, mediaType: g.mediaType, name: spec.name.trim() ? spec.name : g.name, description: spec.description || g.description })}
        secondary={{ label: "Open as new collection", onApply: (g) => navigate(newCollectionHref(), { state: { spec: g } }) }} />
    </div>
  );
}
