/** Mirrors of the Go config/API types (internal/config, internal/admin). */

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

export interface UsenetProvider {
  id: string;
  name: string;
  host: string;
  port: number;
  tls: boolean;
  username: string;
  password: string;
  connections: number;
  priority: number;
  backfill: boolean;
  enabled: boolean;
}

export interface Indexer {
  id: string;
  name: string;
  url: string;
  apiKey: string;
  dailyLimit: number;
  dailyGrabs: number;
  priority: number;
  enabled: boolean;
  movieCategories: number[] | null;
  tvCategories: number[] | null;
  /** "usenet" (Newznab, the default when empty or absent) or "torrent" (Torznab). */
  protocol?: IndexerProtocol | "";
}

export type IndexerProtocol = "usenet" | "torrent";

export type DebridKind = "realdebrid" | "torbox" | "alldebrid" | "premiumize";

/** One owner-supplied debrid account. `apiKey` is redacted to "••••" when set. */
export interface DebridProvider {
  id: string;
  name: string;
  kind: DebridKind;
  apiKey: string;
  /** Lower is tried first. */
  priority: number;
  enabled: boolean;
}

export interface DebridConfig {
  providers: DebridProvider[] | null;
  cachedOnly: boolean;
  removeUncached: boolean;
  /** Largest HTTP range request; requests start at 512 KiB and grow. 0 = 8 MiB; max 64 MiB. */
  chunkBytes?: number;
  /** Fixed read-ahead window in chunks; 0 = adaptive to throughput, negative disables; max 32. */
  readAheadChunks?: number;
  /** 0 = 4; max 16. */
  maxConnections?: number;
}

/** GET /status/debrid. */
export interface DebridStatus {
  enabled: boolean;
  cachedOnly: boolean;
  providers: DebridProviderStatus[] | null;
  /** Streaming engine counters; absent until the engine is wired. */
  engine?: DebridEngineStatus;
}

/** Moving averages (EWMA) of one provider's timings; all 0 until `samples` > 0. */
export interface DebridLatency {
  resolveMs?: number;
  linkMs?: number;
  connectMs?: number;
  ttfbMs?: number;
  throughputBps?: number;
  samples?: number;
}

export interface DebridEngineStatus {
  openFiles?: number;
  blockBytes?: number;
  memoryHits?: number;
  diskHits?: number;
  blocksFetched?: number;
  bytesFetched?: number;
  hedges?: number;
  hedgeWins?: number;
  linkRefreshes?: number;
  errors?: number;
}

export interface DebridProviderStatus {
  id: string;
  name: string;
  kind: string;
  enabled: boolean;
  priority: number;
  state: "ok" | "backoff" | "auth_failed" | "disabled" | string;
  lastError?: string;
  backoffUntil?: string;
  requests?: number;
  failures?: number;
  cached?: number;
  notCached?: number;
  linksRefreshed?: number;
  /** Torrents reused from the account instead of re-added. */
  reused?: number;
  latency?: DebridLatency;
  lastSuccess?: string;
}

export interface EtherConfig {
  server: {
    port: number;
    bindAddress: string;
    friendlyName: string;
    machineIdentifier: string;
    logLevel: string;
    defaultStreamLimit: number;
    maxStreams: number;
  };
  plex: {
    token: string;
    clientIdentifier: string;
    ownerPlexId: number;
    ownerUsername: string;
    claimed: boolean;
    remoteAccess: boolean;
    useUpnp: boolean;
    publicPort: number;
    publicHost: string;
    customUrls: string[] | null;
    certFile: string;
    keyFile: string;
    certHash: string;
    secureConnections: string;
  };
  usenet: { providers: UsenetProvider[] | null };
  indexers: Indexer[] | null;
  /** Absent on servers without debrid support. */
  debrid?: DebridConfig;
  cache: {
    dir: string;
    budgetBytes: number;
    prefetchNextEpisode: boolean;
    prefetchBytes: number;
    keepInProgressWarm: boolean;
    nextEpisodeAt: number;
  };
  catalog: {
    tmdbKey: string;
    language: string;
    region: string;
    minPopularity: number;
    minVotes: number;
    includeAdult: boolean;
    refreshInterval: string;
    showUnreleased: boolean;
  };
  resolver: {
    allowCam: boolean;
    healthSamples: number;
    searchCacheTtl: string;
    unavailableCooloff: string;
    maxCandidates: number;
    blockedGroups: string[] | null;
    blockedTerms: string[] | null;
  };
  quality: {
    profiles: QualityProfile[] | null;
    rules: QualityRule[] | null;
    default: string;
    remote: string;
  };
  transcode: {
    ffmpegPath: string;
    ffprobePath: string;
    hwAccel: string;
    vaapiDevice: string;
    tempDir: string;
    maxTranscodes: number;
    remoteMaxBitrateKbps: number;
  };
  trakt: { enabled: boolean; clientId: string; clientSecret: string };
  openSubtitles: { enabled: boolean; apiKey: string; username: string; password: string };
  llm: { enabled: boolean; baseUrl: string; apiKey: string; model: string };
  updates: { channel: string; autoUpdate: boolean; feedUrl: string };
  spike: { enabled: boolean; localFiles: string[] | null };
  setupComplete: boolean;
}

export interface QualityProfile {
  name: string;
  maxHeight: number;
  minHeight: number;
  allowHdr: boolean;
  allowDolbyVision: boolean;
  preferredSources: string[] | null;
  preferredCodecs: string[] | null;
  preferredAudio: string[] | null;
  minSizeMbPerMin: number;
  maxSizeMbPerMin: number;
  preferredLanguages: string[] | null;
  allowCam: boolean;
}

export interface QualityRule {
  product?: string;
  platform?: string;
  userId?: number;
  profile: string;
}

export interface SetupStep {
  id: string;
  title: string;
  complete: boolean;
  required: boolean;
  detail?: string;
}
export interface SetupStatus {
  setupComplete: boolean;
  requiredComplete: boolean;
  steps: SetupStep[];
}
export interface Bootstrap {
  ok: boolean;
  version: string;
  setupComplete: boolean;
  authenticated: boolean;
  authRequired: boolean;
  localTrusted?: boolean;
  signIn?: boolean;
  serverName: string;
}
export interface TestResult {
  ok: boolean;
  error?: string;
  durationMs: number;
}
export interface LogEntry {
  time: string;
  level: string;
  message: string;
  attrs?: Record<string, string>;
}
export interface DiskInfo {
  path: string;
  probedPath: string;
  totalBytes: number;
  freeBytes: number;
  usedBytes: number;
  cacheBudgetBytes: number;
}

export interface EtherUser {
  id: number;
  plexId: number;
  uuid?: string;
  username: string;
  title: string;
  email?: string;
  thumb?: string;
  isOwner: boolean;
  streamLimit: number;
  maxContentRating: string;
  subtitleLanguages: string[] | null;
  audioLanguages: string[] | null;
  allowedSections: number[] | null;
}

export interface FieldError {
  field: string;
  message: string;
}

export const REDACTED = "••••";

/** GET /dashboard: one JSON blob per wired status provider. */
export interface DashboardData {
  sections: Record<string, unknown>;
  names: string[];
  setupComplete: boolean;
  setupSteps?: SetupStep[];
  version: string;
}

/** One resolver decision (internal/resolver.Trace). Durations are nanoseconds. */
export interface CandidateTrace {
  releaseId: string;
  name: string;
  indexer: string;
  score: number;
  rejected: boolean;
  reason?: string;
  healthChecked: boolean;
  healthy: boolean;
  fetchDuration: number;
  healthDuration: number;
  size?: number;
  /** The owner's pin for the title. */
  pinned?: boolean;
  customFormats?: { id: string; name: string; score: number }[] | null;
  compatibilityReason?: string;
  /** Why source routing moved this candidate. */
  route?: string;
}
export interface ResolverTrace {
  titleKey: string;
  titleName: string;
  profile: string;
  at: string;
  candidates: CandidateTrace[] | null;
  winner?: string;
  err?: string;
  searchDuration: number;
  /** The resolution excluded releases already tried (mid-play failover). */
  failover?: boolean;
  /** Pinned release id and what became of it ("used", "unhealthy: …"). */
  pinned?: string;
  pinStatus?: string;
  /** Candidates left out of a stored trace. */
  omitted?: number;
  /** The source-routing decision, e.g. "debrid first: popular title". */
  routing?: string;
}

/** A source's measured startup (NZB or payload fetch plus a real health check). */
export interface SourceStartup {
  protocol: "usenet" | "torrent";
  /** Exponentially weighted average, nanoseconds. */
  average: number;
  last: number;
  samples: number;
}
