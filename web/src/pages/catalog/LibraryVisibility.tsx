import { Badge, Button, Input, toast } from "@braedonsaunders/appkit-ui";
import { Eye, Filter, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Segmented } from "../../components/fields";
import { ErrorPanel } from "../../components/feedback";
import { Artwork } from "../../components/Artwork";
import { api, apiFetch, errorMessage } from "../../lib/api";
import type { Rule, RuleField } from "../../lib/contract";
import { isRuleComplete } from "../../lib/rules";
import { useAsync, useDebouncedValue } from "../../lib/hooks";
import { RuleRow } from "../collections/RuleRow";

type Exclusion = { name: string; mediaType: string; match: string; rules: Rule[] };
type Preview = {
  total: number;
  hidden: number;
  movies: number;
  shows: number;
  unknownCountry: number;
  items: { key: string; title: string; year: number; poster?: string; posterPath?: string }[];
};
const FIELDS: RuleField[] = [
  "country",
  "language",
  "genre",
  "contentRating",
  "year",
  "rating",
  "votes",
  "popularity",
  "runtime",
];
const condition = (): Rule => ({ field: "country", op: "in", value: [] });

export function LibraryVisibility() {
  const stored = useAsync(
    () => apiFetch<{ value: { exclusions?: Exclusion[] } }>("/config/catalog"),
    [],
  );
  const [groups, setGroups] = useState<Exclusion[]>([]);
  const [saved, setSaved] = useState("[]");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (stored.data) {
      const v = stored.data.value.exclusions ?? [];
      setGroups(v);
      setSaved(JSON.stringify(v));
    }
  }, [stored.data]);
  const draft = useDebouncedValue(groups, 400);
  const complete = draft.every(
    (g) => g.name.trim() && g.rules.length && g.rules.every(isRuleComplete),
  );
  const preview = useAsync(
    async (signal) => {
      if (!complete) return null;
      return apiFetch<Preview>("/catalog/visibility/preview", {
        method: "POST",
        body: JSON.stringify({ exclusions: draft }),
        signal,
      });
    },
    [draft, complete],
  );
  const update = (i: number, patch: Partial<Exclusion>) =>
    setGroups((gs) => gs.map((g, n) => (n === i ? { ...g, ...patch } : g)));
  const save = async () => {
    setSaving(true);
    try {
      await api.putSection("catalog", { exclusions: groups });
      setSaved(JSON.stringify(groups));
      toast.success("Library visibility updated");
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setSaving(false);
    }
  };
  const dirty = JSON.stringify(groups) !== saved;
  return (
    <div className="catalog-scroll app-scroll">
      <div className="grid gap-6 p-5 lg:grid-cols-[minmax(0,1fr)_340px] lg:p-7">
        <div className="space-y-5">
          <div className="rounded-xl border border-border bg-surface p-6">
            <p className="catalog-eyebrow">LIBRARY VISIBILITY</p>
            <h2 className="mt-2 text-2xl font-semibold tracking-tight">
              Your catalogue. Your boundaries.
            </h2>
            <p className="mt-3 max-w-2xl text-sm text-fg-muted">
              Hide matching movies and shows from Plex browsing, search, recommendations and
              collections. Combine conditions inside a group; a title matching any group is hidden.
              Metadata and watch history are kept.
            </p>
            <div className="mt-4 flex flex-wrap gap-2">
              <Badge variant="secondary">Local metadata · no provider searches</Badge>
              <Badge variant="secondary">Applies to all viewers</Badge>
            </div>
          </div>
          {stored.error ? <ErrorPanel error={stored.error} onRetry={stored.reload} /> : null}
          {!stored.loading && groups.length === 0 ? (
            <div className="rounded-xl border border-dashed border-border p-8 text-center">
              <ShieldCheck className="mx-auto mb-3 text-accent" size={28} />
              <h3 className="font-medium">No custom exclusions</h3>
              <p className="mt-2 text-sm text-fg-muted">
                All eligible titles are visible. Add a group to narrow your library.
              </p>
            </div>
          ) : null}
          {groups.map((g, i) => (
            <section
              key={i}
              className="rounded-xl border border-border bg-surface p-5 space-y-4"
              aria-label={`Exclusion group ${i + 1}`}
            >
              <div className="flex items-center gap-3">
                <div className="rounded-lg bg-accent/10 p-2 text-accent">
                  <Filter size={17} />
                </div>
                <Input
                  aria-label={`Group ${i + 1} name`}
                  value={g.name}
                  onChange={(e) => update(i, { name: e.target.value })}
                  placeholder="Name this exclusion"
                />
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`Delete group ${i + 1}`}
                  onClick={() => setGroups((gs) => gs.filter((_, n) => n !== i))}
                >
                  <Trash2 size={16} />
                </Button>
              </div>
              <div className="flex flex-wrap gap-4">
                <Segmented
                  ariaLabel={`Group ${i + 1} media`}
                  value={g.mediaType}
                  onChange={(v) => update(i, { mediaType: v })}
                  options={[
                    { value: "mixed", label: "Movies + TV" },
                    { value: "movie", label: "Movies" },
                    { value: "show", label: "TV shows" },
                  ]}
                />
                <Segmented
                  ariaLabel={`Group ${i + 1} match`}
                  value={g.match}
                  onChange={(v) => update(i, { match: v })}
                  options={[
                    { value: "all", label: "Match all" },
                    { value: "any", label: "Match any" },
                  ]}
                />
              </div>
              {g.rules.map((r, j) => (
                <RuleRow
                  key={j}
                  rule={r}
                  index={j}
                  kind="exclude"
                  mediaType={g.mediaType}
                  region=""
                  allowedFields={FIELDS}
                  onChange={(v) => update(i, { rules: g.rules.map((r, k) => (k === j ? v : r)) })}
                  onRemove={() => update(i, { rules: g.rules.filter((_, k) => k !== j) })}
                />
              ))}
              <Button
                size="sm"
                variant="outline"
                disabled={g.rules.length >= 20}
                onClick={() => update(i, { rules: [...g.rules, condition()] })}
              >
                <Plus size={14} />
                Add condition
              </Button>
            </section>
          ))}
          <div className="flex flex-wrap justify-between gap-3">
            <Button
              variant="outline"
              disabled={stored.loading || groups.length >= 32}
              onClick={() =>
                setGroups((gs) => [
                  ...gs,
                  {
                    name: `Exclusion ${gs.length + 1}`,
                    mediaType: "mixed",
                    match: "all",
                    rules: [condition()],
                  },
                ])
              }
            >
              <Plus size={16} />
              Add exclusion group
            </Button>
            <div className="flex gap-2">
              <Button
                variant="ghost"
                disabled={!dirty || saving}
                onClick={() => setGroups(JSON.parse(saved) as Exclusion[])}
              >
                Discard
              </Button>
              <Button
                disabled={
                  !dirty ||
                  saving ||
                  !complete ||
                  JSON.stringify(groups) !== JSON.stringify(draft) ||
                  Boolean(preview.error)
                }
                onClick={() => void save()}
              >
                {saving ? "Saving…" : "Save visibility"}
              </Button>
            </div>
          </div>
        </div>
        <aside
          className="space-y-4 lg:sticky lg:top-0 lg:self-start"
          aria-label="Visibility preview"
        >
          <div className="rounded-xl border border-border bg-surface p-5">
            <div className="flex items-center gap-2 text-sm font-semibold">
              <Eye size={17} />
              Live preview
            </div>
            <p className="mt-2 text-xs text-fg-muted">Unsaved rules · local catalogue only</p>
            {preview.error ? <ErrorPanel error={preview.error} onRetry={preview.reload} /> : null}
            {!complete ? (
              <p className="mt-4 text-sm text-fg-muted">Finish each condition to preview.</p>
            ) : (
              <>
                <div className="mt-5 text-4xl font-semibold tracking-tight">
                  {preview.loading ? "…" : (preview.data?.hidden ?? 0).toLocaleString()}
                </div>
                <p className="mt-1 text-sm text-fg-muted">titles would be hidden</p>
                <div className="mt-4 grid grid-cols-2 gap-3 text-sm">
                  <div className="rounded-lg bg-surface-alt p-3">
                    {(preview.data?.movies ?? 0).toLocaleString()} movies
                  </div>
                  <div className="rounded-lg bg-surface-alt p-3">
                    {(preview.data?.shows ?? 0).toLocaleString()} shows
                  </div>
                </div>
                <p className="mt-4 text-xs text-fg-muted">
                  {Math.max(
                    0,
                    (preview.data?.total ?? 0) - (preview.data?.hidden ?? 0),
                  ).toLocaleString()}{" "}
                  catalogue titles remain before release, availability and viewer restrictions.
                </p>
              </>
            )}
          </div>
          <div className="rounded-xl border border-border p-5 text-sm text-fg-muted">
            <h3 className="mb-2 font-medium text-fg">Country is not language</h3>
            <p>
              Country matches TMDB origin or production countries, including co-productions.
              Language is a separate condition. Unknown metadata never matches an exclusion.
            </p>
            <p className="mt-3">
              {(preview.data?.unknownCountry ?? 0).toLocaleString()} titles currently have no
              country metadata. Normal metadata refreshes fill gaps; saving rules starts no bulk
              lookups.
            </p>
          </div>
          {preview.data?.items.length ? (
            <div className="rounded-xl border border-border p-4">
              <h3 className="mb-3 text-sm font-medium">Example matches</h3>
              <div className="grid grid-cols-3 gap-3">
                {preview.data.items.slice(0, 12).map((t) => (
                  <div key={t.key}>
                    <Artwork
                      src={
                        t.poster ??
                        (t.posterPath
                          ? `https://image.tmdb.org/t/p/w185${t.posterPath}`
                          : undefined)
                      }
                      alt={t.title}
                      className="aspect-[2/3] rounded-md"
                    />
                    <p className="mt-1 truncate text-xs" title={t.title}>
                      {t.title}
                    </p>
                    <p className="text-xs text-fg-muted">{t.year || ""}</p>
                  </div>
                ))}
              </div>
            </div>
          ) : null}
        </aside>
      </div>
    </div>
  );
}
