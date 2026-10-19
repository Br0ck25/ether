import type { Bootstrap, DiskInfo, EtherConfig, EtherUser, FieldError, Indexer, LogEntry, SetupStatus, TestResult, UsenetProvider } from "./types";

const TOKEN_KEY = "ether-admin-token";

export function getToken(): string {
  try { return localStorage.getItem(TOKEN_KEY) ?? ""; } catch { return ""; }
}
export function setToken(token: string): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch { /* private mode */ }
}

export class ApiError extends Error {
  status: number;
  code: string;
  fields?: FieldError[];
  constructor(status: number, code: string, message: string, fields?: FieldError[]) {
    super(message);
    this.status = status; this.code = code; this.fields = fields;
  }
}

const BASE = "/ether/api";

export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  const token = getToken();
  if (token) headers.set("X-Plex-Token", token);
  const method = (init.method ?? "GET").toUpperCase();
  if (method !== "GET" && method !== "HEAD") {
    headers.set("X-Ether-Request", "1");
    if (init.body != null && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  }
  let res: Response;
  try {
    res = await fetch(BASE + path, { ...init, headers });
  } catch (e) {
    throw new ApiError(0, "network", `Could not reach the Ether server: ${(e as Error).message}`);
  }
  const text = await res.text();
  let data: unknown = null;
  if (text) { try { data = JSON.parse(text); } catch { data = null; } }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; fields?: FieldError[] } } | null)?.error;
    throw new ApiError(res.status, err?.code ?? "http", err?.message ?? `Request failed (${res.status})`, err?.fields);
  }
  return data as T;
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return apiFetch<T>(path, { method: "POST", body: JSON.stringify(body ?? {}) });
}

export const api = {
  bootstrap: () => apiFetch<Bootstrap>("/bootstrap"),
  getConfig: async () => (await apiFetch<{ config: EtherConfig }>("/config")).config,
  putConfig: async (config: EtherConfig) => (await apiFetch<{ config: EtherConfig }>("/config", { method: "PUT", body: JSON.stringify(config) })).config,
  putSection: (section: string, value: unknown) =>
    apiFetch<{ section: string; value: unknown }>(`/config/${section}`, { method: "PUT", body: JSON.stringify(value) }),
  setupStatus: () => apiFetch<SetupStatus>("/setup/status"),
  setupComplete: (force = false) => post<SetupStatus>("/setup/complete", { force }),
  setupReopen: () => post<SetupStatus>("/setup/reopen"),
  pinStart: () => post<{ id: string; code: string; authUrl: string }>("/plex/pin"),
  signInStart: () => post<{ handle: string; code: string; authUrl: string }>("/auth/pin"),
  signInPoll: (handle: string) => apiFetch<{ done: boolean; token?: string }>(`/auth/pin/${encodeURIComponent(handle)}`),
  pinPoll: (id: string) => apiFetch<{ done: boolean }>(`/plex/pin/${encodeURIComponent(id)}`),
  claim: () => post<{ ok: boolean }>("/plex/claim"),
  plexStatus: () => apiFetch<Record<string, unknown>>("/plex/status"),
  testUsenet: (p: UsenetProvider) => post<TestResult>("/test/usenet", p),
  testIndexer: (ix: Indexer) => post<TestResult>("/test/indexer", ix),
  testTMDB: (key: string) => post<TestResult>("/test/tmdb", { key }),
  testOpenSubtitles: (cfg: EtherConfig["openSubtitles"]) => post<TestResult>("/test/opensubtitles", cfg),
  testLLM: (cfg: EtherConfig["llm"]) => post<TestResult>("/test/llm", cfg),
  testTrakt: (cfg: EtherConfig["trakt"]) => post<TestResult>("/test/trakt", cfg),
  dashboard: () => apiFetch<{ sections: Record<string, unknown>; names: string[]; setupComplete: boolean; version: string }>("/dashboard"),
  traces: () => apiFetch<{ traces: unknown }>("/traces"),
  disk: (path: string) => apiFetch<DiskInfo>(`/disk?path=${encodeURIComponent(path)}`),
  users: async () => (await apiFetch<{ users: EtherUser[] }>("/users")).users,
  updateUser: async (id: number, patch: Partial<EtherUser>) =>
    (await apiFetch<{ user: EtherUser }>(`/users/${id}`, { method: "PATCH", body: JSON.stringify(patch) })).user,
  logs: (params: { limit?: number; level?: string; q?: string }) => {
    const qs = new URLSearchParams();
    if (params.limit) qs.set("limit", String(params.limit));
    if (params.level) qs.set("level", params.level);
    if (params.q) qs.set("q", params.q);
    return apiFetch<{ entries: LogEntry[]; total: number }>(`/logs?${qs}`);
  },
  clearLogs: () => apiFetch<{ ok: boolean }>("/logs", { method: "DELETE" }),
};

export function newId(prefix: string): string {
  const rand = Math.random().toString(36).slice(2, 8);
  return `${prefix}-${rand}`;
}
