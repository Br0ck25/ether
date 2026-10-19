import {
  Badge,
  Button,
  Input,
  SettingsSection,
  Spinner,
  Switch,
  toast,
} from "@braedonsaunders/appkit-ui";
import { RefreshCw, X } from "lucide-react";
import { useState } from "react";
import { Artwork } from "../../components/Artwork";
import { ErrorPanel } from "../../components/feedback";
import { NumberInput } from "../../components/fields";
import { Section } from "../../components/layout";
import { TitleSearch } from "../../components/pickers";
import { titleSubtitle } from "../../components/Poster";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { apiFetch, errorMessage } from "../../lib/api";
import type { TitleCard } from "../../lib/contract";
import { useAsync } from "../../lib/hooks";
import { nativeSelect } from "./shared";

export type RatingSettings = {
  enabled: boolean;
  provider: string;
  apiKey: string;
  critic: string;
  audience: string;
  cacheHours: number;
};
type RatingBadge = { source: string; value: number; scale: number };

/** The server keeps the key and sends this placeholder back. */
const STORED_KEY = "[configured]";

const SCORE_CHOICES = [
  { value: "", label: "Keep catalogue default" },
  { value: "imdb", label: "IMDb" },
  { value: "tomatoes", label: "Rotten Tomatoes critics" },
  { value: "popcorn", label: "Rotten Tomatoes audience (MDBList)" },
];

/** OMDb / MDBList rating sources, edited in place, with a live badge check beside them. */
export function RatingSources() {
  const doc = useAsync(() => apiFetch<RatingSettings>("/discovery/ratings/settings"), []);
  const { draft, setDraft, dirty, reset } = useDraft(doc.data);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);

  if (doc.error) return <ErrorPanel error={doc.error} onRetry={() => void doc.reload()} />;
  if (!draft)
    return (
      <div className="py-10 text-center">
        <Spinner size={20} label="Loading rating settings" />
      </div>
    );

  const set = (patch: Partial<RatingSettings>) => setDraft({ ...draft, ...patch });
  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const next = await apiFetch<RatingSettings>("/discovery/ratings/settings", {
        method: "PUT",
        body: JSON.stringify(draft),
      });
      doc.setData(next);
      toast.success("Rating sources saved");
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <SettingsSection
          title="Rating sources"
          description="Show IMDb and Rotten Tomatoes scores in Plex using your own OMDb or MDBList key. Scores a source doesn't have stay blank."
        >
          <SettingsRow
            title={<label htmlFor="ratings-enabled">External ratings</label>}
            description="Off: Plex shows the catalogue's own rating."
            control={
              <Switch
                id="ratings-enabled"
                aria-label="External ratings"
                checked={draft.enabled}
                onChange={(e) => set({ enabled: e.currentTarget.checked })}
              />
            }
          />
          <SettingsRow
            title={<label htmlFor="ratings-provider">Provider</label>}
            description="OMDb has IMDb, Rotten Tomatoes critics and Metacritic. MDBList adds audience and Letterboxd scores."
            control={
              <select
                id="ratings-provider"
                className={`${nativeSelect} sm:w-48`}
                value={draft.provider}
                onChange={(e) => set({ provider: e.currentTarget.value })}
              >
                <option value="omdb">OMDb</option>
                <option value="mdblist">MDBList</option>
              </select>
            }
          />
          <SettingsRow
            title={<label htmlFor="ratings-key">API key</label>}
            description={
              draft.apiKey === STORED_KEY
                ? "Saved. Leave as is to keep it, or type a new key to replace it."
                : "Your own key from the provider's website."
            }
            control={
              <Input
                id="ratings-key"
                type="password"
                autoComplete="new-password"
                spellCheck={false}
                className="sm:w-64"
                value={draft.apiKey}
                onFocus={(e) => {
                  if (draft.apiKey === STORED_KEY) e.currentTarget.select();
                }}
                onChange={(e) => set({ apiKey: e.currentTarget.value })}
              />
            }
          />
          {(
            [
              ["critic", "Plex rating", "The main score on a title's page."],
              ["audience", "Plex audience rating", "The second score beside it."],
            ] as const
          ).map(([key, label, hint]) => (
            <SettingsRow
              key={key}
              title={<label htmlFor={`ratings-${key}`}>{label}</label>}
              description={hint}
              control={
                <select
                  id={`ratings-${key}`}
                  className={`${nativeSelect} sm:w-64`}
                  value={draft[key]}
                  onChange={(e) => set({ [key]: e.currentTarget.value })}
                >
                  {SCORE_CHOICES.map((c) => (
                    <option key={c.value} value={c.value}>
                      {c.label}
                    </option>
                  ))}
                </select>
              }
            />
          ))}
          <SettingsRow
            title={<label htmlFor="ratings-cache">Keep scores for</label>}
            description="Hours before a title's scores are looked up again."
            control={
              <span className="flex items-center gap-2">
                <NumberInput
                  id="ratings-cache"
                  className="w-24"
                  min={1}
                  max={2160}
                  value={draft.cacheHours}
                  onChange={(n) => set({ cacheHours: n })}
                />
                <span className="text-sm text-fg-muted">hours</span>
              </span>
            }
          />
        </SettingsSection>
        <BadgePreview enabled={Boolean(doc.data?.enabled)} dirty={dirty} />
      </div>
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

/** Look up one title's badges with the saved sources. */
function BadgePreview({ enabled, dirty }: { enabled: boolean; dirty: boolean }) {
  const [title, setTitle] = useState<TitleCard | null>(null);
  const [badges, setBadges] = useState<RatingBadge[] | null>(null);
  const [busy, setBusy] = useState(false);
  const refresh = async (t: TitleCard) => {
    setBusy(true);
    try {
      setBadges(
        await apiFetch<RatingBadge[]>(`/discovery/ratings/refresh/${t.key}`, {
          method: "POST",
          body: "{}",
        }),
      );
    } catch (e) {
      toast.error("Couldn't look up ratings", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Section
      title="Title badges"
      description={
        enabled
          ? "Check the scores Plex will show for a title."
          : "Turn on external ratings and save to check a title's scores."
      }
    >
      {title ? (
        <div className="space-y-3">
          <div className="flex items-center gap-3">
            <Artwork src={title.poster} alt="" title="" className="h-16 w-11 shrink-0 rounded" />
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium text-fg">{title.title}</p>
              <p className="truncate text-xs text-fg-muted">{titleSubtitle(title)}</p>
            </div>
            <Button
              variant="ghost"
              size="icon"
              className="size-8"
              aria-label="Choose another title"
              onClick={() => {
                setTitle(null);
                setBadges(null);
              }}
            >
              <X size={14} />
            </Button>
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={busy || !enabled}
            onClick={() => void refresh(title)}
          >
            <RefreshCw size={14} /> {busy ? "Looking up…" : "Refresh ratings"}
          </Button>
          {dirty ? (
            <p className="text-xs text-fg-muted">Uses your saved sources, not unsaved changes.</p>
          ) : null}
          {badges ? (
            badges.length ? (
              <ul className="flex flex-wrap gap-1.5" aria-label="Rating badges">
                {badges.map((b) => (
                  <li key={b.source}>
                    <Badge variant="outline">
                      {b.source}: {b.value}/{b.scale}
                    </Badge>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-sm text-fg-muted">No scores found for this title.</p>
            )
          ) : null}
        </div>
      ) : (
        <TitleSearch placeholder="Find a movie or show…" onPick={setTitle} />
      )}
    </Section>
  );
}
