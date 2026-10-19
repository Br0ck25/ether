/**
 * In-memory fake of the Ether admin API (docs/owner-ui.md plus the existing
 * endpoints). Used by `vite --mode mock` and by the tests; never shipped.
 * It evaluates collection rules against a generated catalogue and builds
 * context-aware Home rows so every screen behaves realistically offline.
 */
import type {
  ActivityDay, CollectionSpec, HomeRow, LayoutRule, Rule, Source, TasteOverride, TasteProfile, TitleCard, Viewer,
} from "../lib/contract";
import { previewLayout } from "../lib/layout";
import { normalizeSpec } from "../lib/rules";
import type { Bootstrap, EtherConfig, EtherUser, LogEntry } from "../lib/types";
import {
  COLLECTIONS, GENRES, KEYWORDS, NETWORKS, PEOPLE, PROVIDERS, STUDIOS, avatarFor, buildCatalog, card, hash, posterFor,
  type FakeTitle,
} from "./data";
import { TEMPLATES } from "./templates";

const NOW = () => new Date();

export interface FakeOptions {
  bootstrap?: Partial<Bootstrap>;
  /** Simulate an LLM being configured (collections/generate). */
  llm?: boolean;
  /** Artificial latency for realism in the browser; 0 in tests. */
  latencyMs?: number;
}

function json(body: unknown, status = 200): Response {
  return new Response(body === null ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
function err(status: number, code: string, message: string): Response {
  return json({ error: { code, message } }, status);
}

function defaultConfig(): EtherConfig {
  return {
    server: { port: 32400, bindAddress: "0.0.0.0", friendlyName: "Living Room Ether", machineIdentifier: "mock-machine", logLevel: "info", defaultStreamLimit: 2, maxStreams: 0 },
    plex: { token: "••••", clientIdentifier: "mock-client", ownerPlexId: 1, ownerUsername: "owner", claimed: true, remoteAccess: true, useUpnp: true, publicPort: 32400, publicHost: "", customUrls: null, certFile: "", keyFile: "", certHash: "abc", secureConnections: "preferred" },
    usenet: { providers: [
      { id: "p1", name: "Primary news", host: "news.example.com", port: 563, tls: true, username: "me", password: "••••", connections: 20, priority: 0, backfill: false, enabled: true },
      { id: "p2", name: "Block account", host: "block.example.net", port: 563, tls: true, username: "me", password: "••••", connections: 8, priority: 1, backfill: true, enabled: true },
    ] },
    indexers: [
      { id: "i1", name: "Indexer One", url: "https://indexer-one.example.com", apiKey: "••••", dailyLimit: 200, dailyGrabs: 50, priority: 0, enabled: true, movieCategories: null, tvCategories: null },
      { id: "i2", name: "Indexer Two", url: "https://indexer-two.example.com", apiKey: "••••", dailyLimit: 25, dailyGrabs: 10, priority: 1, enabled: true, movieCategories: null, tvCategories: null },
    ],
    cache: { dir: "/var/lib/ether/cache", budgetBytes: 200 * 1024 ** 3, prefetchNextEpisode: true, prefetchBytes: 64 * 1024 ** 2, keepInProgressWarm: true, nextEpisodeAt: 0.8 },
    catalog: { tmdbKey: "", language: "en-US", region: "US", minPopularity: 5, minVotes: 100, includeAdult: false, refreshInterval: "24h0m0s", showUnreleased: true },
    resolver: { allowCam: false, healthSamples: 12, searchCacheTtl: "6h0m0s", unavailableCooloff: "24h0m0s", maxCandidates: 3, blockedGroups: null, blockedTerms: null },
    quality: {
      profiles: [
        { name: "4K", maxHeight: 2160, minHeight: 1080, allowHdr: true, allowDolbyVision: true, preferredSources: ["remux", "bluray", "webdl"], preferredCodecs: ["hevc", "av1"], preferredAudio: ["truehd", "eac3"], minSizeMbPerMin: 15, maxSizeMbPerMin: 600, preferredLanguages: null, allowCam: false },
        { name: "1080p", maxHeight: 1080, minHeight: 720, allowHdr: false, allowDolbyVision: false, preferredSources: ["webdl", "bluray"], preferredCodecs: ["h264", "hevc"], preferredAudio: ["eac3", "ac3", "aac"], minSizeMbPerMin: 5, maxSizeMbPerMin: 150, preferredLanguages: null, allowCam: false },
      ],
      rules: [{ product: "Plex for Apple TV", profile: "4K" }],
      default: "1080p", remote: "1080p",
    },
    transcode: { ffmpegPath: "", ffprobePath: "", hwAccel: "auto", vaapiDevice: "", tempDir: "/tmp/ether-transcode", maxTranscodes: 2, remoteMaxBitrateKbps: 8000 },
    trakt: { enabled: false, clientId: "", clientSecret: "" },
    openSubtitles: { enabled: false, apiKey: "", username: "", password: "" },
    llm: { enabled: true, baseUrl: "http://localhost:11434/v1", apiKey: "", model: "llama3" },
    updates: { channel: "stable", autoUpdate: true, feedUrl: "" },
    spike: { enabled: false, localFiles: null },
    setupComplete: true,
  };
}

const VIEWERS: Viewer[] = [
  { id: 1, plexId: 1001, username: "owner", title: "Alex Rivera", isOwner: true, lastActive: "", plays30d: 42, hoursWatched30d: 61.5 },
  { id: 2, plexId: 1002, username: "sam", title: "Sam Rivera", isOwner: false, lastActive: "", plays30d: 27, hoursWatched30d: 33.2 },
  { id: 3, plexId: 1003, username: "kids", title: "The Kids", isOwner: false, lastActive: "", plays30d: 58, hoursWatched30d: 24.8 },
  { id: 4, plexId: 1004, username: "jordan", title: "Jordan Lee", isOwner: false, lastActive: "", plays30d: 6, hoursWatched30d: 9.1 },
];

const TASTE_SEEDS: Record<number, string[]> = {
  1: ["Science Fiction", "Thriller", "Mystery", "Crime"],
  2: ["Romance", "Comedy", "Drama", "Music"],
  3: ["Animation", "Family", "Adventure", "Fantasy"],
  4: ["Documentary", "History", "War"],
};

function decadeOf(year?: number): string { return year ? String(Math.floor(year / 10) * 10) : ""; }

function durationMs(w: string): number {
  const m = /^(\d+)([dwmy])$/.exec(w);
  if (!m) return 0;
  const n = Number(m[1]);
  return n * ({ d: 1, w: 7, m: 30, y: 365 }[m[2] as "d" | "w" | "m" | "y"]) * 86400000;
}

export function createFakeServer(opts: FakeOptions = {}) {
  const catalog = buildCatalog();
  const byKey = new Map(catalog.map((t) => [t.key, t]));
  const state = {
    boot: { ok: true, version: "0.9.0-mock", setupComplete: true, authenticated: true, authRequired: false, localTrusted: true, signIn: true, serverName: "Living Room Ether", ...opts.bootstrap } as Bootstrap,
    config: defaultConfig(),
    users: VIEWERS.map<EtherUser>((v) => ({ id: v.id, plexId: v.plexId, username: v.username, title: v.title, thumb: avatarFor(v.title), isOwner: v.isOwner, streamLimit: 0, maxContentRating: v.id === 3 ? "PG" : "", subtitleLanguages: [], audioLanguages: [], allowedSections: [] })),
    collections: [] as CollectionSpec[],
    overrides: new Map<number, TasteOverride[]>(),
    layout: [
      { id: "lr-1", scope: "all", section: "home", rows: [{ hubId: "continue-watching", action: "pin", position: 0 }] },
      { id: "lr-2", scope: { userIds: [3] }, section: "home", rows: [{ hubId: "short-on-time", action: "boost", when: { days: [1, 2, 3, 4, 5], fromHour: 16, toHour: 20 } }] },
    ] as LayoutRule[],
    logs: [] as LogEntry[],
    calls: [] as { method: string; path: string; body: unknown }[],
    llm: opts.llm ?? true,
  };
  let seq = 1;

  // Seed collections from templates so the gallery isn't empty.
  for (const id of ["mood-edge-of-seat", "trend-new-to-ether", "family-night", "short-weeknight"]) {
    const t = TEMPLATES.find((x) => x.id === id)!;
    const spec = normalizeSpec({ ...t.spec, id: `c${seq++}`, enabled: id !== "family-night", updatedAt: NOW().toISOString(), lastBuiltAt: NOW().toISOString() });
    spec.itemCount = evaluate(spec).total;
    state.collections.push(spec);
  }
  state.collections.push({ ...normalizeSpec({ name: "Recently added", description: "Plex's built-in recently added row.", sources: [{ kind: "catalog" }], sort: "added" }), id: "builtin-recent", builtIn: true, enabled: true, itemCount: 120 });

  const levels = ["debug", "info", "info", "info", "warn", "error"];
  for (let i = 0; i < 80; i++) {
    const lvl = levels[i % levels.length] as string;
    state.logs.push({ time: new Date(Date.now() - (80 - i) * 45000).toISOString(), level: lvl.toUpperCase(), message: lvl === "error" ? "segment fetch failed" : lvl === "warn" ? "indexer budget low" : "served hubs", attrs: { component: i % 2 ? "plex" : "resolver", user: "owner" } });
  }

  // ---- Collection evaluation ---------------------------------------------------

  function candidates(spec: CollectionSpec): FakeTitle[] {
    const media = (t: FakeTitle) => spec.mediaType === "mixed" || t.type === spec.mediaType;
    const out = new Map<string, FakeTitle>();
    for (const s of spec.sources) {
      for (const t of sourceTitles(s)) if (media(t) || s.kind === "titles") out.set(t.key, t);
    }
    return [...out.values()];
  }

  function sourceTitles(s: Source): FakeTitle[] {
    switch (s.kind) {
      case "catalog": return catalog;
      case "titles": return s.keys.map((k) => byKey.get(k)).filter((t): t is FakeTitle => Boolean(t));
      case "tmdbPerson": return catalog.filter((t) => t.people.includes(s.id));
      case "tmdbKeyword": return catalog.filter((t) => t.keywords.includes(s.id));
      case "tmdbCompany": return catalog.filter((t) => t.studio === s.id);
      case "tmdbNetwork": return catalog.filter((t) => t.network === s.id);
      case "tmdbCollection": return catalog.filter((t) => t.collectionId === s.id);
      case "watchProvider": return catalog.filter((t) => t.providers.includes(s.id));
      case "tmdbList": return [...catalog].sort((a, b) => (hash(s.list + a.key) % 1000) - (hash(s.list + b.key) % 1000)).slice(0, 40);
      default: return catalog.filter((t) => hash(JSON.stringify(s) + t.key) % 5 === 0);
    }
  }

  function fieldValues(t: FakeTitle, field: string): (string | number | boolean)[] {
    switch (field) {
      case "genre": return t.genres ?? [];
      case "keyword": return t.keywords;
      case "person": return t.people;
      case "studio": return [t.studio];
      case "network": return t.network ? [t.network] : [];
      case "country": return [t.country];
      case "language": return [t.language];
      case "year": return t.year ? [t.year] : [];
      case "decade": return [decadeOf(t.year)];
      case "rating": return [t.rating ?? 0];
      case "votes": return [t.votes];
      case "popularity": return [t.popularity];
      case "runtime": return [t.runtimeMinutes ?? 0];
      case "contentRating": return t.contentRating ? [t.contentRating] : [];
      case "released": return [t.released];
      case "addedAt": return [t.addedAt.slice(0, 10)];
      case "watchProvider": return t.providers;
      case "collection": return t.collectionId ? [t.collectionId] : [];
      case "watched": return [hash("w" + t.key) % 4 === 0];
      case "inProgress": return [hash("p" + t.key) % 9 === 0];
      default: return [];
    }
  }

  function matches(t: FakeTitle, r: Rule): boolean {
    const vals = fieldValues(t, r.field);
    const eq = (a: unknown, b: unknown) => String(a).toLowerCase() === String(b).toLowerCase();
    switch (r.op) {
      case "in": return Array.isArray(r.value) && r.value.some((v) => vals.some((x) => eq(x, v)));
      case "notIn": return !(Array.isArray(r.value) && r.value.some((v) => vals.some((x) => eq(x, v))));
      case "is": return vals.some((x) => eq(x, r.value));
      case "isNot": return !vals.some((x) => eq(x, r.value));
      case "gte": return vals.some((x) => (typeof x === "number" ? x >= Number(r.value) : String(x) >= String(r.value)));
      case "lte": return vals.some((x) => (typeof x === "number" ? x <= Number(r.value) : String(x) <= String(r.value)));
      case "between": {
        const [lo, hi] = Array.isArray(r.value) ? r.value : [];
        return vals.some((x) => (typeof x === "number" ? x >= Number(lo) && x <= Number(hi) : String(x) >= String(lo) && String(x) <= String(hi)));
      }
      case "within": {
        const since = new Date(Date.now() - durationMs(String(r.value))).toISOString().slice(0, 10);
        return vals.some((x) => String(x) >= since);
      }
    }
  }

  function evaluate(spec: CollectionSpec) {
    const started = performance.now();
    const warnings: string[] = [];
    let items = candidates(spec);
    if (spec.rules.length) items = items.filter((t) => (spec.match === "any" ? spec.rules.some((r) => matches(t, r)) : spec.rules.every((r) => matches(t, r))));
    if (spec.exclude.length) items = items.filter((t) => !spec.exclude.some((r) => matches(t, r)));
    const dir = spec.sortDesc ? -1 : 1;
    const sorters: Record<string, (a: FakeTitle, b: FakeTitle) => number> = {
      popularity: (a, b) => (a.popularity - b.popularity) * dir,
      rating: (a, b) => ((a.rating ?? 0) - (b.rating ?? 0)) * dir,
      released: (a, b) => a.released.localeCompare(b.released) * dir,
      added: (a, b) => a.addedAt.localeCompare(b.addedAt) * dir,
      title: (a, b) => a.title.localeCompare(b.title) * dir,
      random: (a, b) => (hash(a.key + new Date().toDateString()) % 997) - (hash(b.key + new Date().toDateString()) % 997),
      personal: (a, b) => (hash("me" + b.key) % 100) - (hash("me" + a.key) % 100),
    };
    const sorter = sorters[spec.sort];
    if (sorter) items.sort(sorter);
    if (spec.sources.some((s) => ["traktList", "letterboxdList", "mdblist"].includes(s.kind))) warnings.push("External lists are refreshed on the schedule; the preview shows the last fetched copy.");
    if (items.length === 0 && spec.rules.length) warnings.push("No titles match every rule. Try “any” instead of “all”, or loosen a rule.");
    const total = spec.limit > 0 ? Math.min(spec.limit, items.length) : items.length;
    const limited = spec.limit > 0 ? items.slice(0, spec.limit) : items;
    const shown = spec.rotate && spec.rotate.size > 0 ? limited.slice(0, spec.rotate.size) : limited;
    return { total, items: shown.slice(0, 60).map(card), warnings, tookMs: Math.max(1, Math.round(performance.now() - started)) + 9 };
  }

  // ---- Personalization ---------------------------------------------------------------

  function taste(userId: number): TasteProfile {
    const seeds = TASTE_SEEDS[userId] ?? ["Drama"];
    const overrides = state.overrides.get(userId) ?? [];
    const genres = GENRES.map((g) => {
      const i = seeds.indexOf(g);
      let w = i >= 0 ? 0.9 - i * 0.18 : ((hash(g + userId) % 100) / 100) * 0.6 - 0.35;
      const o = overrides.find((x) => x.kind === "genre" && x.value === g);
      if (o) w = o.weight;
      return { name: g, weight: Math.round(w * 100) / 100 };
    }).sort((a, b) => b.weight - a.weight);
    return {
      userId,
      updatedAt: new Date(Date.now() - 3600_000).toISOString(),
      confidence: userId === 4 ? 0.22 : 0.55 + (userId % 3) * 0.12,
      genres,
      people: PEOPLE.slice(userId, userId + 5).map((p, i) => ({ id: p.id, name: p.name, role: p.role, weight: Math.round((0.8 - i * 0.15) * 100) / 100, image: avatarFor(p.name) })),
      keywords: KEYWORDS.slice(0, 6).map((k, i) => ({ name: k.name, weight: Math.round((0.7 - i * 0.22) * 100) / 100 })),
      decades: ["1970", "1980", "1990", "2000", "2010", "2020"].map((d, i) => ({ decade: Number(d), weight: Math.round(Math.max(0, Math.sin((i + userId) / 2)) * 100) / 100 })),
      languages: [{ code: "en", weight: 0.92 }, { code: userId === 1 ? "ko" : "fr", weight: 0.31 }, { code: "ja", weight: userId === 3 ? 0.4 : -0.1 }],
      habits: {
        weeknightMinutes: userId === 3 ? 45 : 95,
        weekendMinutes: userId === 3 ? 120 : 170,
        preferredDevice: userId === 2 ? "mobile" : userId === 3 ? "tv" : "tv",
        bingeScore: userId === 1 ? 0.72 : 0.38,
        rewatchScore: userId === 3 ? 0.81 : 0.24,
        peakHours: userId === 3 ? [16, 17, 18] : [20, 21, 22],
      },
      overrides,
    };
  }

  function pickTitles(seed: string, n: number, filter?: (t: FakeTitle) => boolean): FakeTitle[] {
    const pool = filter ? catalog.filter(filter) : catalog;
    return [...pool].sort((a, b) => (hash(seed + a.key) % 10007) - (hash(seed + b.key) % 10007)).slice(0, n);
  }

  function homeFor(userId: number, device: string, at: Date, section: string) {
    const day = at.getDay();
    const hour = at.getHours();
    const weekend = day === 0 || day === 6;
    const daypart = hour >= 5 && hour < 12 ? "morning" : hour < 17 ? "afternoon" : hour < 22 ? "evening" : "late";
    const t = taste(userId);
    const top = t.genres[0]?.name ?? "Drama";
    const media = section === "movies" ? (x: FakeTitle) => x.type === "movie" : section === "shows" ? (x: FakeTitle) => x.type === "show" : () => true;
    const short = device === "mobile" || (!weekend && hour >= 21);
    const seed = `${userId}-${section}`;
    const mk = (hubId: string, title: string, kind: string, reason: string, score: number, items: FakeTitle[], source: HomeRow["source"] = "engine", why?: (t: FakeTitle) => string): HomeRow => ({
      hubId, title, kind, reason, score: Math.round(score * 100) / 100, source, items: items.map(card),
      itemReasons: why ? Object.fromEntries(items.map((x) => [x.key, why(x)])) : undefined,
    });
    const anchor = pickTitles(seed + "anchor", 1, media)[0];
    const rows: HomeRow[] = [
      mk("continue-watching", "Continue Watching", "continue-watching", "Titles in progress, most recent first.", 0.98, pickTitles(seed + "cw", 6, media), "plex", () => "Started recently"),
      mk("top-10", "Top 10 for you", "top-picks", `Ranked by predicted enjoyment; leans on ${top}.`, 0.91, pickTitles(seed + "top", 10, (x) => media(x) && (x.genres ?? []).includes(top)).concat(pickTitles(seed + "top2", 10, media)).slice(0, 10), "engine", (x) => `${(x.genres ?? []).join(", ")} · rated ${x.rating}`),
      mk("because-you-watched", `Because you watched ${anchor?.title ?? "something"}`, "because-you-watched", `Similar keywords and people to ${anchor?.title}.`, 0.84, pickTitles(seed + "byw", 12, media), "engine", (x) => `Shares ${(x.genres ?? [])[0] ?? "themes"} and keywords with ${anchor?.title}`),
      mk("more-from", `More from ${PEOPLE[userId]?.name}`, "more-from-person", `You've finished ${2 + userId} titles with ${PEOPLE[userId]?.name}.`, 0.77, pickTitles(seed + "mf", 9, media)),
      mk("trending-taste", "Trending in your taste", "trending", `Popular this week in ${top} and neighbours.`, 0.72, pickTitles(seed + "tr", 14, media)),
      mk("hidden-gems", "Hidden gems for you", "hidden-gems", "High ratings, few votes, strong taste match.", 0.66, pickTitles(seed + "hg", 12, (x) => media(x) && (x.rating ?? 0) > 7.5 && x.votes < 6000)),
      mk("short-on-time", "Short on time", "short-on-time", short ? "Shorter titles: it's a weeknight or a phone." : "Shorter titles for a quick watch.", short ? 0.8 : 0.41, pickTitles(seed + "sh", 12, (x) => media(x) && (x.runtimeMinutes ?? 999) <= 95)),
      mk("new-this-week", "New this week in genres you like", "new", "Added in the last 7 days.", 0.58, pickTitles(seed + "new", 10, media)),
      mk("comfort", "Comfort rewatches", "comfort", "Favourites you've watched more than once.", weekend ? 0.61 : 0.47, pickTitles(seed + "cf", 8, media)),
      mk("something-different", "Something different", "exploration", "Outside your usual genres to keep things fresh.", 0.39, pickTitles(seed + "sd", 10, (x) => media(x) && !(x.genres ?? []).includes(top))),
    ];
    for (const c of state.collections) {
      if (!c.enabled || !c.targets.homeRow || c.builtIn) continue;
      if (c.targets.users !== "all" && !c.targets.users.includes(userId)) continue;
      const s = c.targets.schedule;
      if (s?.days?.length && !s.days.includes(day)) continue;
      if (s?.devices?.length && !s.devices.includes(device as never)) continue;
      if (s && (s.fromHour !== undefined || s.toHour !== undefined)) {
        const f = s.fromHour ?? 0; const to = s.toHour ?? 24;
        if (!(f <= to ? hour >= f && hour < to : hour >= f || hour < to)) continue;
      }
      rows.push({ hubId: `collection:${c.id}`, title: c.name, kind: `collection:${c.id}`, reason: "Owner collection promoted to Home.", score: 0.6, source: "owner", items: evaluate(c).items.slice(0, 12) });
    }
    rows.sort((a, b) => b.score - a.score);
    const laid = previewLayout(rows, state.layout, { userId, section: (section || "home") as "home", device, day, hour })
      .map((p) => ({ ...p.row, pinned: p.action === "pin" || undefined, hidden: p.hidden || undefined }))
      .filter((r) => !r.hidden);
    return { rows: laid, generatedAt: new Date().toISOString(), context: { device, daypart, weekend } };
  }

  function availableRows() {
    return [
      { hubId: "continue-watching", title: "Continue Watching", kind: "continue-watching", description: "In-progress titles" },
      { hubId: "top-10", title: "Top 10 for you", kind: "top-picks", description: "Highest predicted engagement" },
      { hubId: "because-you-watched", title: "Because you watched…", kind: "because-you-watched", description: "Similar to a recent favourite" },
      { hubId: "more-from", title: "More from…", kind: "more-from-person", description: "A director or actor the viewer loves" },
      { hubId: "trending-taste", title: "Trending in your taste", kind: "trending", description: "Popular now, filtered by taste" },
      { hubId: "hidden-gems", title: "Hidden gems for you", kind: "hidden-gems", description: "Under-seen, highly rated" },
      { hubId: "short-on-time", title: "Short on time", kind: "short-on-time", description: "Short runtimes" },
      { hubId: "new-this-week", title: "New this week", kind: "new", description: "Recently added in liked genres" },
      { hubId: "comfort", title: "Comfort rewatches", kind: "comfort", description: "Favourites watched again" },
      { hubId: "something-different", title: "Something different", kind: "exploration", description: "Exploration outside the usual" },
      ...state.collections.filter((c) => !c.builtIn).map((c) => ({ hubId: `collection:${c.id}`, title: c.name, kind: `collection:${c.id}`, description: "Owner collection" })),
    ];
  }

  function activity(): ActivityDay[] {
    const out: ActivityDay[] = [];
    for (let i = 29; i >= 0; i--) {
      const d = new Date(Date.now() - i * 86400000);
      const dow = d.getDay();
      const base = dow === 0 || dow === 6 ? 9 : 5;
      const plays = base + (hash(d.toDateString()) % 6);
      out.push({ date: d.toISOString().slice(0, 10), plays, hours: Math.round(plays * 1.4 * 10) / 10, uniqueViewers: 1 + (hash("u" + d.toDateString()) % 4) });
    }
    return out;
  }

  function dashboard() {
    const session = catalog[3]!;
    return {
      names: ["cache", "catalog", "hwaccel", "indexers", "providers", "remote", "sessions"],
      setupComplete: state.config.setupComplete,
      version: state.boot.version,
      sections: {
        cache: { used: 87 * 1024 ** 3, budget: state.config.cache.budgetBytes },
        catalog: { movies: 24929, shows: 24298, browsableMovies: 24928, browsableShows: 24298, lastSync: new Date(Date.now() - 5 * 3600_000).toISOString(), sync: { running: false, titlesSeen: 63500 } },
        hwaccel: { ffmpeg: "8.0.1", hwaccel: { name: "videotoolbox", encoders: ["h264_videotoolbox", "hevc_videotoolbox"] } },
        indexers: (state.config.indexers ?? []).map((ix, i) => ({ indexer: ix.name, day: new Date().toISOString().slice(0, 10), hitsUsed: 6 + i * 15, hitsLimit: ix.dailyLimit, hitsRemaining: Math.max(0, ix.dailyLimit - 6 - i * 15), grabsUsed: 3, grabsLimit: ix.dailyGrabs, grabsRemaining: ix.dailyGrabs - 3 })),
        providers: (state.config.usenet.providers ?? []).map((p, i) => ({ id: p.id, name: p.name, host: p.host, backfill: p.backfill, state: "ok", configuredConnections: p.connections, activeConnections: i === 0 ? 6 : 0 })),
        remote: { claimed: true, ownerUsername: "owner", publicAddress: "203.0.113.7", remoteAccess: state.config.plex.remoteAccess, listed: true, presence: { connected: true }, upnp: { active: true }, cert: { present: true, subject: "*.abc.plex.direct", notAfter: new Date(Date.now() + 80 * 86400000).toISOString() } },
        sessions: [{
          ID: "s1", UserID: 2, Key: session.key, TitleName: `${session.title} (${session.year})`, Username: "Sam Rivera",
          Client: { product: "Plex for Apple TV", deviceName: "Living Room", remote: false }, Decision: "directplay",
          BytesServed: 2.1 * 1024 ** 3, SpeedBps: 3.2e6, StartedAt: new Date(Date.now() - 40 * 60000).toISOString(),
          Info: { size: 5.4 * 1024 ** 3, duration: 118 * 60 * 1e9 }, viewOffset: 46 * 60 * 1000,
          Resolution: { release: { name: `${session.title.replace(/ /g, ".")}.${session.year}.1080p.WEB-DL.H264` } },
        }],
      },
    };
  }

  // ---- Router ------------------------------------------------------------------------

  async function handle(method: string, url: URL, body: unknown): Promise<Response> {
    const p = url.pathname.replace(/^\/ether\/api/, "");
    const q = url.searchParams;
    const seg = p.split("/").filter(Boolean);
    const route = `${method} /${seg.join("/")}`;

    // Static routes
    switch (route) {
      case "GET /bootstrap": case "GET /health": return json(state.boot);
      case "GET /dashboard": return json(dashboard());
      case "GET /config": return json({ config: state.config });
      case "PUT /config": state.config = body as EtherConfig; return json({ config: state.config });
      case "GET /setup/status": return json({ setupComplete: state.config.setupComplete, requiredComplete: true, steps: [] });
      case "POST /setup/complete": state.config.setupComplete = true; state.boot.setupComplete = true; return json({ setupComplete: true, requiredComplete: true, steps: [] });
      case "POST /setup/reopen": state.config.setupComplete = false; state.boot.setupComplete = false; return json({ setupComplete: false, requiredComplete: true, steps: [] });
      case "POST /plex/pin": return json({ id: "pin1", code: "WXYZ", authUrl: "https://app.plex.tv/auth#?code=WXYZ" });
      case "POST /plex/claim": return json({ ok: true });
      case "POST /auth/pin": return json({ handle: "h1", code: "ABCD", authUrl: "https://app.plex.tv/auth#?code=ABCD" });
      case "POST /test/usenet": case "POST /test/indexer": case "POST /test/tmdb": case "POST /test/llm": case "POST /test/trakt": case "POST /test/opensubtitles":
        return json({ ok: true, durationMs: 40 + (hash(route) % 200) });
      case "GET /disk": return json({ path: q.get("path") ?? "", probedPath: "/", totalBytes: 2e12, freeBytes: 1.2e12, usedBytes: 0.8e12, cacheBudgetBytes: state.config.cache.budgetBytes });
      case "GET /users": return json({ users: state.users });
      case "GET /logs": {
        const lvl = q.get("level") ?? "";
        const order = ["debug", "info", "warn", "error"];
        const needle = (q.get("q") ?? "").toLowerCase();
        const entries = state.logs.filter((e) => (!lvl || order.indexOf(e.level.toLowerCase()) >= order.indexOf(lvl)) && (!needle || e.message.toLowerCase().includes(needle)));
        return json({ entries: entries.slice(-Number(q.get("limit") ?? 500)), total: state.logs.length });
      }
      case "DELETE /logs": state.logs = []; return json({ ok: true });
      case "GET /traces": return json({ traces: [
        { titleKey: catalog[3]!.key, titleName: `${catalog[3]!.title} (${catalog[3]!.year})`, profile: "1080p", at: new Date(Date.now() - 41 * 60000).toISOString(), searchDuration: 1.8e9, winner: "r1", candidates: [
          { releaseId: "r1", name: `${catalog[3]!.title.replace(/ /g, ".")}.1080p.WEB-DL.H264-GRP`, indexer: "Indexer One", score: 812, rejected: false, healthChecked: true, healthy: true, fetchDuration: 3.1e8, healthDuration: 6.4e8 },
          { releaseId: "r2", name: `${catalog[3]!.title.replace(/ /g, ".")}.2160p.REMUX-GRP`, indexer: "Indexer Two", score: 640, rejected: true, reason: "above the 1080p profile cap", healthChecked: false, healthy: false, fetchDuration: 0, healthDuration: 0 },
          { releaseId: "r3", name: `${catalog[3]!.title.replace(/ /g, ".")}.CAM`, indexer: "Indexer One", score: 0, rejected: true, reason: "CAM releases are disabled", healthChecked: false, healthy: false, fetchDuration: 0, healthDuration: 0 },
        ] },
      ] });

      case "GET /collections": return json({ collections: state.collections });
      case "POST /collections": {
        const spec = normalizeSpec(body as CollectionSpec);
        const saved = { ...spec, id: `c${seq++}`, updatedAt: new Date().toISOString(), lastBuiltAt: new Date().toISOString(), itemCount: evaluate(spec).total };
        state.collections.push(saved);
        return json(saved);
      }
      case "POST /collections/preview": return json(evaluate(normalizeSpec(body as CollectionSpec)));
      case "GET /collections/templates": return json({ templates: TEMPLATES });
      case "POST /collections/generate": {
        if (!state.llm) return err(501, "not_implemented", "The LLM is not configured");
        const prompt = String((body as { prompt?: string }).prompt ?? "").toLowerCase();
        const genres = GENRES.filter((g) => prompt.includes(g.toLowerCase()) || (g === "Science Fiction" && /sci-?fi/.test(prompt)));
        const rules: Rule[] = genres.length ? [{ field: "genre", op: "in", value: genres }] : [];
        const dm = /(19|20)(\d)0s/.exec(prompt) ?? /\b(\d)0s\b/.exec(prompt);
        if (dm) rules.push({ field: "decade", op: "in", value: [dm[0].length === 5 ? dm[0].slice(0, 4) : `19${dm[1]}0`] });
        if (/short|quick|under/.test(prompt)) rules.push({ field: "runtime", op: "lte", value: 95 });
        if (/acclaimed|best|great|top/.test(prompt)) rules.push({ field: "rating", op: "gte", value: 7.5 });
        const name = String((body as { prompt?: string }).prompt ?? "").replace(/^(a |an )?(collection|row|list) (of|with)\s+/i, "").replace(/^./, (c) => c.toUpperCase()).slice(0, 48);
        const spec = normalizeSpec({ name, description: `Generated from “${(body as { prompt: string }).prompt}”.`, mediaType: /tv|show|series/.test(prompt) ? "show" : "movie", rules, sort: rules.some((r) => r.field === "rating") ? "rating" : "popularity" });
        return json({ spec, explanation: rules.length ? `Matched ${rules.length} condition${rules.length === 1 ? "" : "s"} from your description: ${rules.map((r) => r.field).join(", ")}.` : "I couldn't find specific conditions, so this starts from the whole catalogue sorted by popularity." });
      }
      case "GET /catalog/search": {
        const needle = (q.get("q") ?? "").toLowerCase();
        const type = q.get("type");
        return json({ items: catalog.filter((t) => t.title.toLowerCase().includes(needle) && (!type || type === "mixed" || t.type === type)).slice(0, Number(q.get("limit") ?? 24)).map(card) });
      }
      case "GET /catalog/facets": {
        const facet = q.get("facet");
        const type = q.get("type");
        const pool = catalog.filter((t) => !type || type === "mixed" || t.type === type);
        const counts = new Map<string, number>();
        for (const t of pool) {
          const vals = facet === "genre" ? t.genres ?? [] : facet === "decade" ? [decadeOf(t.year)] : facet === "contentRating" ? [t.contentRating ?? ""] : facet === "country" ? [t.country] : facet === "language" ? [t.language] : facet === "studio" ? [String(t.studio)] : [];
          for (const v of vals) if (v) counts.set(v, (counts.get(v) ?? 0) + 1);
        }
        const title = (k: string) => (facet === "decade" ? `${k}s` : facet === "studio" ? STUDIOS.find((s) => String(s.id) === k)?.name ?? k : facet === "language" ? (new Intl.DisplayNames(["en"], { type: "language" }).of(k) ?? k) : facet === "country" ? (new Intl.DisplayNames(["en"], { type: "region" }).of(k) ?? k) : k);
        return json({ values: [...counts.entries()].sort((a, b) => (facet === "decade" ? a[0].localeCompare(b[0]) : b[1] - a[1])).map(([key, count]) => ({ key, title: title(key), count })) });
      }
      case "GET /catalog/lookup": {
        const kind = q.get("kind");
        const needle = (q.get("q") ?? "").toLowerCase();
        const pool = kind === "person" ? PEOPLE.map((x) => ({ id: x.id, name: x.name, subtitle: x.role, image: avatarFor(x.name) }))
          : kind === "keyword" ? KEYWORDS : kind === "company" ? STUDIOS : kind === "network" ? NETWORKS : kind === "collection" ? COLLECTIONS : kind === "watchProvider" ? PROVIDERS.map((x) => ({ ...x, subtitle: q.get("region") ?? "US" })) : [];
        return json({ results: pool.filter((x) => x.name.toLowerCase().includes(needle)).slice(0, 10) });
      }
      case "GET /viewers": return json({ viewers: VIEWERS.map((v, i) => ({ ...v, thumb: avatarFor(v.title), lastActive: new Date(Date.now() - (i * 7 + 1) * 3600_000).toISOString() })) });
      case "GET /layout": return json({ rules: state.layout, availableRows: availableRows() });
      case "PUT /layout": {
        state.layout = ((body as { rules: LayoutRule[] }).rules ?? []).map((r, i) => ({ ...r, id: r.id ?? `lr-${Date.now()}-${i}` }));
        return json({ rules: state.layout, availableRows: availableRows() });
      }
      case "GET /insights/rows": return json({ rows: availableRows().slice(0, 9).map((r) => {
        const impressions7d = 80 + (hash(r.hubId) % 400);
        const plays7d = Math.round(impressions7d * ((hash(r.hubId + "p") % 30) / 100));
        return { hubId: r.hubId, title: r.title, impressions7d, plays7d, playRate: plays7d / impressions7d };
      }) });
      case "GET /insights/activity": return json({ days: activity() });
    }

    // Parameterised routes
    if (seg[0] === "collections" && seg[1]) {
      const id = decodeURIComponent(seg[1]);
      const idx = state.collections.findIndex((c) => c.id === id);
      if (seg[2] === "artwork" && method === "GET") {
        const c = state.collections[idx];
        const svg = decodeURIComponent((posterFor(id, c?.name ?? "Collection").split(",")[1]) ?? "");
        return new Response(svg, { status: 200, headers: { "Content-Type": "image/svg+xml" } });
      }
      if (idx < 0) return err(404, "not_found", "no such collection");
      const current = state.collections[idx]!;
      if (seg[2] === "rebuild" && method === "POST") {
        const r = evaluate(current);
        current.itemCount = r.total; current.lastBuiltAt = new Date().toISOString();
        return json({ itemCount: r.total, lastBuiltAt: current.lastBuiltAt });
      }
      if (method === "GET") return json(current);
      if (method === "PUT") {
        if (current.builtIn) return err(400, "read_only", "built-in collections can't be edited");
        const spec = normalizeSpec(body as CollectionSpec);
        const saved = { ...spec, id, updatedAt: new Date().toISOString(), itemCount: evaluate(spec).total, lastBuiltAt: new Date().toISOString() };
        state.collections[idx] = saved;
        return json(saved);
      }
      if (method === "DELETE") { state.collections.splice(idx, 1); return new Response(null, { status: 204 }); }
    }
    if (seg[0] === "catalog" && seg[1] === "title" && seg[2]) {
      const t = byKey.get(decodeURIComponent(seg[2]));
      if (!t) return err(404, "not_found", "no such title");
      return json({ ...card(t), cast: t.people.map((id) => PEOPLE.find((p) => p.id === id)).filter(Boolean), directors: [], keywords: t.keywords.map((id) => KEYWORDS.find((k) => k.id === id)).filter(Boolean), collection: null });
    }
    if (seg[0] === "viewers" && seg[1]) {
      const id = Number(seg[1]);
      const v = VIEWERS.find((x) => x.id === id);
      if (!v) return err(404, "not_found", "no such viewer");
      if (!seg[2] && method === "GET") {
        const recent = pickTitles(`recent${id}`, 8).map((t, i) => ({ title: card(t), at: new Date(Date.now() - (i * 19 + 2) * 3600_000).toISOString(), completed: i % 3 !== 1, progress: i % 3 === 1 ? 0.35 + (i % 4) * 0.1 : 1 }));
        return json({ ...v, thumb: avatarFor(v.title), lastActive: new Date(Date.now() - 3600_000).toISOString(), recent });
      }
      if (seg[2] === "taste" && !seg[3] && method === "GET") return json(taste(id));
      if (seg[2] === "taste" && seg[3] === "overrides" && method === "PUT") {
        state.overrides.set(id, (body as { overrides: TasteOverride[] }).overrides ?? []);
        return json(taste(id));
      }
      if (seg[2] === "home" && method === "GET") {
        const at = q.get("at") ? new Date(q.get("at")!) : new Date();
        return json(homeFor(id, q.get("device") ?? "tv", at, q.get("section") ?? "home"));
      }
      if (seg[2] === "refresh" && method === "POST") return new Response(null, { status: 204 });
    }
    if (seg[0] === "users" && seg[1] && (method === "PATCH" || method === "PUT")) {
      const id = Number(seg[1]);
      const i = state.users.findIndex((u) => u.id === id);
      if (i < 0) return err(404, "not_found", "no such user");
      state.users[i] = { ...state.users[i]!, ...(body as Partial<EtherUser>) };
      return json({ user: state.users[i] });
    }
    if (seg[0] === "config" && seg[1] && method === "PUT") {
      const key = seg[1] === "opensubtitles" ? "openSubtitles" : seg[1];
      (state.config as unknown as Record<string, unknown>)[key] = body;
      return json({ section: key, value: body });
    }
    if (seg[0] === "auth" && seg[1] === "pin" && seg[2]) return json({ done: true, token: "mock-owner-token" });
    if (seg[0] === "plex" && seg[1] === "pin" && seg[2]) return json({ done: true });
    if (seg[0] === "status" && seg[1]) return json({ name: seg[1], value: (dashboard().sections as Record<string, unknown>)[seg[1]] ?? null });
    return err(404, "not_found", "no such API endpoint");
  }

  async function fetchImpl(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const req = input instanceof Request ? input : null;
    const url = new URL(req ? req.url : String(input), "http://localhost");
    const method = (init?.method ?? req?.method ?? "GET").toUpperCase();
    let body: unknown = undefined;
    const raw = init?.body ?? null;
    if (typeof raw === "string" && raw) { try { body = JSON.parse(raw); } catch { body = raw; } }
    state.calls.push({ method, path: url.pathname.replace(/^\/ether\/api/, "") + url.search, body });
    if (opts.latencyMs) await new Promise((r) => setTimeout(r, opts.latencyMs! * (0.5 + Math.random())));
    if (init?.signal?.aborted) throw new DOMException("Aborted", "AbortError");
    return handle(method, url, body);
  }

  return { fetch: fetchImpl, state, catalog, evaluate, homeFor };
}

export type FakeServer = ReturnType<typeof createFakeServer>;
export type { TitleCard };
