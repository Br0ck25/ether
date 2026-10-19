import { Drawer } from "../../components/Drawer";
import { EditPanel } from "../../components/EditPanel";
import {
  Button,
  PagedTable,
  PageContainer,
  PageHeader,
  SubtabNav,
  Input,
  Switch,
} from "@braedonsaunders/appkit-ui";
import { useEffect, useRef, useState } from "react";
import { api, apiFetch, errorMessage } from "../../lib/api";
import type { EtherUser } from "../../lib/types";
import { TitleSearch } from "../../components/pickers";
import type { CollectionSpec, TitleCard } from "../../lib/contract";

type Item = {
  key: number;
  title: string;
  durationMs: number;
  summary?: string;
  rating?: string;
  thumb?: string;
};
type Block = { start: number; end: number; items: Item[]; shuffle: boolean };
type Channel = {
  id: string;
  number: string;
  name: string;
  kind: "virtual" | "iptv";
  transcode?: boolean;
  enabled: boolean;
  favorite: boolean;
  rating?: string;
  logo?: string;
  allowedUsers?: number[];
  sourceId?: string;
  guideId?: string;
  streamUrl?: string;
  group?: string;
  epoch: string;
  seed: number;
  shuffle: boolean;
  items: Item[];
  blocks?: Block[];
  collection?: number;
  filter?: Record<string, unknown>;
};
type Source = {
  id: string;
  name: string;
  playlistUrl?: string;
  guideUrl?: string;
  updatedAt: string;
};
type Programme = { id: string; channelId: string; start: string; end: string; item: Item };
type Policy = {
  userId: number;
  disabled: boolean;
  channels: string[];
  favorites: string[];
  maxRating: string;
};
const input = "rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg";
const emptyChannel = (): Channel => ({
  id: "",
  number: "1",
  name: "",
  kind: "virtual",
  enabled: true,
  favorite: false,
  epoch: "1970-01-01T00:00:00Z",
  seed: 1,
  shuffle: false,
  items: [],
});
type CollectionChoice = CollectionSpec & { ratingKey?: string };
const timeOfDay = (minutes: number) =>
  minutes === 1440
    ? "24:00"
    : `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
const minutesOfDay = (value: string) => {
  const [h = NaN, m = NaN] = value.split(":").map(Number);
  return h * 60 + m;
};
function ProgrammePicker({
  items,
  onChange,
  label,
}: {
  items: Item[];
  onChange: (items: Item[]) => void;
  label: string;
}) {
  const [notice, setNotice] = useState("");
  const pending = useRef(false);
  async function pick(card: TitleCard) {
    if (pending.current) return;
    pending.current = true;
    setNotice("");
    try {
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
  return (
    <div className="space-y-3" aria-label={label}>
      <TitleSearch
        type="movie"
        placeholder="Find a movie to add…"
        excludeKeys={new Set(items.map((i) => String(i.key)))}
        onPick={(title) => void pick(title)}
      />
      {notice && (
        <p role="alert" className="text-sm text-danger">
          {notice}
        </p>
      )}
      {items.length ? (
        <ol className="space-y-2">
          {items.map((item, index) => (
            <li
              key={`${item.key}-${index}`}
              className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-3"
            >
              <div>
                <p className="text-sm font-medium">{item.title}</p>
                <p className="text-xs text-fg-muted">
                  {Math.round(item.durationMs / 60000)} min{item.rating ? ` · ${item.rating}` : ""}
                </p>
              </div>
              <div className="flex gap-1">
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={`Move ${item.title} earlier`}
                  disabled={index === 0}
                  onClick={() => move(index, -1)}
                >
                  ↑
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={`Move ${item.title} later`}
                  disabled={index === items.length - 1}
                  onClick={() => move(index, 1)}
                >
                  ↓
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={`Remove ${item.title}`}
                  onClick={() => onChange(items.filter((_, n) => n !== index))}
                >
                  Remove
                </Button>
              </div>
            </li>
          ))}
        </ol>
      ) : (
        <p className="text-sm text-fg-muted">
          Search for movies to build your running order. Use a collection to include TV episodes.
        </p>
      )}
    </div>
  );
}
const clock = (date: string) =>
  new Date(date).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });

export default function LiveTV() {
  const [onlyFavorites, setOnlyFavorites] = useState(false);
  const [sourceOpen, setSourceOpen] = useState(false);
  const [channelTab, setChannelTab] = useState("basics");
  const [channels, setChannels] = useState<Channel[]>([]);
  const [sources, setSources] = useState<Source[]>([]);
  const [guide, setGuide] = useState<Programme[]>([]);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Channel | null>(null);
  const [items, setItems] = useState<Item[]>([]);
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [programming, setProgramming] = useState<"movies" | "collection" | "genre">("movies");
  const [collections, setCollections] = useState<CollectionChoice[]>([]);
  const [collection, setCollection] = useState("");
  const [genre, setGenre] = useState("");
  const [preview, setPreview] = useState<Programme[]>([]);
  const [source, setSource] = useState({
    id: "",
    name: "",
    playlistUrl: "",
    guideUrl: "",
    playlist: "",
    guide: "",
  });
  const [users, setUsers] = useState<EtherUser[]>([]);
  const [viewer, setViewer] = useState(0);
  const [policy, setPolicy] = useState<Policy | null>(null);
  const [tab, setTab] = useState<"channels" | "sources" | "guide" | "access">("channels");
  async function refresh() {
    const [c, s, g] = await Promise.all([
      apiFetch<Channel[]>("/livetv/channels"),
      apiFetch<Source[]>("/livetv/sources"),
      apiFetch<Programme[]>("/livetv/guide"),
    ]);
    setChannels(c);
    setSources(s);
    setGuide(g);
  }
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      apiFetch<Channel[]>("/livetv/channels", { signal: controller.signal }),
      apiFetch<Source[]>("/livetv/sources", { signal: controller.signal }),
      apiFetch<Programme[]>("/livetv/guide", { signal: controller.signal }),
      api.users(),
      api.collections(),
    ])
      .then(([c, s, g, u, collections]) => {
        if (!controller.signal.aborted) {
          setChannels(c);
          setSources(s);
          setGuide(g);
          setUsers(u);
          setCollections(collections);
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setMessage(errorMessage(e));
      });
    return () => controller.abort();
  }, []);
  useEffect(() => {
    if (!viewer) {
      setPolicy(null);
      return;
    }
    const ctrl = new AbortController();
    setPolicy(null);
    void apiFetch<Policy>(`/livetv/policies/${viewer}`, { signal: ctrl.signal })
      .then(setPolicy)
      .catch((e) => {
        if (!ctrl.signal.aborted) setMessage(errorMessage(e));
      });
    return () => ctrl.abort();
  }, [viewer]);
  async function action(fn: () => Promise<void>) {
    setBusy(true);
    setMessage("");
    try {
      await fn();
    } catch (e) {
      setMessage(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  function edit(c: Channel) {
    setChannelTab("basics");
    setMessage("");
    setEditing(c);
    setItems(c.items || []);
    setBlocks(c.blocks || []);
    setProgramming(c.collection ? "collection" : c.filter ? "genre" : "movies");
    setCollection(c.collection ? String(c.collection) : "");
    setGenre(typeof c.filter?.genres === "object" ? (c.filter.genres as string[]).join(", ") : "");
    setPreview([]);
  }
  function draft(): Channel {
    if (!editing) throw new Error("No channel selected");
    if (editing.kind === "virtual") {
      if (programming === "collection" && !collection)
        throw new Error("Choose a collection for this channel.");
      if (
        programming === "genre" &&
        !genre.trim() &&
        !Object.keys(editing.filter || {}).some((key) => key !== "genres" && key !== "type")
      )
        throw new Error("Enter at least one genre for this channel.");
      if (programming === "movies" && !items.length)
        throw new Error("Add at least one movie to this channel.");
      let end = 0;
      for (const block of [...blocks].sort((a, b) => a.start - b.start)) {
        if (
          !Number.isFinite(block.start) ||
          !Number.isFinite(block.end) ||
          block.start < end ||
          block.start < 0 ||
          block.end <= block.start ||
          block.end > 1440
        )
          throw new Error("Time blocks must have valid start and end times without overlapping.");
        if (!block.items.length)
          throw new Error("Add at least one movie to each time block, or remove the empty block.");
        end = block.end;
      }
    }
    return {
      ...editing,
      items,
      blocks,
      collection: programming === "collection" && collection ? Number(collection) : undefined,
      filter:
        programming === "genre"
          ? {
              ...editing.filter,
              type: editing.filter?.type === "show" ? "show" : "movie",
              genres: genre
                .split(",")
                .map((s) => s.trim())
                .filter(Boolean),
            }
          : undefined,
    };
  }
  async function save() {
    await action(async () => {
      const c = draft();
      await apiFetch(`/livetv/channels${c.id ? `/${encodeURIComponent(c.id)}` : ""}`, {
        method: c.id ? "PUT" : "POST",
        body: JSON.stringify(c),
      });
      setEditing(null);
      await refresh();
      setMessage("Channel saved.");
    });
  }
  async function importSource() {
    await action(async () => {
      await apiFetch(`/livetv/sources${source.id ? `/${encodeURIComponent(source.id)}` : ""}`, {
        method: source.id ? "PUT" : "POST",
        body: JSON.stringify(source),
      });
      setSource({ id: "", name: "", playlistUrl: "", guideUrl: "", playlist: "", guide: "" });
      await refresh();
      setMessage("Source and guide imported.");
      setSourceOpen(false);
    });
  }
  async function upload(file: File | undefined, key: "playlist" | "guide") {
    if (!file) return;
    const limit = key === "playlist" ? 16 * 1024 * 1024 : 64 * 1024 * 1024;
    if (file.size > limit) {
      setMessage("The selected file exceeds the import size limit.");
      return;
    }
    try {
      const text = await file.text();
      setSource((s) => ({
        ...s,
        [key]: text,
        [key === "playlist" ? "playlistUrl" : "guideUrl"]: "",
      }));
    } catch (e) {
      setMessage(errorMessage(e));
    }
  }
  const showGuide = (programmes: Programme[]) => (
    <div className="max-h-[32rem] overflow-auto rounded-md border border-border">
      <table className="w-full text-left text-sm">
        <thead className="sticky top-0 bg-bg">
          <tr>
            <th className="p-3">Channel</th>
            <th className="p-3">Airtime</th>
            <th className="p-3">Programme</th>
          </tr>
        </thead>
        <tbody>
          {programmes.map((p) => (
            <tr key={`${p.channelId}-${p.id}`} className="border-t border-border">
              <td className="p-3">
                {channels.find((c) => c.id === p.channelId)?.name || editing?.name || p.channelId}
              </td>
              <td className="whitespace-nowrap p-3">
                {clock(p.start)} –{" "}
                {new Date(p.end).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
              </td>
              <td className="p-3">
                <strong>{p.item.title}</strong>
                {p.item.rating && (
                  <span className="ml-2 text-xs text-fg-muted">{p.item.rating}</span>
                )}
                {p.item.summary && <p className="mt-1 text-xs text-fg-muted">{p.item.summary}</p>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {programmes.length === 0 && (
        <p className="p-4 text-sm text-fg-muted">No programmes in this guide window.</p>
      )}
    </div>
  );
  return (
    <PageContainer className="space-y-6">
      <PageHeader
        title="Live TV"
        description="Create virtual channels from your catalogue, or import your own M3U playlist and XMLTV guide. Plex Live TV uses the owner’s verified Plex Pass status."
      />
      {message && !editing && !sourceOpen && (
        <p role="status" className="rounded-md border border-border p-3 text-sm">
          {message}
        </p>
      )}
      <div className="border-b border-border">
        <SubtabNav
          ariaLabel="Live TV sections"
          active={tab}
          onSelect={(key) => setTab(key as typeof tab)}
          tabs={[
            { key: "channels", label: "Channels" },
            { key: "sources", label: "IPTV sources" },
            { key: "guide", label: "Guide preview" },
            { key: "access", label: "Viewer access" },
          ]}
        />
      </div>
      {tab === "channels" && (
        <>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              onClick={() => {
                const c = emptyChannel();
                c.name = "80s Action 24/7";
                c.filter = { type: "movie", genres: ["Action"], yearFrom: 1980, yearTo: 1989 };
                c.shuffle = true;
                edit(c);
              }}
            >
              80s action template
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                const c = emptyChannel();
                c.name = "Family Animation";
                c.filter = { type: "movie", genres: ["Animation"], maxContentRating: "PG" };
                c.rating = "PG";
                edit(c);
              }}
            >
              Family animation template
            </Button>
          </div>
          <div className="flex items-center justify-between">
            <p className="text-sm text-fg-muted">
              {channels.length} channels · schedules use UTC time blocks
            </p>
            <Button onClick={() => edit(emptyChannel())}>Create channel</Button>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={onlyFavorites} onChange={(e) => setOnlyFavorites(e.target.checked)} />
            Favourites only
          </label>
          <PagedTable
            searchable
            rows={onlyFavorites ? channels.filter((c) => c.favorite) : channels}
            rowKey={(c) => c.id}
            pageSize={20}
            defaultSort={{ key: "number", dir: "asc" }}
            labels={{
              searchLabel: "Search channels",
              searchPlaceholder: "Find a channel or group…",
            }}
            empty={
              onlyFavorites
                ? "No favourite channels yet. Edit a channel to add it."
                : "No channels yet. Create a channel or import an IPTV source."
            }
            columns={[
              {
                key: "number",
                header: "Number",
                sortValue: (c) => parseFloat(c.number) || 0,
                search: (c) => c.number,
                cell: (c) => <span className="tabular-nums">{c.number}</span>,
              },
              {
                key: "name",
                header: "Channel",
                search: (c) => c.name,
                sortValue: (c) => c.name,
                cell: (c) => {
                  const now = guide.find(
                    (p) =>
                      p.channelId === c.id &&
                      new Date(p.start) <= new Date() &&
                      new Date(p.end) > new Date(),
                  );
                  return (
                    <>
                      <p className="font-medium">
                        {c.favorite ? "★ " : ""}
                        {c.name}
                      </p>
                      {now && <p className="text-xs text-fg-muted">On now: {now.item.title}</p>}
                    </>
                  );
                },
              },
              {
                key: "group",
                header: "Group",
                search: (c) => c.group || "",
                cell: (c) => c.group || "—",
              },
              {
                key: "status",
                header: "Status",
                search: (c) => c.kind + " " + (c.enabled ? "enabled" : "disabled"),
                cell: (c) => (
                  <>
                    <p>{c.enabled ? "Enabled" : "Disabled"}</p>
                    <p className="text-xs text-fg-muted">
                      {c.kind === "iptv"
                        ? "IPTV"
                        : c.shuffle
                          ? "Virtual · shuffled"
                          : "Virtual · sequential"}
                    </p>
                  </>
                ),
              },
              {
                key: "actions",
                header: "",
                cell: (c) => (
                  <Button size="sm" variant="outline" onClick={() => edit(c)}>
                    Edit
                  </Button>
                ),
              },
            ]}
          />
          {editing && (
            <Drawer
              open={editing !== null}
              onClose={() => setEditing(null)}
              title={editing?.id ? "Edit channel" : "Create channel"}
              size="xl"
            >
              <fieldset
                disabled={busy}
                className="min-w-0 space-y-4 rounded-lg border border-border p-5"
              >
                <legend className="px-2 font-medium">
                  {editing.id ? "Edit channel" : "New virtual channel"}
                </legend>
                <SubtabNav
                  ariaLabel="Channel settings"
                  active={channelTab}
                  onSelect={setChannelTab}
                  tabs={[
                    { key: "basics", label: "Basics" },
                    {
                      key: "programming",
                      label: editing.kind === "iptv" ? "Stream & guide" : "Programming",
                    },
                    ...(editing.kind === "virtual" ? [{ key: "schedule", label: "Schedule" }] : []),
                    { key: "viewers", label: "Viewers" },
                  ]}
                />
                {message && (
                  <p role="status" className="text-sm text-danger">
                    {message}
                  </p>
                )}
                {channelTab === "basics" && (
                  <>
                    <div className="grid gap-4 sm:grid-cols-3">
                      <label className="flex flex-col gap-2 text-sm">
                        Number
                        <Input
                          className={input}
                          value={editing.number}
                          onChange={(e) => setEditing({ ...editing, number: e.target.value })}
                        />
                      </label>
                      <label className="flex flex-col gap-2 text-sm sm:col-span-2">
                        Name
                        <Input
                          className={input}
                          value={editing.name}
                          onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                        />
                      </label>
                    </div>
                    <div className="flex flex-wrap gap-4">
                      {(
                        [
                          ["enabled", "Enabled"],
                          ["favorite", "Favourite"],
                          ["shuffle", "Shuffle"],
                        ] as const
                      ).map(([key, label]) => (
                        <label key={key} className="flex items-center gap-2 text-sm">
                          <Switch
                            type="checkbox"
                            checked={editing[key]}
                            onChange={(e) => setEditing({ ...editing, [key]: e.target.checked })}
                          />
                          {label}
                        </label>
                      ))}
                    </div>
                    <div className="grid gap-4 sm:grid-cols-2">
                      <label className="flex flex-col gap-2 text-sm">
                        Content rating fallback
                        <Input
                          className={input}
                          value={editing.rating || ""}
                          onChange={(e) => setEditing({ ...editing, rating: e.target.value })}
                          placeholder="TV-PG"
                        />
                      </label>
                      <label className="flex flex-col gap-2 text-sm">
                        Logo URL (blank generates a channel logo)
                        <Input
                          className={input}
                          value={editing.logo || ""}
                          onChange={(e) => setEditing({ ...editing, logo: e.target.value })}
                        />
                      </label>
                    </div>
                  </>
                )}
                {editing.kind === "iptv" ? (
                  <>
                    {channelTab === "programming" && (
                      <>
                        <label className="flex items-center gap-2 text-sm">
                          <Switch
                            type="checkbox"
                            checked={editing.transcode || false}
                            onChange={(e) =>
                              setEditing({ ...editing, transcode: e.target.checked })
                            }
                          />
                          Transcode IPTV codecs for client compatibility
                        </label>
                        <label className="flex flex-col gap-2 text-sm">
                          XMLTV channel ID
                          <Input
                            className={input}
                            value={editing.guideId || ""}
                            onChange={(e) => setEditing({ ...editing, guideId: e.target.value })}
                          />
                        </label>
                        <label className="flex flex-col gap-2 text-sm">
                          Stream URL
                          <Input
                            className={input}
                            value={editing.streamUrl || ""}
                            onChange={(e) => setEditing({ ...editing, streamUrl: e.target.value })}
                          />
                        </label>
                      </>
                    )}
                  </>
                ) : (
                  <>
                    {channelTab === "programming" && (
                      <>
                        <label className="flex flex-col gap-2 text-sm">
                          Programming source
                          <select
                            className={input}
                            value={programming}
                            onChange={(e) => setProgramming(e.target.value as typeof programming)}
                          >
                            <option value="movies">Choose movies</option>
                            <option value="collection">Use a collection</option>
                            <option value="genre">Catalogue genres</option>
                          </select>
                        </label>
                        {programming === "collection" && (
                          <label className="flex flex-col gap-2 text-sm">
                            Collection
                            <select
                              className={input}
                              value={collection}
                              onChange={(e) => setCollection(e.target.value)}
                            >
                              <option value="">Choose a collection</option>
                              {collections
                                .filter((c) => c.ratingKey)
                                .map((c) => (
                                  <option key={c.ratingKey} value={c.ratingKey}>
                                    {c.name}
                                    {c.itemCount !== undefined ? ` · ${c.itemCount} titles` : ""}
                                  </option>
                                ))}
                              {collection &&
                                !collections.some((c) => c.ratingKey === collection) && (
                                  <option value={collection}>Previously selected collection</option>
                                )}
                            </select>
                            <span className="text-xs text-fg-muted">
                              Shows expand into episodes. Manage available collections in Collection
                              Studio.
                            </span>
                          </label>
                        )}
                        {programming === "genre" && (
                          <div className="grid gap-4 sm:grid-cols-2">
                            <label className="flex flex-col gap-2 text-sm">
                              Catalogue type
                              <select
                                className={input}
                                value={editing.filter?.type === "show" ? "show" : "movie"}
                                onChange={(e) =>
                                  setEditing({
                                    ...editing,
                                    filter: { ...editing.filter, type: e.target.value },
                                  })
                                }
                              >
                                <option value="movie">Movies</option>
                                <option value="show">TV shows (all episodes)</option>
                              </select>
                            </label>
                            <label className="flex flex-col gap-2 text-sm">
                              Genres
                              <Input
                                className={input}
                                value={genre}
                                onChange={(e) => setGenre(e.target.value)}
                                placeholder="Action, Adventure"
                              />
                              <span className="text-xs text-fg-muted">
                                Separate genres with commas. Template year and rating limits are
                                retained. Shows expand into episodes.
                              </span>
                            </label>
                          </div>
                        )}
                        {programming === "movies" && (
                          <ProgrammePicker
                            label="Base programming"
                            items={items}
                            onChange={setItems}
                          />
                        )}
                        {programming !== "movies" && (
                          <p className="text-xs text-fg-muted">
                            Saving snapshots the current catalogue into a stable schedule. Save
                            again to refresh membership.
                          </p>
                        )}
                      </>
                    )}
                    {channelTab === "schedule" && (
                      <>
                        <section className="space-y-3">
                          <div className="flex flex-wrap items-center justify-between gap-2">
                            <div>
                              <h3 className="text-sm font-medium">Daily time blocks</h3>
                              <p className="text-xs text-fg-muted">
                                Optional UTC times. Base programming fills the rest of the day.
                              </p>
                            </div>
                            <Button
                              variant="outline"
                              disabled={blocks.some((b) => b.end === 1440)}
                              onClick={() =>
                                setBlocks([
                                  ...blocks,
                                  {
                                    start: blocks.at(-1)?.end || 0,
                                    end: Math.min(1440, (blocks.at(-1)?.end || 0) + 60),
                                    items: [],
                                    shuffle: false,
                                  },
                                ])
                              }
                            >
                              Add time block
                            </Button>
                          </div>
                          {blocks.map((block, index) => (
                            <div
                              key={index}
                              className="space-y-3 rounded-md border border-border p-4"
                            >
                              <div className="flex flex-wrap items-end gap-3">
                                <label className="flex flex-col gap-2 text-sm">
                                  Start (UTC)
                                  <Input
                                    className={input}
                                    type="time"
                                    value={timeOfDay(block.start)}
                                    onChange={(e) =>
                                      setBlocks(
                                        blocks.map((b, n) =>
                                          n === index
                                            ? { ...b, start: minutesOfDay(e.target.value) }
                                            : b,
                                        ),
                                      )
                                    }
                                  />
                                </label>
                                <label className="flex flex-col gap-2 text-sm">
                                  End (UTC)
                                  <select
                                    className={input}
                                    aria-label={`Time block ${index + 1} end mode`}
                                    value={block.end === 1440 ? "midnight" : "time"}
                                    onChange={(e) =>
                                      setBlocks(
                                        blocks.map((b, n) =>
                                          n === index
                                            ? {
                                                ...b,
                                                end:
                                                  e.target.value === "midnight"
                                                    ? 1440
                                                    : Math.min(1439, b.start + 60),
                                              }
                                            : b,
                                        ),
                                      )
                                    }
                                  >
                                    <option value="time">Choose a time</option>
                                    <option value="midnight">Midnight (end of day)</option>
                                  </select>
                                  {block.end !== 1440 && (
                                    <Input
                                      aria-label={`Time block ${index + 1} end time`}
                                      className={input}
                                      type="time"
                                      value={timeOfDay(block.end)}
                                      onChange={(e) =>
                                        setBlocks(
                                          blocks.map((b, n) =>
                                            n === index
                                              ? { ...b, end: minutesOfDay(e.target.value) }
                                              : b,
                                          ),
                                        )
                                      }
                                    />
                                  )}
                                </label>
                                <label className="flex items-center gap-2 text-sm">
                                  <Switch
                                    type="checkbox"
                                    checked={block.shuffle}
                                    onChange={(e) =>
                                      setBlocks(
                                        blocks.map((b, n) =>
                                          n === index ? { ...b, shuffle: e.target.checked } : b,
                                        ),
                                      )
                                    }
                                  />
                                  Shuffle block
                                </label>
                                <Button
                                  variant="outline"
                                  onClick={() => setBlocks(blocks.filter((_, n) => n !== index))}
                                >
                                  Remove block
                                </Button>
                              </div>
                              <ProgrammePicker
                                label={`Time block ${index + 1} programming`}
                                items={block.items}
                                onChange={(next) =>
                                  setBlocks(
                                    blocks.map((b, n) => (n === index ? { ...b, items: next } : b)),
                                  )
                                }
                              />
                            </div>
                          ))}
                        </section>
                        <details className="space-y-3">
                          <summary className="cursor-pointer text-sm font-medium">
                            Advanced schedule settings
                          </summary>
                          <div className="grid gap-4 sm:grid-cols-2">
                            <label className="flex flex-col gap-2 text-sm">
                              Schedule anchor
                              <Input
                                className={input}
                                type="datetime-local"
                                value={editing.epoch.slice(0, 16)}
                                onChange={(e) =>
                                  setEditing({ ...editing, epoch: `${e.target.value}:00Z` })
                                }
                              />
                              <span className="text-xs text-fg-muted">
                                UTC start time for the repeating programme order.
                              </span>
                            </label>
                            <label className="flex flex-col gap-2 text-sm">
                              Shuffle seed
                              <Input
                                className={input}
                                type="number"
                                value={editing.seed}
                                onChange={(e) =>
                                  setEditing({ ...editing, seed: Number(e.target.value) })
                                }
                              />
                            </label>
                          </div>
                        </details>
                      </>
                    )}
                  </>
                )}
                {channelTab === "viewers" && (
                  <section className="space-y-3">
                    <h3 className="text-sm font-medium">Channel viewers</h3>
                    <p className="text-xs text-fg-muted">
                      Leave all viewers unchecked to allow everyone with Live TV access. Viewer
                      parental limits still apply.
                    </p>
                    <div className="grid gap-2 sm:grid-cols-2">
                      {users.map((user) => (
                        <label key={user.id} className="flex items-center gap-2 text-sm">
                          <Switch
                            type="checkbox"
                            checked={(editing.allowedUsers || []).includes(user.id)}
                            onChange={(e) =>
                              setEditing({
                                ...editing,
                                allowedUsers: e.target.checked
                                  ? [...(editing.allowedUsers || []), user.id]
                                  : (editing.allowedUsers || []).filter((id) => id !== user.id),
                              })
                            }
                          />
                          {user.title || user.username}
                        </label>
                      ))}
                    </div>
                  </section>
                )}
                <div className="flex flex-wrap gap-2">
                  {editing.id && (
                    <Button
                      variant="outline"
                      disabled={busy}
                      onClick={() =>
                        void action(async () => {
                          await apiFetch(`/livetv/channels/${encodeURIComponent(editing.id)}`, {
                            method: "DELETE",
                          });
                          setEditing(null);
                          await refresh();
                        })
                      }
                    >
                      Delete channel
                    </Button>
                  )}
                  <Button onClick={() => void save()}>Save channel</Button>
                  {editing.kind === "virtual" && (
                    <Button
                      variant="outline"
                      onClick={() =>
                        void action(async () => {
                          setPreview(
                            await apiFetch<Programme[]>("/livetv/preview", {
                              method: "POST",
                              body: JSON.stringify(draft()),
                            }),
                          );
                        })
                      }
                    >
                      Preview six hours
                    </Button>
                  )}
                  <Button variant="outline" onClick={() => setEditing(null)}>
                    Cancel
                  </Button>
                </div>
                {preview.length > 0 && showGuide(preview)}
              </fieldset>
            </Drawer>
          )}
        </>
      )}
      {tab === "sources" && (
        <>
          <div className="flex justify-end">
            <Button
              onClick={() => {
                setSource({
                  id: "",
                  name: "",
                  playlistUrl: "",
                  guideUrl: "",
                  playlist: "",
                  guide: "",
                });
                setSourceOpen(true);
              }}
            >
              Import IPTV source
            </Button>
          </div>
          <div className="space-y-3">
            {sources.map((s) => (
              <div
                key={s.id}
                className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border p-4"
              >
                <div>
                  <h2 className="font-medium">{s.name}</h2>
                  <p className="text-xs text-fg-muted">
                    Updated {new Date(s.updatedAt).toLocaleString()}
                  </p>
                </div>
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy}
                    onClick={() =>
                      void action(async () => {
                        await apiFetch(`/livetv/sources/${s.id}/refresh`, { method: "POST" });
                        await refresh();
                        setMessage("Source refreshed; channel mappings and access rules retained.");
                      })
                    }
                  >
                    Refresh
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setSource({
                        ...s,
                        playlistUrl: s.playlistUrl || "",
                        guideUrl: s.guideUrl || "",
                        playlist: "",
                        guide: "",
                      });
                      setSourceOpen(true);
                    }}
                  >
                    Edit
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy}
                    onClick={() =>
                      void action(async () => {
                        await apiFetch(`/livetv/sources/${s.id}`, { method: "DELETE" });
                        await refresh();
                      })
                    }
                  >
                    Delete source
                  </Button>
                </div>
              </div>
            ))}
          </div>
          <Drawer
            open={sourceOpen}
            onClose={() => setSourceOpen(false)}
            title={source.id ? "Edit IPTV source" : "Import IPTV source"}
            size="lg"
          >
            <fieldset
              disabled={busy}
              className="min-w-0 space-y-4 rounded-lg border border-border p-5"
            >
              <legend className="px-2 font-medium">
                {source.id ? "Replace source" : "Import your IPTV source"}
              </legend>
              {message && (
                <p role="status" className="text-sm text-danger">
                  {message}
                </p>
              )}
              <label className="flex flex-col gap-2 text-sm">
                Source name
                <Input
                  className={input}
                  value={source.name}
                  onChange={(e) => setSource({ ...source, name: e.target.value })}
                />
              </label>
              <div className="grid gap-4 sm:grid-cols-2">
                <label className="flex flex-col gap-2 text-sm">
                  M3U URL
                  <Input
                    className={input}
                    value={source.playlistUrl}
                    onChange={(e) =>
                      setSource({ ...source, playlistUrl: e.target.value, playlist: "" })
                    }
                  />
                </label>
                <label className="flex flex-col gap-2 text-sm">
                  XMLTV URL
                  <Input
                    className={input}
                    value={source.guideUrl}
                    onChange={(e) => setSource({ ...source, guideUrl: e.target.value, guide: "" })}
                  />
                </label>
                <label className="flex flex-col gap-2 text-sm">
                  Or upload M3U
                  <input
                    type="file"
                    accept=".m3u,.m3u8,text/plain"
                    onChange={(e) => void upload(e.target.files?.[0], "playlist")}
                  />
                  {source.playlist && (
                    <span className="text-xs text-fg-muted">Playlist file ready</span>
                  )}
                </label>
                <label className="flex flex-col gap-2 text-sm">
                  Or upload XMLTV
                  <input
                    type="file"
                    accept=".xml,application/xml,text/xml"
                    onChange={(e) => void upload(e.target.files?.[0], "guide")}
                  />
                  {source.guide && <span className="text-xs text-fg-muted">Guide file ready</span>}
                </label>
              </div>
              <Button
                disabled={!source.name || (!source.playlist && !source.playlistUrl)}
                onClick={() => void importSource()}
              >
                Import source and guide
              </Button>
            </fieldset>
          </Drawer>
        </>
      )}
      {tab === "guide" && (
        <>
          <div className="flex items-center justify-between">
            <p className="text-sm text-fg-muted">
              Next 24 hours, displayed in your local timezone.
            </p>
            <Button variant="outline" disabled={busy} onClick={() => void action(refresh)}>
              Refresh guide
            </Button>
          </div>
          {showGuide(guide)}
        </>
      )}
      {tab === "access" && (
        <div className="space-y-4 rounded-lg border border-border p-5">
          <label className="flex flex-col gap-2 text-sm">
            Viewer
            <select
              className={input}
              value={viewer}
              onChange={(e) => setViewer(Number(e.target.value))}
            >
              <option value={0}>Select a viewer</option>
              {users.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.title || u.username}
                </option>
              ))}
            </select>
          </label>
          {policy && (
            <EditPanel
              title="Live TV access"
              description="Choose allowed channels and additional parental limits for this viewer."
              action="Edit Live TV access"
            >
              <fieldset disabled={busy} className="min-w-0 space-y-4">
                <label className="flex items-center gap-2 text-sm">
                  <Switch
                    type="checkbox"
                    checked={policy.disabled}
                    onChange={(e) => setPolicy({ ...policy, disabled: e.target.checked })}
                  />
                  Disable Live TV for this viewer
                </label>
                <label className="flex flex-col gap-2 text-sm">
                  Additional maximum rating
                  <Input
                    className={input}
                    value={policy.maxRating || ""}
                    onChange={(e) => setPolicy({ ...policy, maxRating: e.target.value })}
                    placeholder="Blank inherits viewer’s existing parental limits"
                  />
                </label>
                <p className="text-xs text-fg-muted">
                  Select allowed channels; an empty selection permits all channels. Existing viewer
                  rating and section limits also apply to guide, tune, manifests and segments.
                </p>
                <div className="grid gap-2 sm:grid-cols-2">
                  {channels.map((c) => (
                    <label key={c.id} className="flex items-center gap-2 text-sm">
                      <Switch
                        type="checkbox"
                        checked={(policy.channels || []).includes(c.id)}
                        onChange={(e) =>
                          setPolicy({
                            ...policy,
                            channels: e.target.checked
                              ? [...(policy.channels || []), c.id]
                              : (policy.channels || []).filter((id) => id !== c.id),
                          })
                        }
                      />
                      {c.number} · {c.name}
                    </label>
                  ))}
                </div>
                <Button
                  onClick={() =>
                    void action(async () => {
                      await apiFetch(`/livetv/policies/${viewer}`, {
                        method: "PUT",
                        body: JSON.stringify(policy),
                      });
                      setMessage("Live TV access saved.");
                    })
                  }
                >
                  Save viewer access
                </Button>
              </fieldset>
            </EditPanel>
          )}
        </div>
      )}
    </PageContainer>
  );
}
