import { formatBytes, formatDate, isPlainObject, parseTime, plural, relativeTime } from "./format";

/** ok/warn/error carry status; "off" is a deliberate owner choice; "unknown" means not reported. */
export type HealthLevel = "ok" | "warn" | "error" | "off" | "unknown";

export interface HealthItem {
  id: "usenet" | "indexers" | "cache" | "catalog" | "remote" | "plexdirect";
  label: string;
  level: HealthLevel;
  summary: string;
  details: string[];
  /** Settings section that fixes this, for "Open settings" links. */
  settings: string;
  /** 0..1 gauge when the item has a natural fill level. */
  fill?: number;
}

const RANK: Record<HealthLevel, number> = { error: 4, warn: 3, unknown: 2, ok: 1, off: 0 };

export function worstLevel(levels: HealthLevel[]): HealthLevel {
  let worst: HealthLevel = "ok";
  for (const l of levels) if (RANK[l] > RANK[worst]) worst = l;
  return worst;
}

const num = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : Number(v) || 0);
const str = (v: unknown): string => (typeof v === "string" ? v : "");

function errorOf(v: unknown): string {
  return isPlainObject(v) && typeof v.error === "string" ? v.error : "";
}

export function usenetHealth(v: unknown): HealthItem {
  const base = { id: "usenet" as const, label: "Usenet", settings: "usenet" };
  const err = errorOf(v);
  if (err) return { ...base, level: "error", summary: err, details: [] };
  if (!Array.isArray(v)) return { ...base, level: "unknown", summary: "Not reported", details: [] };
  const providers = v.filter(isPlainObject);
  if (providers.length === 0) return { ...base, level: "error", summary: "No providers configured", details: [] };
  const primary = providers.filter((p) => !p.backfill);
  const bad = providers.filter((p) => str(p.state) && str(p.state) !== "ok");
  const primaryBad = primary.filter((p) => str(p.state) && str(p.state) !== "ok");
  const active = providers.reduce((n, p) => n + num(p.activeConnections), 0);
  const configured = providers.reduce((n, p) => n + num(p.configuredConnections ?? p.maxConnections), 0);
  const details = providers.map((p) => {
    const state = str(p.state) || "unknown";
    return `${str(p.name) || str(p.host) || str(p.id)}: ${state}${p.backfill ? " (backfill)" : ""} · ${num(p.activeConnections)}/${num(p.configuredConnections ?? p.maxConnections)} connections`;
  });
  let level: HealthLevel = "ok";
  if (primary.length > 0 && primaryBad.length === primary.length) level = "error";
  else if (bad.length > 0) level = "warn";
  const summary = level === "ok"
    ? `${plural(providers.length, "provider")} healthy · ${active}/${configured} connections busy`
    : `${bad.length} of ${plural(providers.length, "provider")} degraded`;
  return { ...base, level, summary, details, fill: configured > 0 ? active / configured : undefined };
}

export function indexersHealth(v: unknown): HealthItem {
  const base = { id: "indexers" as const, label: "Indexers", settings: "indexers" };
  const err = errorOf(v);
  if (err) return { ...base, level: "error", summary: err, details: [] };
  if (!Array.isArray(v)) return { ...base, level: "unknown", summary: "Not reported", details: [] };
  const rows = v.filter(isPlainObject);
  if (rows.length === 0) return { ...base, level: "error", summary: "No indexers configured", details: [] };
  let exhausted = 0;
  let remaining = 0;
  let limited = 0;
  let used = 0;
  let limit = 0;
  const details = rows.map((r) => {
    const hl = num(r.hitsLimit);
    const hr = num(r.hitsRemaining);
    if (hl > 0) {
      limited++;
      remaining += hr;
      used += num(r.hitsUsed);
      limit += hl;
      if (hr <= 0) exhausted++;
    }
    const grabs = num(r.grabsLimit) > 0 ? ` · ${num(r.grabsRemaining)}/${num(r.grabsLimit)} grabs left` : "";
    return `${str(r.indexer) || str(r.name)}: ${hl > 0 ? `${hr}/${hl} searches left` : "unlimited searches"}${grabs}`;
  });
  let level: HealthLevel = "ok";
  if (exhausted > 0 && exhausted === rows.length) level = "error";
  else if (exhausted > 0) level = "warn";
  const summary = limited === 0
    ? `${plural(rows.length, "indexer")} · no daily limits`
    : exhausted > 0
      ? `${exhausted} of ${plural(rows.length, "indexer")} out of searches today`
      : `${plural(rows.length, "indexer")} · ${remaining} searches left today`;
  return { ...base, level, summary, details, fill: limit > 0 ? used / limit : undefined };
}

export function cacheHealth(v: unknown): HealthItem {
  const base = { id: "cache" as const, label: "Cache", settings: "cache" };
  if (!isPlainObject(v)) return { ...base, level: "unknown", summary: "Not reported", details: [] };
  const used = num(v.used ?? v.usedBytes);
  const budget = num(v.budget ?? v.budgetBytes);
  if (budget <= 0) return { ...base, level: "warn", summary: "No disk budget set", details: [] };
  const fill = Math.min(1, used / budget);
  return {
    ...base, level: "ok", fill,
    summary: `${formatBytes(used)} of ${formatBytes(budget)}`,
    details: ["Older segments are evicted automatically when the budget is reached."],
  };
}

export function catalogHealth(v: unknown, now = new Date()): HealthItem {
  const base = { id: "catalog" as const, label: "Catalogue", settings: "catalog" };
  const err = errorOf(v);
  if (err) return { ...base, level: "error", summary: err, details: [] };
  if (!isPlainObject(v)) return { ...base, level: "unknown", summary: "Not reported", details: [] };
  const movies = num(v.browsableMovies ?? v.movies);
  const shows = num(v.browsableShows ?? v.shows);
  const sync = isPlainObject(v.sync) ? v.sync : {};
  const last = parseTime(v.lastSync);
  const details = [
    `${movies.toLocaleString()} movies · ${shows.toLocaleString()} shows browsable`,
    `Last sync ${relativeTime(last, now)}`,
  ];
  if (typeof sync.error === "string" && sync.error) details.push(`Last sync error: ${sync.error}`);
  if (sync.running === true) {
    return { ...base, level: "ok", summary: `Syncing… ${num(sync.titlesSeen).toLocaleString()} titles seen`, details };
  }
  if (!last) return { ...base, level: "warn", summary: "Not synced yet", details };
  const ageDays = (now.getTime() - last.getTime()) / 86400000;
  const level: HealthLevel = typeof sync.error === "string" && sync.error ? "warn" : ageDays > 3 ? "warn" : "ok";
  return { ...base, level, summary: `${(movies + shows).toLocaleString()} titles · synced ${relativeTime(last, now)}`, details };
}

export function remoteHealth(v: unknown): HealthItem {
  const base = { id: "remote" as const, label: "Remote access", settings: "plex" };
  if (!isPlainObject(v)) return { ...base, level: "unknown", summary: "Not reported", details: [] };
  if (v.claimed === false) return { ...base, level: "error", summary: "Server is not claimed", details: ["Finish the setup wizard to link Ether to your Plex account."] };
  const presence = isPlainObject(v.presence) ? v.presence : {};
  const details: string[] = [];
  if (str(v.ownerUsername)) details.push(`Claimed by ${str(v.ownerUsername)}`);
  if (str(v.publicAddress)) details.push(`Public address ${str(v.publicAddress)}`);
  details.push(presence.connected ? "Connected to plex.tv" : "Not connected to plex.tv");
  if (v.listed === false) details.push("Not yet published to plex.tv");
  if (presence.connected === false) return { ...base, level: "warn", summary: "plex.tv connection lost", details };
  if (v.remoteAccess === true) {
    const upnp = isPlainObject(v.upnp) ? v.upnp : {};
    if (upnp.active === true) details.push("Port mapped with UPnP");
    return { ...base, level: "ok", summary: "Reachable outside your network", details };
  }
  return { ...base, level: "off", summary: "Local network only", details };
}

export function plexDirectHealth(v: unknown, now = new Date()): HealthItem {
  const base = { id: "plexdirect" as const, label: "plex.direct", settings: "plex" };
  if (!isPlainObject(v)) return { ...base, level: "unknown", summary: "Not reported", details: [] };
  const cert = isPlainObject(v.cert) ? v.cert : null;
  if (!cert || cert.present !== true) {
    return { ...base, level: "warn", summary: "No certificate yet", details: ["Secure connections fall back to plain HTTP until a plex.direct certificate is issued."] };
  }
  const expires = parseTime(cert.notAfter);
  const details = [str(cert.subject)].filter(Boolean);
  if (cert.custom === true) details.push("Custom certificate");
  if (!expires) return { ...base, level: "ok", summary: "Certificate installed", details };
  const days = (expires.getTime() - now.getTime()) / 86400000;
  details.push(`Expires ${formatDate(expires)}`);
  if (days < 0) return { ...base, level: "error", summary: "Certificate expired", details };
  if (days < 14) return { ...base, level: "warn", summary: `Certificate renews ${relativeTime(expires, now)}`, details };
  return { ...base, level: "ok", summary: `Valid until ${formatDate(expires)}`, details };
}

export function computeHealth(sections: Record<string, unknown> | undefined, now = new Date()): HealthItem[] {
  const s = sections ?? {};
  return [
    usenetHealth(s.providers),
    indexersHealth(s.indexers),
    cacheHealth(s.cache),
    catalogHealth(s.catalog, now),
    remoteHealth(s.remote),
    plexDirectHealth(s.remote, now),
  ];
}

// ---- Now playing --------------------------------------------------------------

export interface NowPlaying {
  id: string;
  key: string;
  title: string;
  user: string;
  device: string;
  product: string;
  decision: string;
  remote: boolean;
  bytesServed: number;
  size: number;
  speedBps: number;
  startedAt: Date | null;
  durationMs: number;
  /** 0..1 when the server reports a playback position, else null. */
  progress: number | null;
  release: string;
  failovers: number;
}

function pick(o: Record<string, unknown>, ...keys: string[]): unknown {
  for (const k of keys) if (o[k] !== undefined && o[k] !== null) return o[k];
  return undefined;
}

/** Sessions come from playback.Sessions() (Go field names, no JSON tags); accept camelCase too. */
export function normalizeSessions(v: unknown): NowPlaying[] {
  if (!Array.isArray(v)) return [];
  return v.filter(isPlainObject).map((s) => {
    const info = (pick(s, "Info", "info") ?? {}) as Record<string, unknown>;
    const client = (pick(s, "Client", "client") ?? {}) as Record<string, unknown>;
    const resolution = (pick(s, "Resolution", "resolution") ?? {}) as Record<string, unknown>;
    const release = isPlainObject(resolution.release) ? resolution.release : {};
    const durationNs = num(pick(info, "duration", "Duration"));
    const offsetMs = num(pick(s, "viewOffset", "ViewOffset", "positionMs", "PositionMs"));
    const durationMs = durationNs / 1e6;
    const explicit = pick(s, "progress", "Progress");
    let progress: number | null = null;
    if (typeof explicit === "number") progress = explicit;
    else if (offsetMs > 0 && durationMs > 0) progress = Math.min(1, offsetMs / durationMs);
    return {
      id: str(pick(s, "ID", "id")),
      key: String(pick(s, "Key", "key") ?? ""),
      title: str(pick(s, "TitleName", "titleName", "title")) || "Untitled",
      user: str(pick(s, "Username", "username")) || "Someone",
      device: str(pick(client, "deviceName", "device")) || str(pick(client, "product")) || "Unknown device",
      product: str(pick(client, "product")),
      decision: str(pick(s, "Decision", "decision")),
      remote: pick(client, "remote") === true,
      bytesServed: num(pick(s, "BytesServed", "bytesServed")),
      size: num(pick(info, "size", "Size")),
      speedBps: num(pick(s, "SpeedBps", "speedBps")),
      startedAt: parseTime(pick(s, "StartedAt", "startedAt")),
      durationMs,
      progress,
      release: str(release.name),
      failovers: num(pick(s, "Failovers", "failovers")),
    };
  });
}
