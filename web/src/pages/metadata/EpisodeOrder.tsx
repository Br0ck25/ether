import { Badge, Button, Input, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { Search, Tv, ArrowRight } from "lucide-react";
import { useEffect, useState } from "react";
import { Artwork } from "../../components/Artwork";
import { MetadataWorkspace } from "./MetadataWorkspace";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel } from "../../components/feedback";
import { Section } from "../../components/layout";
import { api, apiFetch, errorMessage } from "../../lib/api";
import type { TitleCard } from "../../lib/contract";
import type { EtherUser } from "../../lib/types";
import { useAsync } from "../../lib/hooks";
import { nativeSelect } from "./shared";

type Group = {
  id: string;
  name: string;
  type: number;
  description: string;
  episode_count: number;
  group_count: number;
};
type Preference = { groupId: string; specials: string; inherited?: boolean };
type Episode = {
  key: string;
  title: string;
  canonicalSeason: number;
  canonicalEpisode: number;
  group: string;
  index: number;
};
const KIND: Record<number, string> = {
  1: "Original air date",
  2: "Absolute",
  3: "DVD",
  4: "Digital",
  5: "Story arc",
  6: "Production",
  7: "TV",
};
const viewerLabel = (u: EtherUser) => u.title || u.username;

/** Find a show, then edit its episode order (a record) in a drawer. */
export function EpisodeOrders() {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<TitleCard[] | null>(null);
  const [show, setShow] = useState<TitleCard | null>(null);
  const [busy, setBusy] = useState(false);
  const search = async () => {
    if (!query.trim()) return;
    setBusy(true);
    try {
      setResults(await api.searchCatalog(query, "show", 15));
    } catch (e) {
      toast.error("Search failed", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <MetadataWorkspace
        aside={
          <>
            <Section
              title="Episode order"
              description="Present a show in DVD, absolute or story order in Plex. Playback, release matching and watch history always use TMDB's original numbering."
            >
              <form
                className="flex max-w-xl gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  void search();
                }}
              >
                <Input
                  aria-label="Find a show"
                  value={query}
                  onChange={(e) => setQuery(e.currentTarget.value)}
                  placeholder="Show title"
                />
                <Button type="submit" variant="outline" disabled={busy || !query.trim()}>
                  <Search size={14} /> Search
                </Button>
              </form>
            </Section>
            <div className="metadata-note">
              <Tv size={17} />
              <p>
                Orders change how episodes appear in Plex. Watch history and playback keep their
                original identities.
              </p>
            </div>
          </>
        }
        main={
          results === null ? (
            <div className="metadata-empty">
              <Tv size={24} />
              <h2>Select a show</h2>
              <p>Search for a series to manage its default episode order and viewer overrides.</p>
              <div className="metadata-empty-options">
                <span>Aired order</span>
                <span>DVD order</span>
                <span>Story order</span>
                <span>Viewer overrides</span>
              </div>
            </div>
          ) : (
            <div>
              <div className="metadata-preview-toolbar">
                <div>
                  <h2>{results.length} series found</h2>
                </div>
              </div>
              {results.length ? (
                <div className="grid gap-3 sm:grid-cols-2">
                  {results.map((item) => (
                    <button
                      type="button"
                      key={item.key}
                      aria-label={`${item.title}${item.year ? ` (${item.year})` : ""}`}
                      onClick={() => setShow(item)}
                      className="flex items-center gap-4 rounded-xl border border-border bg-surface p-3 text-left hover:bg-surface-hover"
                    >
                      <Artwork
                        src={item.poster}
                        alt=""
                        title={item.title}
                        className="aspect-[2/3] w-16 shrink-0 rounded-md"
                      />
                      <span className="min-w-0 flex-1">
                        <strong className="block text-sm font-medium">{item.title}</strong>
                        <span className="text-xs text-fg-muted">{item.year || "TV series"}</span>
                        <span className="mt-2 flex items-center gap-1 text-xs text-fg-muted">
                          Explore orders <ArrowRight size={12} />
                        </span>
                      </span>
                    </button>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-fg-muted">No shows match “{query}”.</p>
              )}
            </div>
          )
        }
      />
      <ShowOrderDrawer show={show} onClose={() => setShow(null)} />
    </>
  );
}

function ShowOrderDrawer({ show, onClose }: { show: TitleCard | null; onClose: () => void }) {
  const key = show?.key ?? null;
  const groups = useAsync(
    (signal) =>
      key ? apiFetch<Group[]>(`/metadata/shows/${key}/groups`, { signal }) : Promise.resolve([]),
    [key],
  );
  const users = useAsync(() => api.users(), []);
  const [viewer, setViewer] = useState<EtherUser | null>(null);
  // Bumped after any save so the override badges re-read.
  const [version, setVersion] = useState(0);
  useEffect(() => setViewer(null), [key]);
  return (
    <Drawer
      open={show !== null}
      onClose={onClose}
      title={show?.title ?? "Episode order"}
      description="Episode order for this show"
      size="lg"
    >
      {!show ? null : groups.error ? (
        <ErrorPanel error={groups.error} onRetry={() => void groups.reload()} />
      ) : groups.loading && !groups.data ? (
        <div className="py-10 text-center">
          <Spinner size={20} label="Loading episode orders" />
        </div>
      ) : (
        <div className="space-y-6">
          <section className="space-y-4" aria-label="Default order">
            <div>
              <h3 className="text-sm font-semibold text-fg">Default for every viewer</h3>
            </div>
            <OrderEditor
              showKey={show.key}
              viewerId={0}
              groups={groups.data ?? []}
              onChanged={() => setVersion((v) => v + 1)}
            />
          </section>
          <section className="space-y-2 border-t border-border pt-5" aria-label="Viewer overrides">
            <div>
              <h3 className="text-sm font-semibold text-fg">Viewer overrides</h3>
              <p className="text-xs text-fg-muted">
                Give one viewer a different order for this show.
              </p>
            </div>
            {users.error ? (
              <ErrorPanel error={users.error} />
            ) : !users.data ? (
              <Spinner size={16} label="Loading viewers" />
            ) : (
              <ul className="divide-y divide-border rounded-lg border border-border">
                {users.data.map((u) => (
                  <ViewerOverrideRow
                    key={u.id}
                    showKey={show.key}
                    user={u}
                    version={version}
                    onEdit={() => setViewer(u)}
                  />
                ))}
              </ul>
            )}
          </section>
        </div>
      )}
      <Drawer
        open={viewer !== null}
        onClose={() => setViewer(null)}
        title={viewer ? `${show?.title ?? "Show"} for ${viewerLabel(viewer)}` : "Viewer order"}
        description="This viewer's episode order for the show"
        size="lg"
      >
        {show && viewer ? (
          <OrderEditor
            showKey={show.key}
            viewerId={viewer.id}
            groups={groups.data ?? []}
            onChanged={() => setVersion((v) => v + 1)}
          />
        ) : null}
      </Drawer>
    </Drawer>
  );
}

function ViewerOverrideRow({
  showKey,
  user,
  version,
  onEdit,
}: {
  showKey: string;
  user: EtherUser;
  version: number;
  onEdit: () => void;
}) {
  const pref = useAsync(
    (signal) => apiFetch<Preference>(`/metadata/shows/${showKey}/users/${user.id}`, { signal }),
    [showKey, user.id, version],
  );
  const custom = pref.data ? !pref.data.inherited : null;
  return (
    <li className="flex items-center justify-between gap-3 px-3 py-2">
      <span className="min-w-0 truncate text-sm text-fg">{viewerLabel(user)}</span>
      <span className="flex shrink-0 items-center gap-2">
        {custom === null ? null : custom ? (
          <Badge variant="info">Custom for this viewer</Badge>
        ) : (
          <Badge variant="secondary">Uses default</Badge>
        )}
        <Button
          variant="outline"
          size="sm"
          aria-label={`Edit order for ${viewerLabel(user)}`}
          onClick={onEdit}
        >
          {custom ? "Edit" : "Customise"}
        </Button>
      </span>
    </li>
  );
}

/** One scope's order: the owner default (viewer 0) or a single viewer's override. */
function OrderEditor({
  showKey,
  viewerId,
  groups,
  onChanged,
}: {
  showKey: string;
  viewerId: number;
  groups: Group[];
  onChanged: () => void;
}) {
  const path = `/metadata/shows/${showKey}/users/${viewerId}`;
  const saved = useAsync((signal) => apiFetch<Preference>(path, { signal }), [path]);
  const [pref, setPref] = useState<Preference>({ groupId: "", specials: "last" });
  const [episodes, setEpisodes] = useState<Episode[]>([]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (saved.data) setPref(saved.data);
  }, [saved.data]);
  const perform = async (fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  if (saved.error) return <ErrorPanel error={saved.error} onRetry={() => void saved.reload()} />;
  if (!saved.data)
    return (
      <div className="py-6 text-center">
        <Spinner size={18} label="Loading episode order" />
      </div>
    );
  const scope = viewerId ? `order-${viewerId}` : "order-default";
  const chosen = groups.find((g) => g.id === pref.groupId);
  const inherited = viewerId !== 0 && saved.data.inherited;
  return (
    <div className="space-y-4">
      {inherited ? (
        <p className="text-sm text-fg-muted">
          This viewer uses the default order. Saving makes a custom order for them.
        </p>
      ) : null}
      <div className="space-y-1.5">
        <label htmlFor={`${scope}-group`} className="block text-sm font-medium text-fg">
          Episode order
        </label>
        <select
          id={`${scope}-group`}
          className={nativeSelect}
          value={pref.groupId}
          onChange={(e) => setPref({ ...pref, groupId: e.currentTarget.value })}
        >
          <option value="">TMDB aired order</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name} · {KIND[g.type] || "Alternate"} · {g.episode_count} episodes
            </option>
          ))}
        </select>
        {groups.length === 0 ? (
          <p className="text-xs text-fg-muted">
            TMDB has no alternate episode orders for this show.
          </p>
        ) : chosen?.description ? (
          <p className="text-xs text-fg-muted">{chosen.description}</p>
        ) : null}
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${scope}-specials`} className="block text-sm font-medium text-fg">
          Specials not in this order
        </label>
        <select
          id={`${scope}-specials`}
          className={nativeSelect}
          value={pref.specials}
          onChange={(e) => setPref({ ...pref, specials: e.currentTarget.value })}
        >
          <option value="last">After regular episodes</option>
          <option value="first">Before regular episodes</option>
          <option value="hidden">Hide specials</option>
        </select>
        <p className="text-xs text-fg-muted">
          Specials the order places keep their spot. Hiding specials removes every season 0 episode
          from browse lists. Episodes the order leaves out appear under Other episodes.
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          disabled={busy}
          onClick={() =>
            void perform(async () => {
              const next = await apiFetch<Preference>(path, {
                method: "PUT",
                body: JSON.stringify({ groupId: pref.groupId, specials: pref.specials }),
              });
              saved.setData(next);
              setEpisodes(await apiFetch<Episode[]>(`${path}/preview`));
              onChanged();
              toast.success("Episode order saved");
            })
          }
        >
          Save episode order
        </Button>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() =>
            void perform(async () => setEpisodes(await apiFetch<Episode[]>(`${path}/preview`)))
          }
        >
          Preview saved order
        </Button>
        {viewerId === 0 || !inherited ? (
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() =>
              void perform(async () => {
                saved.setData(await apiFetch<Preference>(path, { method: "DELETE" }));
                setEpisodes([]);
                onChanged();
                toast.success(viewerId ? "Uses the default again" : "Default reset to aired order");
              })
            }
          >
            {viewerId ? "Use default" : "Reset to aired order"}
          </Button>
        ) : null}
      </div>
      {episodes.length > 0 ? (
        <div className="max-h-96 overflow-auto rounded-lg border border-border">
          <table className="w-full text-left text-sm">
            <thead className="sticky top-0 bg-bg-subtle text-xs text-fg-muted">
              <tr>
                <th className="px-3 py-2 font-medium">Position</th>
                <th className="px-3 py-2 font-medium">Episode</th>
                <th className="px-3 py-2 font-medium">Original number</th>
              </tr>
            </thead>
            <tbody>
              {episodes.map((ep) => (
                <tr className="border-t border-border" key={ep.key}>
                  <td className="px-3 py-2">
                    {ep.group} · {ep.index}
                  </td>
                  <td className="px-3 py-2">{ep.title}</td>
                  <td className="px-3 py-2 tabular-nums">
                    S{ep.canonicalSeason} E{ep.canonicalEpisode}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}
