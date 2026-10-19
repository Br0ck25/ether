import { Drawer } from "../components/Drawer";
import {
  Button,
  Input,
  PageContainer,
  PageHeader,
  Spinner,
  toast,
} from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { api, apiFetch, errorMessage } from "../lib/api";
import type { TitleCard } from "../lib/contract";
import type { EtherUser } from "../lib/types";
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
const select = "block w-full rounded-md border border-border bg-surface px-3 py-2 text-sm";
const labels: Record<number, string> = {
  1: "Original air date",
  2: "Absolute",
  3: "DVD",
  4: "Digital",
  5: "Story arc",
  6: "Production",
  7: "TV",
};
export default function Metadata() {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<TitleCard[]>([]);
  const [show, setShow] = useState<TitleCard | null>(null);
  const [users, setUsers] = useState<EtherUser[]>([]);
  const [viewer, setViewer] = useState(0);
  const [groups, setGroups] = useState<Group[]>([]);
  const [pref, setPref] = useState<Preference>({ groupId: "", specials: "last" });
  const [episodes, setEpisodes] = useState<Episode[]>([]);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const path = show ? `/metadata/shows/${show.key}/users/${viewer}` : "";
  const perform = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    void api
      .users()
      .then(setUsers)
      .catch((e) => toast.error(errorMessage(e)));
  }, []);
  useEffect(() => {
    if (!show) return;
    const abort = new AbortController();
    setLoading(true);
    setError("");
    setEpisodes([]);
    void Promise.all([
      apiFetch<Group[]>(`/metadata/shows/${show.key}/groups`, { signal: abort.signal }).then(
        setGroups,
      ),
      apiFetch<Preference>(path, { signal: abort.signal }).then(setPref),
    ])
      .catch((e) => {
        if (!abort.signal.aborted) setError(errorMessage(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => abort.abort();
  }, [show, viewer, path]);
  return (
    <PageContainer className="space-y-6">
      <PageHeader
        title="Metadata"
        description="Choose how Plex presents each show's episodes while preserving playback and watch history."
      />
      <section className="space-y-4 rounded-xl border border-border bg-surface p-5">
        <h2 className="text-lg font-semibold">Find a show</h2>
        <div className="flex gap-2">
          <Input
            aria-label="Find a show"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Show title"
            onKeyDown={(e) => {
              if (e.key === "Enter" && query.trim())
                void perform(async () => setResults(await api.searchCatalog(query, "show", 15)));
            }}
          />
          <Button
            disabled={busy || !query.trim()}
            onClick={() =>
              void perform(async () => setResults(await api.searchCatalog(query, "show", 15)))
            }
          >
            Search
          </Button>
        </div>
        <div className="flex flex-wrap gap-2">
          {results.map((item) => (
            <Button
              key={item.key}
              variant={show?.key === item.key ? "default" : "outline"}
              onClick={() => setShow(item)}
            >
              {item.title}
              {item.year ? ` (${item.year})` : ""}
            </Button>
          ))}
        </div>
        {results.length === 0 && query && (
          <p className="text-sm text-fg-muted">Search the catalogue to choose a show.</p>
        )}
      </section>
      <Drawer
        open={show !== null}
        onClose={() => setShow(null)}
        title={show?.title ?? "Episode ordering"}
        description="Episode order for this show"
        size="lg"
      >
        {show && (
          <section className="space-y-4 rounded-xl border border-border bg-surface p-5">
            <h2 className="text-lg font-semibold">{show.title}</h2>
            <label className="block text-sm">
              Apply to
              <select
                className={select}
                value={viewer}
                onChange={(e) => setViewer(Number(e.target.value))}
              >
                <option value={0}>Owner default for all viewers</option>
                {users.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.title || user.username}
                  </option>
                ))}
              </select>
            </label>
            {loading ? (
              <Spinner label="Loading episode orders" />
            ) : error ? (
              <p role="alert" className="text-sm text-danger">
                {error}
              </p>
            ) : (
              <>
                <label className="block text-sm">
                  Episode order
                  <select
                    className={select}
                    value={pref.groupId}
                    onChange={(e) => setPref({ ...pref, groupId: e.target.value })}
                  >
                    <option value="">TMDB aired order</option>
                    {groups.map((group) => (
                      <option key={group.id} value={group.id}>
                        {group.name} · {labels[group.type] || "Alternate"} · {group.episode_count}{" "}
                        episodes
                      </option>
                    ))}
                  </select>
                </label>
                {pref.inherited && (
                  <p className="text-sm text-fg-muted">
                    This viewer currently follows the owner default. Saving creates a viewer
                    override.
                  </p>
                )}
                {groups.length === 0 && (
                  <p className="text-sm text-fg-muted">
                    TMDB has no alternate episode groups for this show.
                  </p>
                )}
                {groups.find((g) => g.id === pref.groupId)?.description && (
                  <p className="text-sm text-fg-muted">
                    {groups.find((g) => g.id === pref.groupId)?.description}
                  </p>
                )}
                <label className="block text-sm">
                  Specials outside the selected groups
                  <select
                    className={select}
                    value={pref.specials}
                    onChange={(e) => setPref({ ...pref, specials: e.target.value })}
                  >
                    <option value="last">After regular episodes</option>
                    <option value="first">Before regular episodes</option>
                    <option value="hidden">Hide specials</option>
                  </select>
                </label>
                <p className="text-sm text-fg-muted">
                  Specials explicitly placed in a group keep that position. Hide specials excludes
                  every season 0 episode from browse lists. Episodes absent from an alternate order
                  stay available under Other episodes. Release matching always uses the original
                  TMDB season and episode.
                </p>
                <div className="flex flex-wrap gap-2">
                  <Button
                    disabled={busy}
                    onClick={() =>
                      void perform(async () => {
                        setPref(
                          await apiFetch<Preference>(path, {
                            method: "PUT",
                            body: JSON.stringify(pref),
                          }),
                        );
                        setEpisodes(await apiFetch<Episode[]>(`${path}/preview`));
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
                      void perform(async () =>
                        setEpisodes(await apiFetch<Episode[]>(`${path}/preview`)),
                      )
                    }
                  >
                    Preview saved order
                  </Button>
                  <Button
                    variant="outline"
                    disabled={busy}
                    onClick={() =>
                      void perform(async () => {
                        setPref(await apiFetch<Preference>(path, { method: "DELETE" }));
                        setEpisodes([]);
                        toast.success(
                          viewer ? "Following owner default" : "Default reset to aired order",
                        );
                      })
                    }
                  >
                    {viewer ? "Follow owner default" : "Reset default"}
                  </Button>
                </div>
                {episodes.length > 0 && (
                  <div className="max-h-96 overflow-auto">
                    <table className="w-full text-left text-sm">
                      <thead>
                        <tr>
                          <th className="py-2">Group</th>
                          <th>Episode</th>
                          <th>Canonical identity</th>
                        </tr>
                      </thead>
                      <tbody>
                        {episodes.map((ep) => (
                          <tr className="border-t border-border" key={ep.key}>
                            <td className="py-2">
                              {ep.group} · {ep.index}
                            </td>
                            <td>{ep.title}</td>
                            <td>
                              S{ep.canonicalSeason} E{ep.canonicalEpisode}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </>
            )}
          </section>
        )}
      </Drawer>
    </PageContainer>
  );
}
