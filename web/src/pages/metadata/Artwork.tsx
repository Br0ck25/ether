import { Badge, Button, Input, Spinner, Switch, cn, toast } from "@braedonsaunders/appkit-ui";
import { Check, ImageIcon, Pin, RefreshCw, RotateCcw, SlidersHorizontal } from "lucide-react";
import { useEffect, useState } from "react";
import { Artwork } from "../../components/Artwork";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel, MutationError } from "../../components/feedback";
import { ListInput } from "../../components/fields";
import { Section } from "../../components/layout";
import { TitleSearch } from "../../components/pickers";
import { titleSubtitle } from "../../components/Poster";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { api, apiFetch } from "../../lib/api";
import type { TitleCard } from "../../lib/contract";
import { useAsync, useDebouncedValue } from "../../lib/hooks";
import { blankSpec } from "../../lib/rules";
import { nativeSelect } from "./shared";
import { MetadataWorkspace } from "./MetadataWorkspace";
import { normalizeOverlays, PRESETS, type Overlays } from "./model";
import { PosterCanvas } from "./PosterCanvas";

type Variant = { path: string; language?: string; tags?: string[] };
type Art = {
  posters: Variant[] | null;
  backdrops: Variant[] | null;
  pinnedPoster?: string;
  pinnedBackdrop?: string;
};
const OPTIONS = [
  {
    key: "status",
    label: "Release status badge",
    hint: "New episodes, seasons and returning shows.",
  },
  {
    key: "quality",
    label: "Quality badge",
    hint: "4K, HDR, Dolby Vision and Atmos, when verified.",
  },
  { key: "ratings", label: "Rating badges", hint: "Cached scores, clearly labelled by source." },
  {
    key: "personalize",
    label: "Match posters to taste",
    hint: "Choose tagged artwork for each viewer.",
  },
  { key: "localize", label: "Localized artwork", hint: "Follow the language requested by Plex." },
] as const;

export function ArtworkSettings() {
  const doc = useAsync(
    async () => normalizeOverlays(await apiFetch<Overlays>("/discovery/overlays/settings")),
    [],
  );
  const users = useAsync(() => api.users(), []);
  const { draft, setDraft, dirty, reset } = useDraft(doc.data);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // Use the same catalogue and image pipeline as the Collection Studio.
  // A bounded pool keeps refresh instant and avoids refetching the catalogue.
  const samples = useAsync(async (signal) => {
    const result = await api.previewCollection(
      { ...blankSpec("mixed"), name: "Artwork preview", limit: 60 },
      signal,
    );
    const titles = [
      ...new Map(
        (result.items ?? [])
          .filter((t) => t.poster && (t.type === "movie" || t.type === "show"))
          .map((t) => [t.key, t]),
      ).values(),
    ];
    for (let i = titles.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [titles[i], titles[j]] = [titles[j]!, titles[i]!];
    }
    return titles;
  }, []);
  const [pickedTitle, setTitle] = useState<TitleCard | null>(null);
  const title = pickedTitle ?? samples.data?.[0] ?? null;
  const alternatives = (samples.data ?? []).filter((t) => t.key !== title?.key);
  const refreshTitle = () => {
    if (alternatives.length)
      setTitle(alternatives[Math.floor(Math.random() * alternatives.length)]!);
  };
  const [lang, setLang] = useState("en");
  const [viewer, setViewer] = useState<number | null>(null);
  const [nonce, setNonce] = useState(0);
  const [pinning, setPinning] = useState<TitleCard | null>(null);
  const preview = useDebouncedValue(draft, 180);
  const previewUser = viewer ?? (users.data?.find((u) => u.isOwner) ?? users.data?.[0])?.id ?? null;
  const [mode, setMode] = useState<"poster" | "library" | "compare">("poster");
  if (doc.error)
    return (
      <MetadataWorkspace
        aside={<ErrorPanel error={doc.error} onRetry={() => void doc.reload()} />}
        main={null}
      />
    );
  if (!draft || !preview)
    return (
      <MetadataWorkspace
        aside={<Spinner size={20} label="Loading artwork settings" />}
        main={null}
      />
    );
  const set = (patch: Partial<Overlays>) => setDraft({ ...draft, ...patch });
  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const next = await apiFetch<Overlays>("/discovery/overlays/settings", {
        method: "PUT",
        body: JSON.stringify(draft),
      });
      doc.setData(normalizeOverlays(next));
      setNonce((n) => n + 1);
      toast.success("Artwork settings saved", {
        description: "Plex picks up the new artwork when it refreshes.",
      });
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <MetadataWorkspace
        fitPreview
        actions={
          <Badge variant={dirty ? "warning" : "secondary"}>
            {dirty ? "Unsaved design" : "Saved design"}
          </Badge>
        }
        aside={
          <>
            <div className="metadata-enable">
              <div>
                <label htmlFor="overlay-enabled">Ether posters</label>
                <p>Serve customized posters in Plex</p>
              </div>
              <Switch
                id="overlay-enabled"
                aria-label="Ether posters"
                checked={draft.enabled}
                disabled={saving}
                onChange={(e) => set({ enabled: e.currentTarget.checked })}
              />
            </div>
            <section className="metadata-control-section" aria-label="Design presets">
              <div className="metadata-section-title">
                <h3>Preset</h3>
              </div>
              <div className="metadata-presets">
                {PRESETS.map((p) => {
                  const selected = Object.entries(p.settings).every(
                    ([k, v]) => draft[k as keyof Overlays] === v,
                  );
                  return (
                    <button
                      key={p.name}
                      type="button"
                      className={cn("metadata-preset", selected && "selected")}
                      aria-pressed={selected}
                      disabled={saving}
                      onClick={() => set(p.settings)}
                    >
                      <span className={`metadata-preset-art preset-${p.name.toLowerCase()}`}>
                        <span style={{ background: p.settings.accent }} />
                        {selected && <Check size={13} />}
                      </span>
                      <strong>{p.name}</strong>
                      <small>{p.description}</small>
                    </button>
                  );
                })}
              </div>
            </section>
            <fieldset disabled={!draft.enabled || saving} className="metadata-control-section">
              <div className="metadata-section-title">
                <h3>Badge appearance</h3>
              </div>
              <label className="metadata-field">
                Badge finish
                <select
                  className={nativeSelect}
                  value={draft.style}
                  onChange={(e) => set({ style: e.currentTarget.value as Overlays["style"] })}
                >
                  <option value="glass">Glass</option>
                  <option value="solid">Solid</option>
                  <option value="minimal">Minimal</option>
                </select>
              </label>
              <div className="metadata-field-row">
                <label className="metadata-field">
                  Placement
                  <select
                    className={nativeSelect}
                    value={draft.position}
                    onChange={(e) =>
                      set({ position: e.currentTarget.value as Overlays["position"] })
                    }
                  >
                    <option value="bottom">Bottom</option>
                    <option value="top">Top</option>
                  </select>
                </label>
                <label className="metadata-field">
                  Badge size
                  <select
                    className={nativeSelect}
                    value={draft.size}
                    onChange={(e) => set({ size: e.currentTarget.value as Overlays["size"] })}
                  >
                    <option value="small">Small</option>
                    <option value="medium">Medium</option>
                    <option value="large">Large</option>
                  </select>
                </label>
              </div>
              <div className="metadata-field">
                <span>Accent colour</span>
                <div className="metadata-swatches">
                  {[
                    ["#d4a574", "Sand"],
                    ["#a7c4b5", "Sage"],
                    ["#9ab6d4", "Glacier"],
                    ["#c0a6d8", "Lavender"],
                    ["#e8e4dc", "Pearl"],
                  ].map(([color, name]) => (
                    <button
                      type="button"
                      key={color}
                      aria-label={name}
                      aria-pressed={draft.accent === color}
                      onClick={() => set({ accent: color })}
                      style={{ background: color }}
                    >
                      {draft.accent === color && <Check size={16} />}
                    </button>
                  ))}
                  <label className="metadata-custom-color" title="Custom accent">
                    <input
                      type="color"
                      aria-label="Custom accent"
                      value={draft.accent}
                      onChange={(e) => set({ accent: e.currentTarget.value })}
                    />
                    <SlidersHorizontal size={15} />
                  </label>
                  <code>{draft.accent}</code>
                </div>
              </div>
            </fieldset>
            <section className="metadata-control-section" aria-label="Poster layers">
              <div className="metadata-section-title">
                <h3>Poster layers</h3>
              </div>
              {OPTIONS.map((o) => (
                <div className="metadata-toggle" key={o.key}>
                  <div>
                    <label htmlFor={`overlay-${o.key}`}>{o.label}</label>
                    <p>{o.hint}</p>
                  </div>
                  <Switch
                    id={`overlay-${o.key}`}
                    aria-label={o.label}
                    checked={draft[o.key]}
                    disabled={!draft.enabled || saving}
                    onChange={(e) => set({ [o.key]: e.currentTarget.checked })}
                  />
                </div>
              ))}
            </section>
            <Section
              title="Preview title"
              description="Try your design on artwork from your catalogue."
            >
              <TitleSearch placeholder="Search movies and shows…" onPick={setTitle} />
              {title && (
                <Button size="sm" variant="ghost" onClick={() => setPinning(title)}>
                  <Pin size={13} /> Edit this title’s artwork
                </Button>
              )}
              <div className="metadata-field-row">
                <label className="metadata-field">
                  Preview as
                  <select
                    className={nativeSelect}
                    value={previewUser ?? ""}
                    onChange={(e) => setViewer(Number(e.currentTarget.value))}
                  >
                    <option value="" disabled>
                      Select a viewer
                    </option>
                    {users.data?.map((u) => (
                      <option key={u.id} value={u.id}>
                        {u.title || u.username}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="metadata-field">
                  Preview language
                  <Input
                    value={lang}
                    onChange={(e) => setLang(e.currentTarget.value)}
                    placeholder="en, fr-CA, es"
                  />
                </label>
              </div>
              {users.error ? (
                <ErrorPanel error={users.error} onRetry={() => void users.reload()} />
              ) : null}
            </Section>
            <Section
              title="Title artwork"
              description="Give an individual title its own poster or backdrop."
            >
              <TitleSearch placeholder="Find a title to customise…" onPick={setPinning} />
            </Section>
          </>
        }
        main={
          <>
            <div className="metadata-preview-toolbar">
              <div className="metadata-preview-title" aria-live="polite">
                <h2 title={title?.title}>{title?.title ?? "Artwork preview"}</h2>
                <p>{title ? titleSubtitle(title) : "Catalogue artwork"}</p>
              </div>
              <Button
                size="sm"
                variant="ghost"
                aria-label="Try another artwork"
                title="Try another artwork"
                disabled={!alternatives.length || samples.loading}
                onClick={refreshTitle}
              >
                <RefreshCw size={15} />
              </Button>
              <div className="metadata-view-switch" aria-label="Preview view">
                {(
                  [
                    ["poster", "Poster"],
                    ["library", "Library"],
                    ["compare", "Compare"],
                  ] as const
                ).map(([key, label]) => (
                  <button
                    type="button"
                    aria-pressed={mode === key}
                    key={key}
                    onClick={() => setMode(key)}
                  >
                    {label}
                  </button>
                ))}
              </div>
            </div>
            {title ? (
              <PosterCanvas
                title={title}
                neighbours={alternatives.slice(0, 2)}
                mode={mode}
                settings={preview}
                user={previewUser}
                lang={lang}
                nonce={nonce}
              />
            ) : (
              <div className="metadata-stage metadata-artwork-empty">
                {samples.loading ? (
                  <Spinner size={22} label="Loading catalogue artwork" />
                ) : samples.error ? (
                  <ErrorPanel error={samples.error} onRetry={() => void samples.reload()} />
                ) : (
                  <>
                    <ImageIcon size={28} />
                    <h2>No catalogue artwork yet</h2>
                    <p>
                      Search for a movie or show in Preview title, or refresh after your catalogue
                      has synced.
                    </p>
                    <Button size="sm" variant="outline" onClick={() => void samples.reload()}>
                      Reload catalogue artwork
                    </Button>
                  </>
                )}
              </div>
            )}
            <div className="metadata-preview-caption">
              <span className="metadata-live-dot" />
              <span>
                {mode === "library"
                  ? "Library preview · Plex layouts vary"
                  : "Originals preserved · available badges only"}
              </span>
              <Badge variant="outline">{draft.enabled ? "Ether design" : "Original artwork"}</Badge>
            </div>
          </>
        }
      />
      <TitleArtworkDrawer
        title={pinning}
        lang={lang}
        onClose={() => setPinning(null)}
        onSaved={() => setNonce((n) => n + 1)}
      />
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={save}
        onDiscard={() => {
          reset();
          setError(null);
        }}
      />
    </>
  );
}

/** One title's artwork choices: a record, so it's edited in a drawer. */
function TitleArtworkDrawer({
  title,
  lang,
  onClose,
  onSaved,
}: {
  title: TitleCard | null;
  lang: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [art, setArt] = useState<Art>({ posters: [], backdrops: [] });
  const [choices, setChoices] = useState<Art | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [saveError, setSaveError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!title) return;
    const ctl = new AbortController();
    setChoices(null);
    setLoadError(null);
    setSaveError(null);
    void Promise.all([
      apiFetch<Art>(`/discovery/overlays/artwork/${title.key}`, { signal: ctl.signal }),
      apiFetch<Art>(`/discovery/overlays/variants/${title.key}?lang=${encodeURIComponent(lang)}`, {
        signal: ctl.signal,
      }),
    ])
      .then(([current, loaded]) => {
        if (ctl.signal.aborted) return;
        setChoices(loaded);
        setArt({
          ...current,
          posters: (loaded.posters ?? []).map((v) => ({
            ...v,
            tags: current.posters?.find((p) => p.path === v.path)?.tags ?? [],
          })),
          backdrops: loaded.backdrops,
        });
      })
      .catch((e) => {
        if (!ctl.signal.aborted) setLoadError(e);
      });
    return () => ctl.abort();
    // Reload only for a different title; the language is read when it opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [title?.key]);

  const save = async () => {
    if (!title) return;
    setSaving(true);
    setSaveError(null);
    try {
      await apiFetch(`/discovery/overlays/artwork/${title.key}`, {
        method: "PUT",
        body: JSON.stringify(art),
      });
      toast.success("Artwork saved", { description: title.title });
      onSaved();
      onClose();
    } catch (e) {
      setSaveError(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer
      open={title !== null}
      onClose={onClose}
      title={title?.title ?? "Title artwork"}
      description="Pick the poster and backdrop Plex shows for this title."
      size="xl"
      footer={
        <div className="flex w-full flex-wrap items-center justify-between gap-2">
          <Button
            variant="ghost"
            disabled={!choices}
            onClick={() => setArt({ ...art, pinnedPoster: "", pinnedBackdrop: "" })}
          >
            <RotateCcw size={14} /> Use automatic choices
          </Button>
          <div className="flex gap-2">
            <Button variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button disabled={!choices || saving} onClick={() => void save()}>
              {saving ? "Saving…" : "Save title artwork"}
            </Button>
          </div>
        </div>
      }
    >
      {loadError ? (
        <ErrorPanel error={loadError} />
      ) : !choices ? (
        <div className="py-10 text-center">
          <Spinner size={20} label="Loading artwork choices" />
        </div>
      ) : (
        <div className="space-y-6">
          {saveError ? <MutationError error={saveError} /> : null}
          <section className="space-y-3" aria-label="Posters">
            <div>
              <h3 className="text-sm font-semibold text-fg">Poster</h3>
              <p className="text-xs text-fg-muted">
                Click a poster to pin it for everyone. Tag posters with the genres or people they
                feature so taste matching can pick one per viewer.
              </p>
            </div>
            {choices.posters?.length ? (
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                {choices.posters.map((variant) => {
                  const pinned = art.pinnedPoster === variant.path;
                  return (
                    <div key={variant.path} className="space-y-2">
                      <button
                        type="button"
                        aria-pressed={pinned}
                        aria-label={`Pin poster (${variant.language || "textless"})`}
                        className={cn(
                          "relative block w-full overflow-hidden rounded-lg border-2",
                          pinned ? "border-primary" : "border-transparent",
                        )}
                        onClick={() => setArt({ ...art, pinnedPoster: pinned ? "" : variant.path })}
                      >
                        <Artwork
                          src={`https://image.tmdb.org/t/p/w185${variant.path}`}
                          alt={`Poster (${variant.language || "textless"})`}
                          className="aspect-[2/3]"
                        />
                        {pinned ? (
                          <span className="absolute left-1.5 top-1.5 inline-flex items-center gap-1 rounded bg-primary px-1.5 py-0.5 text-[11px] font-medium text-primary-fg">
                            <Pin size={11} /> Pinned
                          </span>
                        ) : null}
                      </button>
                      <ListInput
                        ariaLabel={`Focus tags for ${variant.path}`}
                        placeholder="Drama, actor name"
                        value={art.posters?.find((p) => p.path === variant.path)?.tags ?? []}
                        onChange={(tags) =>
                          setArt((current) => ({
                            ...current,
                            posters:
                              current.posters?.map((p) =>
                                p.path === variant.path ? { ...p, tags: tags.map(String) } : p,
                              ) ?? [],
                          }))
                        }
                      />
                    </div>
                  );
                })}
              </div>
            ) : (
              <p className="flex items-center gap-2 text-sm text-fg-muted">
                <ImageIcon size={14} /> TMDB has no alternate posters for this title.
              </p>
            )}
          </section>
          <section className="space-y-2">
            <label htmlFor="pinned-backdrop" className="block text-sm font-semibold text-fg">
              Backdrop
            </label>
            <div className="metadata-backdrops">
              {choices.backdrops?.map((v, index) => (
                <button
                  type="button"
                  key={v.path}
                  aria-label={`Pin backdrop ${index + 1} (${v.language || "textless"})`}
                  aria-pressed={art.pinnedBackdrop === v.path}
                  onClick={() =>
                    setArt({ ...art, pinnedBackdrop: art.pinnedBackdrop === v.path ? "" : v.path })
                  }
                  className={cn(
                    "overflow-hidden rounded-lg border-2",
                    art.pinnedBackdrop === v.path ? "border-primary" : "border-transparent",
                  )}
                >
                  <Artwork
                    src={`https://image.tmdb.org/t/p/w300${v.path}`}
                    alt={`Backdrop ${index + 1}`}
                    className="aspect-video"
                  />
                </button>
              ))}
            </div>
            <select
              id="pinned-backdrop"
              className={nativeSelect}
              value={art.pinnedBackdrop || ""}
              onChange={(e) => setArt({ ...art, pinnedBackdrop: e.currentTarget.value })}
            >
              <option value="">Automatic textless backdrop</option>
              {choices.backdrops?.map((v) => (
                <option key={v.path} value={v.path}>
                  {v.language || "Textless"} · {v.path}
                </option>
              ))}
            </select>
          </section>
        </div>
      )}
    </Drawer>
  );
}
