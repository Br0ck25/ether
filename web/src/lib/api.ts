import type {
  ActivityDay,
  CollectionPreview,
  CollectionSpec,
  CollectionTemplate,
  FacetName,
  FacetValue,
  HomeLayout,
  LayoutDoc,
  LayoutImpactResult,
  PreviewContext,
  LookupKind,
  LookupResult,
  RowInsight,
  TasteOverride,
  TasteProfile,
  TitleCard,
  TitleDetail,
  Viewer,
  ViewerDetail,
  ViewerHome,
} from "./contract";
import type {
  Bootstrap,
  DashboardData,
  DiskInfo,
  EtherConfig,
  EtherUser,
  FieldError,
  Indexer,
  LogEntry,
  SetupStatus,
  TestResult,
  UsenetProvider,
} from "./types";

const TOKEN_KEY = "ether-admin-token";

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}
export function setToken(token: string): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* private mode: the session simply won't persist */
  }
}

export class ApiError extends Error {
  status: number;
  code: string;
  fields?: FieldError[];
  constructor(status: number, code: string, message: string, fields?: FieldError[]) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}

export const API_BASE = "/ether/api";

function authHeaders(init: RequestInit): Headers {
  const headers = new Headers(init.headers);
  const token = getToken();
  if (token) headers.set("X-Plex-Token", token);
  const method = (init.method ?? "GET").toUpperCase();
  if (method !== "GET" && method !== "HEAD") {
    headers.set("X-Ether-Request", "1");
    if (init.body != null && !headers.has("Content-Type"))
      headers.set("Content-Type", "application/json");
  }
  return headers;
}

export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  let res: Response;
  try {
    res = await fetch(API_BASE + path, { ...init, headers: authHeaders(init) });
  } catch (e) {
    if (isAbort(e)) throw e;
    throw new ApiError(0, "network", `Could not reach the Ether server: ${(e as Error).message}`);
  }
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const err = (
      data as { error?: { code?: string; message?: string; fields?: FieldError[] } } | null
    )?.error;
    throw new ApiError(
      res.status,
      err?.code ?? "http",
      err?.message ?? `Request failed (${res.status})`,
      err?.fields,
    );
  }
  return data as T;
}

let routeImagesThroughFetch = false;

/** Load /ether/api images through fetch() even without a token (mock mode). */
export function routeApiImagesThroughFetch(on: boolean): void {
  routeImagesThroughFetch = on;
}

/** True when an image URL must be fetched with headers rather than put in an <img>. */
export function imageNeedsFetch(src: string | null | undefined): boolean {
  return Boolean(src && src.startsWith(API_BASE) && (routeImagesThroughFetch || getToken()));
}

/** Fetch an authenticated binary resource (artwork) as a Blob. */
export async function apiBlob(url: string, signal?: AbortSignal): Promise<Blob> {
  const res = await fetch(url, { headers: authHeaders({}), signal });
  if (!res.ok) throw new ApiError(res.status, "http", `Image request failed (${res.status})`);
  return res.blob();
}

function send<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return apiFetch<T>(path, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
}
const post = <T>(path: string, body?: unknown, signal?: AbortSignal) =>
  send<T>("POST", path, body ?? {}, signal);
const put = <T>(path: string, body: unknown) => send<T>("PUT", path, body);

function qs(params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params))
    if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
  const s = q.toString();
  return s ? `?${s}` : "";
}

export type TestFn = () => Promise<TestResult>;

export const api = {
  // ---- Bootstrap, sign-in, setup -----------------------------------------
  bootstrap: () => apiFetch<Bootstrap>("/bootstrap"),
  signInStart: () => post<{ handle: string; code: string; authUrl: string }>("/auth/pin"),
  signInPoll: (handle: string) =>
    apiFetch<{ done: boolean; token?: string }>(`/auth/pin/${encodeURIComponent(handle)}`),
  setupStatus: () => apiFetch<SetupStatus>("/setup/status"),
  setupComplete: (force = false) => post<SetupStatus>("/setup/complete", { force }),
  setupReopen: () => post<SetupStatus>("/setup/reopen"),
  pinStart: () => post<{ id: string; code: string; authUrl: string }>("/plex/pin"),
  pinPoll: (id: string) => apiFetch<{ done: boolean }>(`/plex/pin/${encodeURIComponent(id)}`),
  claim: () => post<{ ok: boolean }>("/plex/claim"),

  // ---- Config ---------------------------------------------------------------
  getConfig: async () => (await apiFetch<{ config: EtherConfig }>("/config")).config,
  putConfig: async (config: EtherConfig) =>
    (await put<{ config: EtherConfig }>("/config", config)).config,
  putSection: (section: string, value: unknown) =>
    put<{ section: string; value: unknown }>(`/config/${section}`, value),

  // ---- Integration tests ----------------------------------------------------
  testUsenet: (p: UsenetProvider) => post<TestResult>("/test/usenet", p),
  testIndexer: (ix: Indexer) => post<TestResult>("/test/indexer", ix),
  testTMDB: (key: string) => post<TestResult>("/test/tmdb", { key }),
  testOpenSubtitles: (cfg: EtherConfig["openSubtitles"]) =>
    post<TestResult>("/test/opensubtitles", cfg),
  testLLM: (cfg: EtherConfig["llm"]) => post<TestResult>("/test/llm", cfg),
  testTrakt: (cfg: EtherConfig["trakt"]) => post<TestResult>("/test/trakt", cfg),

  // ---- Status ----------------------------------------------------------------
  dashboard: () => apiFetch<DashboardData>("/dashboard"),
  disk: (path: string) => apiFetch<DiskInfo>(`/disk${qs({ path })}`),
  logs: (params: { limit?: number; level?: string; q?: string }) =>
    apiFetch<{ entries: LogEntry[]; total: number }>(`/logs${qs(params)}`),
  clearLogs: () => apiFetch<{ ok: boolean }>("/logs", { method: "DELETE" }),

  // ---- Users (limits) ----------------------------------------------------------
  users: async () => (await apiFetch<{ users: EtherUser[] | null }>("/users")).users ?? [],
  updateUser: async (id: number, patch: Partial<EtherUser>) =>
    (await send<{ user: EtherUser }>("PATCH", `/users/${id}`, patch)).user,

  // ---- Collection Studio ------------------------------------------------------
  collections: async () =>
    (await apiFetch<{ collections: CollectionSpec[] | null }>("/collections")).collections ?? [],
  collection: (id: string) => apiFetch<CollectionSpec>(`/collections/${encodeURIComponent(id)}`),
  createCollection: (spec: CollectionSpec) => post<CollectionSpec>("/collections", spec),
  updateCollection: (id: string, spec: CollectionSpec) =>
    put<CollectionSpec>(`/collections/${encodeURIComponent(id)}`, spec),
  deleteCollection: (id: string) =>
    apiFetch<null>(`/collections/${encodeURIComponent(id)}`, { method: "DELETE" }),
  previewCollection: (spec: CollectionSpec, signal?: AbortSignal) =>
    post<CollectionPreview>("/collections/preview", spec, signal),
  rebuildCollection: (id: string) =>
    post<{ itemCount: number; lastBuiltAt: string }>(
      `/collections/${encodeURIComponent(id)}/rebuild`,
    ),
  templates: async () =>
    (await apiFetch<{ templates: CollectionTemplate[] | null }>("/collections/templates"))
      .templates ?? [],
  generateCollection: (prompt: string, mediaType?: string) =>
    post<{ spec: CollectionSpec; explanation: string }>("/collections/generate", {
      prompt,
      mediaType,
    }),
  artworkUrl: (id: string, w: number, version?: string) =>
    `${API_BASE}/collections/${encodeURIComponent(id)}/artwork${qs({ w, v: version })}`,

  // ---- Catalogue helpers ---------------------------------------------------------
  searchCatalog: async (q: string, type?: string, limit = 24, signal?: AbortSignal) =>
    (
      await apiFetch<{ items: TitleCard[] | null }>(`/catalog/search${qs({ q, type, limit })}`, {
        signal,
      })
    ).items ?? [],
  facets: async (type: string, facet: FacetName) =>
    (await apiFetch<{ values: FacetValue[] | null }>(`/catalog/facets${qs({ type, facet })}`))
      .values ?? [],
  lookup: async (kind: LookupKind, q: string, region?: string, signal?: AbortSignal) =>
    (
      await apiFetch<{ results: LookupResult[] | null }>(
        `/catalog/lookup${qs({ kind, q, region })}`,
        { signal },
      )
    ).results ?? [],
  title: (key: string) => apiFetch<TitleDetail>(`/catalog/title/${encodeURIComponent(key)}`),

  // ---- Viewers -------------------------------------------------------------------
  viewers: async () => (await apiFetch<{ viewers: Viewer[] | null }>("/viewers")).viewers ?? [],
  viewer: (id: number) => apiFetch<ViewerDetail>(`/viewers/${id}`),
  taste: (id: number) => apiFetch<TasteProfile>(`/viewers/${id}/taste`),
  putOverrides: (id: number, overrides: TasteOverride[]) =>
    put<TasteProfile>(`/viewers/${id}/taste/overrides`, { overrides }),
  viewerHome: (
    id: number,
    ctx: { device: string; at: string; section?: string },
    signal?: AbortSignal,
  ) => apiFetch<ViewerHome>(`/viewers/${id}/home${qs(ctx)}`, { signal }),
  refreshViewer: (id: number) => post<null>(`/viewers/${id}/refresh`),

  // ---- Home Designer -----------------------------------------------------------
  layout: () => apiFetch<LayoutDoc>("/layout"),
  putLayout: (layout: HomeLayout) => put<LayoutDoc>("/layout", { layout }),
  /** The Plex Home a viewer (null = the typical viewer) gets with a draft layout (omitted = saved). */
  previewLayout: (
    body: PreviewContext & { viewerId: number | null; layout?: HomeLayout; count?: number },
    signal?: AbortSignal,
  ) => post<ViewerHome>("/layout/preview", body, signal),
  layoutImpact: (body: PreviewContext & { layout: HomeLayout }, signal?: AbortSignal) =>
    post<LayoutImpactResult>("/layout/impact", body, signal),

  // ---- Insights ---------------------------------------------------------------------
  rowInsights: async () =>
    (await apiFetch<{ rows: RowInsight[] | null }>("/insights/rows")).rows ?? [],
  activity: async () =>
    (await apiFetch<{ days: ActivityDay[] | null }>("/insights/activity")).days ?? [],
};

export function newId(prefix: string): string {
  const rand = Math.random().toString(36).slice(2, 8);
  return `${prefix}-${rand}`;
}

/** Turn any thrown value into a sentence for the owner. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return "Your session has expired. Sign in again.";
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return String(err);
}
