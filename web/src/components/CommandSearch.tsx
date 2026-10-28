import {
  GlobalSearch,
  type GlobalSearchGroup,
  type GlobalSearchResult,
} from "@braedonsaunders/appkit-ui";
import { useCallback } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import { destinations } from "../lib/sitemap";
import { loadViewers, viewerName } from "./pickers";

const ACTIONS = [
  { title: "Add an indexer", href: "/sources/indexers", keywords: "newznab prowlarr" },
  { title: "Add a Usenet provider", href: "/sources/usenet", keywords: "nntp" },
  {
    title: "Add a debrid provider",
    href: "/sources/debrid",
    keywords: "real-debrid torbox alldebrid premiumize debrid-link debridlink offcloud torrent",
  },
  { title: "New collection", href: "/collections/new", keywords: "create" },
  { title: "New notification channel", href: "/notifications/channels", keywords: "alert" },
  { title: "Back up now", href: "/maintenance/backups", keywords: "backup snapshot" },
];

function matches(q: string, ...fields: (string | undefined)[]): boolean {
  const hay = fields.filter(Boolean).join(" ").toLowerCase();
  return q
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => hay.includes(w));
}

/** ⌘K: jump to any page, section, viewer or collection, or start a common task. */
export function CommandSearch() {
  const navigate = useNavigate();
  const search = useCallback(
    async (q: string, signal: AbortSignal): Promise<GlobalSearchResult> => {
      const groups: GlobalSearchGroup[] = [];
      const places = destinations()
        .filter((d) => matches(q, d.title, d.page, d.keywords))
        .slice(0, 8)
        .map((d) => ({
          id: d.href,
          type: "page",
          title: d.title,
          subtitle: d.page === "Page" ? undefined : d.page,
          href: d.href,
        }));
      if (places.length) groups.push({ id: "places", label: "Go to", hits: places });
      const actions = ACTIONS.filter((a) => matches(q, a.title, a.keywords)).map((a) => ({
        id: a.title,
        type: "action",
        title: a.title,
        href: a.href,
      }));
      if (actions.length) groups.push({ id: "actions", label: "Actions", hits: actions });
      const [viewers, collections] = await Promise.all([
        loadViewers().catch(() => []),
        api.collections().catch(() => []),
      ]);
      if (signal.aborted) return { groups: [], total: 0 };
      const people = viewers
        .filter((v) => matches(q, viewerName(v), v.username))
        .slice(0, 5)
        .map((v) => ({
          id: String(v.id),
          type: "viewer",
          title: viewerName(v),
          subtitle: v.isOwner ? "Owner" : "Viewer",
          href: `/viewers/${v.id}`,
        }));
      if (people.length) groups.push({ id: "viewers", label: "Viewers", hits: people });
      const cols = (Array.isArray(collections) ? collections : [])
        .filter((c) => c.id && matches(q, c.name))
        .slice(0, 5)
        .map((c) => ({
          id: c.id!,
          type: "collection",
          title: c.name,
          subtitle: "Collection",
          href: `/collections/${encodeURIComponent(c.id!)}`,
        }));
      if (cols.length) groups.push({ id: "collections", label: "Collections", hits: cols });
      return { groups, total: groups.reduce((n, g) => n + g.hits.length, 0) };
    },
    [],
  );
  return (
    <GlobalSearch
      className="w-full max-w-md"
      search={search}
      minimumQueryLength={1}
      onNavigate={(hit) => navigate(hit.href)}
      labels={{
        placeholder: "Search or jump to…",
        ariaLabel: "Search Ether",
        clear: "Clear search",
        searching: "Searching…",
        noMatches: (query) => `Nothing matches “${query}”`,
        navigate: "navigate",
        open: "open",
        close: "close",
        resultCount: (count) => `${count} result${count === 1 ? "" : "s"}`,
      }}
    />
  );
}
