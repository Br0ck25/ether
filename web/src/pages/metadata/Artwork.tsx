import {
  Button,
  Input,
  SettingsSection,
  Spinner,
  Switch,
  cn,
  toast,
} from "@braedonsaunders/appkit-ui";
import { ImageIcon, Pin, RotateCcw, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Artwork } from "../../components/Artwork";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel, MutationError } from "../../components/feedback";
import { ListInput } from "../../components/fields";
import { Section } from "../../components/layout";
import { TitleSearch } from "../../components/pickers";
import { titleSubtitle } from "../../components/Poster";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { api, apiFetch } from "../../lib/api";
import type { TitleCard } from "../../lib/contract";
import { useAsync } from "../../lib/hooks";
import { nativeSelect } from "./shared";

type Overlays = {
  enabled: boolean;
  status: boolean;
  quality: boolean;
  ratings: boolean;
  personalize: boolean;
  localize: boolean;
};
type Variant = { path: string; language?: string; tags?: string[] };
type Art = {
  posters: Variant[] | null;
  backdrops: Variant[] | null;
  pinnedPoster?: string;
  pinnedBackdrop?: string;
};

const OPTIONS: { key: keyof Overlays; label: string; hint: string }[] = [
  {
    key: "enabled",
    label: "Ether posters",
    hint: "Serve Ether's own posters and backdrops, with the badges and choices below.",
  },
  {
    key: "status",
    label: "Release status badge",
    hint: "New, new season or ended, only when Ether has seen it happen.",
  },
  {
    key: "quality",
    label: "Quality badge",
    hint: "4K, HDR, Dolby Vision or Atmos, only when a verified release has it.",
  },
  {
    key: "ratings",
    label: "Rating badges",
    hint: "Cached scores from Ratings, labelled by source.",
  },
  {
    key: "personalize",
    label: "Match posters to taste",
    hint: "Pick the tagged poster that best fits each viewer's taste.",
  },
  {
    key: "localize",
    label: "Localized artwork",
    hint: "Use posters in the language each viewer's Plex app asks for.",
  },
];

/** Server-wide poster appearance in place, a live preview beside it, and per-title pins. */
export function ArtworkSettings() {
  const doc = useAsync(() => apiFetch<Overlays>("/discovery/overlays/settings"), []);
  const users = useAsync(() => api.users(), []);
  const { draft, setDraft, dirty, reset } = useDraft(doc.data);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [title, setTitle] = useState<TitleCard | null>(null);
  const [lang, setLang] = useState("en");
  const [nonce, setNonce] = useState(0);
  const [pinning, setPinning] = useState<TitleCard | null>(null);
  // Previews render as the owner; personalized choices differ per viewer.
  const previewUser = useMemo(() => {
    const list = users.data ?? [];
    return (list.find((u) => u.isOwner) ?? list[0])?.id ?? null;
  }, [users.data]);

  if (doc.error) return <ErrorPanel error={doc.error} onRetry={() => void doc.reload()} />;
  if (!draft)
    return (
      <div className="py-10 text-center">
        <Spinner size={20} label="Loading artwork settings" />
      </div>
    );

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const next = await apiFetch<Overlays>("/discovery/overlays/settings", {
        method: "PUT",
        body: JSON.stringify(draft),
      });
      doc.setData(next);
      setNonce((n) => n + 1);
      toast.success("Artwork settings saved");
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };
  const previewSrc =
    title && previewUser !== null
      ? `/ether/api/discovery/overlays/preview/${title.key}?userId=${previewUser}&lang=${encodeURIComponent(lang)}&v=${nonce}&settings=${encodeURIComponent(JSON.stringify(draft))}`
      : null;

  return (
    <>
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <SettingsSection
          title="Posters in Plex"
          description="Applies to every viewer. Badges only appear when there's evidence for them."
        >
          {OPTIONS.map((o) => (
            <SettingsRow
              key={o.key}
              title={<label htmlFor={`overlay-${o.key}`}>{o.label}</label>}
              description={o.hint}
              control={
                <Switch
                  id={`overlay-${o.key}`}
                  aria-label={o.label}
                  checked={draft[o.key]}
                  disabled={o.key !== "enabled" && !draft.enabled}
                  onChange={(e) => setDraft({ ...draft, [o.key]: e.currentTarget.checked })}
                />
              }
            />
          ))}
        </SettingsSection>
        <Section
          title="Preview"
          description="Shows your unsaved choices on one title, as you would see it."
        >
          {title ? (
            <div className="space-y-3">
              <div className="flex items-center gap-2">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-fg">{title.title}</p>
                  <p className="truncate text-xs text-fg-muted">{titleSubtitle(title)}</p>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-8"
                  aria-label="Choose another title"
                  onClick={() => setTitle(null)}
                >
                  <X size={14} />
                </Button>
              </div>
              {previewSrc ? (
                <Artwork
                  src={previewSrc}
                  alt={`${title.title} poster preview`}
                  className="mx-auto aspect-[2/3] w-full max-w-[200px] rounded-lg"
                  eager
                />
              ) : (
                <Spinner size={16} label="Loading preview" />
              )}
              <label className="block space-y-1 text-sm">
                <span className="text-fg-muted">Preview language</span>
                <Input
                  value={lang}
                  onChange={(e) => setLang(e.currentTarget.value)}
                  placeholder="en, fr-CA, es"
                />
              </label>
            </div>
          ) : (
            <TitleSearch placeholder="Find a title to preview…" onPick={setTitle} />
          )}
        </Section>
      </div>
      <Section
        title="Title artwork"
        description="Pin a poster or backdrop for one title, or tag its posters so taste matching can choose between them."
      >
        <div className="max-w-xl">
          <TitleSearch placeholder="Find a title to customise…" onPick={setPinning} />
        </div>
      </Section>
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
        <ErrorPanel error={loadError} feature="Artwork choices" />
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
