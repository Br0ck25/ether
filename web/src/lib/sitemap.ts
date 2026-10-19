/**
 * Every owner destination in one place. The sidebar, ⌘K search and legacy
 * redirects all read this, so a section only ever has one home.
 */

export interface Destination {
  href: string;
  title: string;
  /** Page the destination belongs to, shown as the search subtitle. */
  page: string;
  keywords?: string;
}

export interface PageDef {
  href: string;
  title: string;
  /** In-page sections (tabs) in display order. */
  sections: { key: string; title: string; keywords?: string }[];
  keywords?: string;
}

export const PAGES: PageDef[] = [
  {
    href: "/",
    title: "Overview",
    sections: [],
    keywords: "home dashboard health attention status",
  },
  {
    href: "/activity",
    title: "Activity",
    sections: [
      { key: "now", title: "Now playing", keywords: "streams sessions stop live" },
      { key: "history", title: "History", keywords: "plays watched" },
      { key: "stats", title: "Stats", keywords: "statistics devices hours viewers titles" },
    ],
  },
  {
    href: "/viewers",
    title: "Viewers",
    sections: [
      { key: "people", title: "People", keywords: "users friends accounts" },
      { key: "invitations", title: "Invitations", keywords: "invite friend share" },
      {
        key: "defaults",
        title: "Defaults",
        keywords: "audio subtitles language watchlist alerts policy",
      },
    ],
  },
  {
    href: "/home-screens",
    title: "Home screens",
    sections: [],
    keywords: "designer rows layout overrides clean home plex home",
  },
  { href: "/collections", title: "Collections", sections: [], keywords: "studio templates rows" },
  {
    href: "/livetv",
    title: "Live TV",
    sections: [
      { key: "channels", title: "Channels", keywords: "virtual channel" },
      { key: "sources", title: "IPTV sources", keywords: "m3u xmltv playlist" },
      { key: "guide", title: "Guide", keywords: "epg schedule" },
    ],
  },
  {
    href: "/metadata",
    title: "Metadata",
    sections: [
      { key: "ratings", title: "Ratings", keywords: "omdb mdblist imdb rotten badges" },
      { key: "artwork", title: "Artwork", keywords: "posters overlays backdrop" },
      { key: "episodes", title: "Episode order", keywords: "dvd absolute aired ordering" },
      { key: "catalogue", title: "Catalogue", keywords: "tmdb language region popularity" },
    ],
  },
  {
    href: "/sources",
    title: "Sources",
    sections: [
      { key: "usenet", title: "Usenet providers", keywords: "nntp backfill connections" },
      { key: "indexers", title: "Indexers", keywords: "newznab prowlarr budget api" },
      { key: "usage", title: "Usage & costs", keywords: "quota price plan cost" },
    ],
  },
  {
    href: "/releases",
    title: "Releases",
    sections: [
      { key: "quality", title: "Quality profiles", keywords: "resolution 4k 1080p remote" },
      { key: "rules", title: "Client rules", keywords: "device profile" },
      { key: "selection", title: "Selection rules", keywords: "cam health blocked terms groups" },
      { key: "scoring", title: "Custom scoring", keywords: "trash custom formats" },
      { key: "startup", title: "Playback start", keywords: "startup race warm" },
      { key: "search", title: "Search", keywords: "interactive search manual release pick" },
      { key: "pinned", title: "Pinned", keywords: "pin release override" },
      { key: "blocklist", title: "Blocklist", keywords: "blocked bad release unblock" },
      { key: "history", title: "History", keywords: "traces resolver why release debug decisions" },
    ],
  },
  {
    href: "/playback",
    title: "Playback",
    sections: [
      { key: "transcoding", title: "Transcoding", keywords: "ffmpeg hardware gpu bitrate" },
      { key: "devices", title: "Device capabilities", keywords: "compatibility codecs" },
      { key: "cache", title: "Cache & prefetch", keywords: "disk budget next episode" },
      { key: "markers", title: "Markers", keywords: "intro credits skip chapters" },
      { key: "subtitles", title: "Subtitles", keywords: "timing sync opensubtitles" },
    ],
  },
  {
    href: "/notifications",
    title: "Notifications",
    sections: [
      { key: "channels", title: "Channels", keywords: "email discord webhook push alerts" },
      { key: "report", title: "Weekly report", keywords: "newsletter digest" },
      { key: "deliveries", title: "Delivery history" },
    ],
  },
  {
    href: "/settings",
    title: "Settings",
    sections: [
      { key: "server", title: "General", keywords: "name port log level stream limit" },
      { key: "plex", title: "Plex & remote access", keywords: "claim remote upnp certificate" },
      { key: "services", title: "Connected services", keywords: "trakt llm openai opensubtitles" },
      { key: "backups", title: "Backups & import", keywords: "restore migrate csv letterboxd" },
      { key: "updates", title: "Updates", keywords: "channel beta" },
      { key: "troubleshooting", title: "Troubleshooting", keywords: "local file test" },
    ],
  },
  { href: "/logs", title: "Logs", sections: [], keywords: "errors debug" },
];

export function destinations(): Destination[] {
  return PAGES.flatMap((p) => [
    { href: p.href, title: p.title, page: "Page", keywords: p.keywords },
    ...p.sections.map((s) => ({
      href: `${p.href}/${s.key}`,
      title: s.title,
      page: p.title,
      keywords: s.keywords,
    })),
  ]);
}

const PLAYBACK_POLICY_TAB: Record<string, string> = {
  profiles: "/releases/quality",
  defaults: "/releases/quality",
  rules: "/releases/rules",
  startup: "/releases/startup",
  scoring: "/releases/scoring",
  compatibility: "/playback/devices",
};

const LEGACY_SETTINGS: Record<string, string> = {
  users: "/viewers",
  usenet: "/sources/usenet",
  indexers: "/sources/indexers",
  cache: "/playback/cache",
  resolver: "/releases/selection",
  quality: "/releases/quality",
  transcode: "/playback/transcoding",
  catalog: "/metadata/catalogue",
  llm: "/settings/services",
  trakt: "/settings/services",
  opensubtitles: "/settings/services",
  markers: "/playback/markers",
  spike: "/settings/troubleshooting",
};

/** Where a pre-rework URL lives now, or null when it is still current. */
export function legacyRedirect(pathname: string, search: string): string | null {
  const tab = new URLSearchParams(search).get("tab");
  if (pathname === "/playback" && tab) return PLAYBACK_POLICY_TAB[tab] ?? "/releases/quality";
  const settings = /^\/settings\/([^/]+)$/.exec(pathname);
  if (settings) return LEGACY_SETTINGS[settings[1]!] ?? null;
  const fixed: Record<string, string> = {
    "/discovery": "/metadata/ratings",
    "/insights": "/activity",
    "/adoption": "/settings/backups",
    "/streams": "/viewers/defaults",
    "/safety": "/viewers",
    "/traces": "/releases/history",
    "/designer": "/home-screens",
  };
  return fixed[pathname] ?? null;
}
