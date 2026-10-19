import { Badge, Button, Input, SearchSelect } from "@braedonsaunders/appkit-ui";
import { ArrowDown, ArrowUp, Plus, Trash2, X } from "lucide-react";
import { useState } from "react";
import { Artwork } from "../../components/Artwork";
import { Segmented } from "../../components/fields";
import { LookupPicker, TitleSearch } from "../../components/pickers";
import { titleSubtitle } from "../../components/Poster";
import { DragHandle, SortableList, arrayMove } from "../../components/Sortable";
import type { Source, TitleCard } from "../../lib/contract";
import { TMDB_LISTS, describeSource, isSourceComplete, sourceMeta } from "../../lib/rules";
import { useTitle } from "../../lib/titles";

export function SourceEditor({
  source,
  onChange,
  onRemove,
  index,
  region,
  removable,
}: {
  source: Source;
  onChange: (s: Source) => void;
  onRemove: () => void;
  index: number;
  region: string;
  removable: boolean;
}) {
  const meta = sourceMeta(source.kind);
  const label = `Source ${index + 1}`;
  return (
    <div className="rounded-lg border border-border bg-surface p-3" role="group" aria-label={label}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-sm font-medium text-fg">{meta?.label ?? source.kind}</p>
          <p className="text-xs text-fg-muted">
            {isSourceComplete(source) && source.kind !== "catalog"
              ? describeSource(source)
              : meta?.description}
          </p>
        </div>
        {removable ? (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="size-8 shrink-0 text-fg-muted hover:text-danger"
            onClick={onRemove}
            aria-label={`Remove ${label.toLowerCase()}`}
          >
            <Trash2 size={15} />
          </Button>
        ) : null}
      </div>
      <div className="mt-2 empty:hidden">
        <SourceBody source={source} onChange={onChange} region={region} label={label} />
      </div>
    </div>
  );
}

function SourceBody({
  source,
  onChange,
  region,
  label,
}: {
  source: Source;
  onChange: (s: Source) => void;
  region: string;
  label: string;
}) {
  switch (source.kind) {
    case "catalog":
      return null;
    case "tmdbList": {
      const lists = TMDB_LISTS.filter((l) => l.media.includes(source.mediaType));
      return (
        <div className="flex flex-wrap items-center gap-2">
          <Segmented
            ariaLabel={`${label} media`}
            size="sm"
            value={source.mediaType}
            onChange={(m) =>
              onChange({
                ...source,
                mediaType: m,
                list: TMDB_LISTS.find((l) => l.value === source.list)?.media.includes(m)
                  ? source.list
                  : "popular",
              })
            }
            options={[
              { value: "movie", label: "Movies" },
              { value: "show", label: "TV" },
            ]}
          />
          <div className="min-w-48 flex-1">
            <SearchSelect
              value={source.list}
              onChange={(v) => v && onChange({ ...source, list: v as typeof source.list })}
              options={lists.map((l) => ({ value: l.value, label: l.label }))}
              ariaLabel={`${label} chart`}
              searchable={false}
            />
          </div>
        </div>
      );
    }
    case "tmdbCollection":
    case "tmdbKeyword":
    case "tmdbCompany":
    case "tmdbNetwork":
    case "tmdbPerson":
    case "watchProvider": {
      const lookup = sourceMeta(source.kind)?.lookup ?? "keyword";
      const picked =
        source.id > 0 ? [{ id: source.id, name: source.label ?? `#${source.id}` }] : [];
      return (
        <div className="space-y-2">
          <LookupPicker
            kind={lookup}
            multiple={false}
            region={source.kind === "watchProvider" ? source.region : region}
            value={picked}
            ariaLabel={`${label} search`}
            onChange={(p) =>
              onChange({ ...source, id: p[0]?.id ?? 0, label: p[0]?.name } as Source)
            }
          />
          {source.kind === "watchProvider" ? (
            <label className="flex items-center gap-2 text-xs text-fg-muted">
              Region
              <Input
                value={source.region}
                maxLength={2}
                className="h-8 w-16 uppercase"
                aria-label={`${label} region`}
                onChange={(e) =>
                  onChange({ ...source, region: e.currentTarget.value.toUpperCase() })
                }
              />
            </label>
          ) : null}
        </div>
      );
    }
    case "traktList":
    case "letterboxdList":
    case "mdblist": {
      const placeholder =
        source.kind === "traktList"
          ? "https://trakt.tv/users/…/lists/…"
          : source.kind === "letterboxdList"
            ? "https://letterboxd.com/…/list/…"
            : "https://mdblist.com/lists/…";
      const bad = source.url.trim() !== "" && !isSourceComplete(source);
      return (
        <div className="space-y-1">
          <Input
            type="url"
            value={source.url}
            placeholder={placeholder}
            aria-label={`${label} URL`}
            aria-invalid={bad || undefined}
            onChange={(e) => onChange({ ...source, url: e.currentTarget.value })}
          />
          {bad ? (
            <p className="text-xs text-danger">Enter a full https:// link to a public list.</p>
          ) : null}
        </div>
      );
    }
    case "tmdbDiscover":
      return (
        <DiscoverParams
          params={source.params}
          onChange={(params) => onChange({ ...source, params })}
          label={label}
        />
      );
    case "titles":
      return <HandPicked keys={source.keys} onChange={(keys) => onChange({ ...source, keys })} />;
  }
}

function DiscoverParams({
  params,
  onChange,
  label,
}: {
  params: Record<string, string>;
  onChange: (p: Record<string, string>) => void;
  label: string;
}) {
  const entries = Object.entries(params);
  const [k, setK] = useState("");
  const [v, setV] = useState("");
  return (
    <div className="space-y-2">
      {entries.length ? (
        <div className="flex flex-wrap gap-1.5">
          {entries.map(([key, value]) => (
            <Badge key={key} variant="secondary" className="gap-1 font-mono text-[11px] text-fg">
              {key}={value}
              <button
                type="button"
                aria-label={`Remove ${key}`}
                onClick={() => {
                  const n = { ...params };
                  delete n[key];
                  onChange(n);
                }}
                className="rounded-full p-0.5 hover:bg-surface-hover"
              >
                <X size={11} />
              </button>
            </Badge>
          ))}
        </div>
      ) : (
        <p className="text-xs text-fg-muted">
          Add TMDB discover parameters, e.g. <code>with_genres=27</code> or{" "}
          <code>vote_average.gte=7</code>.
        </p>
      )}
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (k.trim() && v.trim()) {
            onChange({ ...params, [k.trim()]: v.trim() });
            setK("");
            setV("");
          }
        }}
      >
        <Input
          value={k}
          onChange={(e) => setK(e.currentTarget.value)}
          placeholder="parameter"
          aria-label={`${label} parameter name`}
          className="font-mono text-xs"
        />
        <Input
          value={v}
          onChange={(e) => setV(e.currentTarget.value)}
          placeholder="value"
          aria-label={`${label} parameter value`}
          className="font-mono text-xs"
        />
        <Button
          type="submit"
          variant="outline"
          size="icon"
          className="shrink-0"
          aria-label="Add parameter"
          disabled={!k.trim() || !v.trim()}
        >
          <Plus size={15} />
        </Button>
      </form>
    </div>
  );
}

/** Hand-picked titles: search to add, drag (or use the arrows) to set the order. */
function HandPicked({ keys, onChange }: { keys: string[]; onChange: (k: string[]) => void }) {
  const [known, setKnown] = useState<Record<string, TitleCard>>({});
  const move = (i: number, d: -1 | 1) => {
    const j = i + d;
    if (j < 0 || j >= keys.length) return;
    onChange(arrayMove(keys, i, j));
  };
  return (
    <div className="space-y-3">
      <TitleSearch
        excludeKeys={new Set(keys)}
        onPick={(t) => {
          setKnown((k) => ({ ...k, [t.key]: t }));
          onChange([...keys, t.key]);
        }}
      />
      {keys.length === 0 ? (
        <p className="text-xs text-fg-muted">No titles yet. Search above to add some.</p>
      ) : (
        <SortableList
          items={keys}
          getId={(k) => k}
          onReorder={(next) => onChange(next)}
          className="space-y-1.5"
          renderItem={(key, handle, i) => (
            <PickedRow
              keyId={key}
              hint={known[key]}
              index={i}
              handle={handle}
              onUp={() => move(i, -1)}
              onDown={() => move(i, 1)}
              first={i === 0}
              last={i === keys.length - 1}
              onRemove={() => onChange(keys.filter((k) => k !== key))}
            />
          )}
        />
      )}
      {keys.length > 1 ? (
        <p className="text-[11px] text-fg-subtle">
          Drag the handle or use the arrows to reorder. Sort by “Source order” to keep this order in
          Plex.
        </p>
      ) : null}
    </div>
  );
}

function PickedRow({
  keyId,
  hint,
  index,
  handle,
  onUp,
  onDown,
  onRemove,
  first,
  last,
}: {
  keyId: string;
  hint?: TitleCard;
  index: number;
  handle: Parameters<typeof DragHandle>[0]["handle"];
  onUp: () => void;
  onDown: () => void;
  onRemove: () => void;
  first: boolean;
  last: boolean;
}) {
  const loaded = useTitle(hint ? undefined : keyId);
  const t = hint ?? loaded;
  const name = t?.title ?? `Title ${keyId}`;
  return (
    <div className="flex items-center gap-2 rounded-md border border-border-subtle bg-bg-subtle p-1.5">
      <DragHandle handle={handle} label={`Reorder ${name}`} />
      <span className="w-5 text-right text-xs tabular-nums text-fg-subtle">{index + 1}</span>
      <Artwork src={t?.poster} alt="" title="" className="h-12 w-8 shrink-0 rounded" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm text-fg">{name}</span>
        {t ? (
          <span className="block truncate text-xs text-fg-muted">{titleSubtitle(t)}</span>
        ) : null}
      </span>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-7"
        onClick={onUp}
        disabled={first}
        aria-label={`Move ${name} up`}
      >
        <ArrowUp size={14} />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-7"
        onClick={onDown}
        disabled={last}
        aria-label={`Move ${name} down`}
      >
        <ArrowDown size={14} />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-7 hover:text-danger"
        onClick={onRemove}
        aria-label={`Remove ${name}`}
      >
        <X size={14} />
      </Button>
    </div>
  );
}
