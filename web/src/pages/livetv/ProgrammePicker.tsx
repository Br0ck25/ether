import { Button } from "@braedonsaunders/appkit-ui";
import { ArrowDown, ArrowUp, Shuffle, X } from "lucide-react";
import { useRef, useState } from "react";
import { Artwork } from "../../components/Artwork";
import { Segmented } from "../../components/fields";
import { TitleSearch } from "../../components/pickers";
import { DragHandle, SortableList } from "../../components/Sortable";
import { api, errorMessage } from "../../lib/api";
import type { TitleCard } from "../../lib/contract";
import { plural } from "../../lib/format";
import { duration, loopLength, type Item } from "./model";

type Include = "all" | "movie" | "show";

// A show's catalogue key sits in [2e15, 3e15) (internal/core/ratingkey.go).
const SHOW_KEY_MIN = 2e15;
const SHOW_KEY_MAX = 3e15;

/**
 * A hand-built running order of movies and TV shows: search to add, drag (or
 * arrows) to reorder. A show is added whole and the server replaces it with
 * its episodes, in order, when the channel is saved.
 */
export function ProgrammePicker({
  items,
  onChange,
  label,
}: {
  items: Item[];
  onChange: (items: Item[]) => void;
  label: string;
}) {
  const [notice, setNotice] = useState("");
  const [include, setInclude] = useState<Include>("all");
  const pending = useRef(false);
  async function pick(card: TitleCard) {
    if (pending.current) return;
    pending.current = true;
    setNotice("");
    try {
      if (card.type === "show") {
        // A placeholder: the server expands the show into its episodes on save.
        onChange([
          ...items,
          {
            key: Number(card.key),
            title: card.title,
            durationMs: 60000,
            summary: card.summary,
            rating: card.contentRating,
            thumb: card.poster,
          },
        ]);
        return;
      }
      const title = card.runtimeMinutes ? card : await api.title(card.key);
      if (!title.runtimeMinutes || title.runtimeMinutes <= 0)
        throw new Error(
          "This movie has no running time yet. Choose another movie or add it through a collection after its metadata is refreshed.",
        );
      onChange([
        ...items,
        {
          key: Number(title.key),
          title: title.title,
          durationMs: title.runtimeMinutes * 60000,
          summary: title.summary,
          rating: title.contentRating,
          thumb: title.poster,
        },
      ]);
    } catch (e) {
      setNotice(errorMessage(e));
    } finally {
      pending.current = false;
    }
  }
  function move(index: number, offset: number) {
    const next = [...items];
    const current = next[index],
      other = next[index + offset];
    if (!current || !other) return;
    next[index] = other;
    next[index + offset] = current;
    onChange(next);
  }
  const ids = items.map((item, i) => `${item.key}-${i}`);
  return (
    <div className="space-y-3" role="group" aria-label={label}>
      <Segmented
        ariaLabel="Add"
        size="sm"
        value={include}
        onChange={(v) => setInclude(v as Include)}
        options={[
          { value: "all", label: "Movies and shows" },
          { value: "movie", label: "Movies" },
          { value: "show", label: "TV shows" },
        ]}
      />
      <TitleSearch
        type={include === "all" ? undefined : include}
        placeholder={
          include === "movie"
            ? "Find a movie to add…"
            : include === "show"
              ? "Find a TV show to add…"
              : "Find a movie or TV show to add…"
        }
        excludeKeys={new Set(items.map((i) => String(i.key)))}
        onPick={(title) => void pick(title)}
      />
      {notice && (
        <p role="alert" className="text-sm text-danger">
          {notice}
        </p>
      )}
      {items.length ? (
        <>
          <div className="flex items-center justify-between text-xs text-fg-muted">
            <span>
              {plural(items.length, "title")} · {duration(loopLength(items))} loop
            </span>
            {items.length > 1 ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => onChange([...items].sort(() => Math.random() - 0.5))}
              >
                <Shuffle size={12} /> Shuffle order
              </Button>
            ) : null}
          </div>
          <SortableList
            items={items}
            getId={(item) => ids[items.indexOf(item)] ?? String(item.key)}
            onReorder={(next) => onChange(next)}
            className="space-y-1.5"
            renderItem={(item, handle, index) => (
              <div className="flex items-center gap-2 rounded-lg border border-border bg-surface p-1.5 pr-2">
                <DragHandle handle={handle} label={`Reorder ${item.title}`} className="size-7" />
                <span className="w-5 shrink-0 text-right text-xs tabular-nums text-fg-subtle">
                  {index + 1}
                </span>
                <Artwork
                  src={item.thumb}
                  alt=""
                  title={item.title}
                  className="h-12 w-8 shrink-0 rounded"
                />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-fg">{item.title}</p>
                  <p className="text-xs text-fg-muted">
                    {item.durationMs <= 60000 && item.key >= SHOW_KEY_MIN && item.key < SHOW_KEY_MAX
                      ? "All episodes (listed after saving)"
                      : `${Math.round(item.durationMs / 60000)} min`}
                    {item.rating ? ` · ${item.rating}` : ""}
                  </p>
                </div>
                <div className="flex shrink-0 gap-0.5">
                  <Button
                    size="sm"
                    variant="ghost"
                    className="size-7 p-0"
                    aria-label={`Move ${item.title} earlier`}
                    disabled={index === 0}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowUp size={13} />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="size-7 p-0"
                    aria-label={`Move ${item.title} later`}
                    disabled={index === items.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowDown size={13} />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="size-7 p-0 text-fg-subtle hover:text-danger"
                    aria-label={`Remove ${item.title}`}
                    onClick={() => onChange(items.filter((_, n) => n !== index))}
                  >
                    <X size={14} />
                  </Button>
                </div>
              </div>
            )}
          />
        </>
      ) : (
        <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-xs text-fg-muted">
          Search to build the running order. A TV show plays all its episodes in order; save the
          channel to see them listed.
        </p>
      )}
    </div>
  );
}
