/**
 * Every owner destination in one place. The sidebar and ⌘K search read this,
 * so a section only ever has one home.
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
    href: "/cache",
    title: "Cache",
    sections: [
      { key: "overview", title: "Overview", keywords: "storage readiness policy" },
      { key: "explorer", title: "Explorer", keywords: "retained media releases pins" },
      { key: "preloading", title: "Preloading", keywords: "household selection popular movies tv" },
    ],
    keywords: "disk budget storage retention prefetch warming next episode",
  },
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
      {
        key: "defaults",
        title: "Defaults",
        keywords: "audio subtitles language watchlist alerts policy",
      },
    ],
  },
  {
    href: "/home",
    title: "Home",
    sections: [],
    keywords: "designer rows layout overrides plex home",
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
    href: "/catalog",
    title: "Catalog",
    sections: [
      { key: "settings", title: "Catalog settings", keywords: "tmdb language region popularity" },
    ],
    keywords: "catalogue movies shows browse search library",
  },
  {
    href: "/metadata",
    title: "Metadata",
    sections: [
      { key: "ratings", title: "Ratings", keywords: "omdb mdblist imdb rotten badges" },
      { key: "artwork", title: "Artwork", keywords: "posters overlays backdrop" },
      { key: "episodes", title: "Episode order", keywords: "dvd absolute aired ordering" },
    ],
  },
  {
    href: "/sources",
    title: "Sources",
    sections: [
      { key: "usenet", title: "Usenet providers", keywords: "nntp backfill connections" },
      {
        key: "indexers",
        title: "Indexers",
        keywords:
          "newznab torznab torrent built-in sites definitions cardigann flaresolverr prowlarr stremio addon budget api",
      },
      {
        key: "debrid",
        title: "Debrid",
        keywords: "real-debrid realdebrid torbox alldebrid premiumize torrent cached",
      },
      {
        key: "availability",
        title: "Availability",
        keywords: "release feeds rss source evidence checks budget",
      },
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
      {
        key: "pinned",
        title: "Pinned",
        keywords: "pin release override search interactive manual pick find",
      },
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
    href: "/server",
    title: "General",
    sections: [
      { key: "server", title: "Server", keywords: "name port listen log level stream limit" },
      { key: "downloads", title: "Downloads", keywords: "offline sync friends remux" },
      {
        key: "personalize",
        title: "Personalization",
        keywords: "post-play up next match finish time clock",
      },
      { key: "extras", title: "Trailers & extras", keywords: "cinema trailers yt-dlp resolver" },
    ],
    keywords: "settings",
  },
  {
    href: "/remote-access",
    title: "Connectivity",
    sections: [
      { key: "status", title: "Plex status", keywords: "claim connection checks reachability" },
      { key: "remote", title: "Remote access", keywords: "upnp port forward public" },
      { key: "secure", title: "Secure connections", keywords: "certificate plex.direct tls" },
      { key: "account", title: "Plex account", keywords: "owner token claimed" },
      { key: "compatibility", title: "Compatibility", keywords: "pms version" },
    ],
    keywords: "settings plex",
  },
  {
    href: "/services",
    title: "Integrations",
    sections: [
      { key: "overview", title: "Integrations", keywords: "status tmdb debrid prowlarr ratings" },
      { key: "trakt", title: "Trakt", keywords: "sync history" },
      { key: "llm", title: "Language model", keywords: "llm ollama openai recaps search" },
      { key: "opensubtitles", title: "OpenSubtitles", keywords: "subtitles" },
    ],
    keywords: "settings integrations",
  },
  {
    href: "/cluster",
    title: "Cluster",
    sections: [],
    keywords: "high availability ha nodes failover postgres postgresql join token weight leader",
  },
  { href: "/maintenance", title: "Maintenance", sections: [], keywords: "server maintenance" },
  {
    href: "/maintenance/backups",
    title: "Backups & import",
    sections: [
      { key: "backups", title: "Backups", keywords: "snapshot restore schedule" },
      { key: "import", title: "Import & export", keywords: "migrate plex csv letterboxd trakt" },
    ],
  },
  {
    href: "/maintenance/updates",
    title: "Updates",
    sections: [],
    keywords: "version channel beta maintenance window update now install",
  },
  {
    href: "/maintenance/troubleshooting",
    title: "Troubleshooting",
    sections: [],
    keywords: "local file test playback",
  },
  { href: "/maintenance/logs", title: "Logs", sections: [], keywords: "errors debug" },
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
