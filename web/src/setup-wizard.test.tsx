import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Setup, { canAdvance } from "./pages/Setup";
import type { EtherConfig, Indexer, UsenetProvider } from "./types";

const provider: UsenetProvider = {
  id: "p1", name: "Main", host: "news.example.com", port: 563, tls: true,
  username: "u", password: "pw", connections: 20, priority: 0, backfill: false, enabled: true,
};
const indexer: Indexer = {
  id: "i1", name: "Idx", url: "https://idx.example.com", apiKey: "k",
  dailyLimit: 0, dailyGrabs: 0, priority: 0, enabled: true, movieCategories: null, tvCategories: null,
};

describe("canAdvance gating", () => {
  const base = {
    plexDone: false, providers: [provider], indexers: [indexer],
    providerTests: {}, indexerTests: {}, skipped: {},
  };
  it("blocks the Plex step until sign-in completes", () => {
    expect(canAdvance(0, base)).toBe(false);
    expect(canAdvance(0, { ...base, plexDone: true })).toBe(true);
  });
  it("blocks providers until every enabled provider passes its test", () => {
    expect(canAdvance(1, base)).toBe(false);
    expect(canAdvance(1, { ...base, providerTests: { p1: { status: "fail" } } })).toBe(false);
    expect(canAdvance(1, { ...base, providerTests: { p1: { status: "ok" } } })).toBe(true);
    expect(canAdvance(1, { ...base, providers: [{ ...provider, enabled: false }] })).toBe(false);
    expect(canAdvance(1, { ...base, providers: [{ ...provider, host: "" }], providerTests: { p1: { status: "ok" } } })).toBe(false);
  });
  it("blocks indexers until tested, and the skip escape hatch always passes", () => {
    expect(canAdvance(2, base)).toBe(false);
    expect(canAdvance(2, { ...base, indexerTests: { i1: { status: "ok" } } })).toBe(true);
    expect(canAdvance(0, { ...base, skipped: { 0: true } })).toBe(true);
    expect(canAdvance(1, { ...base, skipped: { 1: true } })).toBe(true);
  });
  it("cache and optional steps never block", () => {
    expect(canAdvance(3, base)).toBe(true);
    expect(canAdvance(4, base)).toBe(true);
  });
});

function fakeConfig(): EtherConfig {
  return {
    server: { port: 32400, bindAddress: "0.0.0.0", friendlyName: "Ether", machineIdentifier: "m", logLevel: "info", defaultStreamLimit: 2, maxStreams: 0 },
    plex: { token: "••••", clientIdentifier: "c", ownerPlexId: 1, ownerUsername: "alice", claimed: true, remoteAccess: false, useUpnp: true, publicPort: 0, publicHost: "", customUrls: null, certFile: "", keyFile: "", certHash: "", secureConnections: "preferred" },
    usenet: { providers: [provider] },
    indexers: [indexer],
    cache: { dir: "/tmp/cache", budgetBytes: 50 << 30, prefetchNextEpisode: true, prefetchBytes: 1024, keepInProgressWarm: true, nextEpisodeAt: 0.8 },
    catalog: { tmdbKey: "", language: "en-US", region: "US", minPopularity: 5, minVotes: 100, includeAdult: false, refreshInterval: "24h", showUnreleased: true },
    resolver: { allowCam: false, healthSamples: 12, searchCacheTtl: "6h", unavailableCooloff: "24h", maxCandidates: 3, blockedGroups: null, blockedTerms: null },
    quality: { profiles: [], rules: [], default: "", remote: "" },
    transcode: { ffmpegPath: "", ffprobePath: "", hwAccel: "auto", vaapiDevice: "", tempDir: "/tmp/t", maxTranscodes: 2, remoteMaxBitrateKbps: 8000 },
    trakt: { enabled: false, clientId: "", clientSecret: "" },
    openSubtitles: { enabled: false, apiKey: "", username: "", password: "" },
    llm: { enabled: false, baseUrl: "", apiKey: "", model: "" },
    updates: { channel: "stable", autoUpdate: false, feedUrl: "" },
    spike: { enabled: false, localFiles: null },
    setupComplete: false,
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("Setup wizard UI flow", () => {
  beforeEach(() => {
    const cfg = fakeConfig();
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith("/config") && (!init?.method || init.method === "GET")) return jsonResponse({ config: cfg });
      if (url.endsWith("/config") && init?.method === "PUT") return jsonResponse({ config: cfg });
      if (url.includes("/test/usenet")) return jsonResponse({ ok: true, durationMs: 12 });
      if (url.includes("/disk")) return jsonResponse({ path: "/tmp/cache", probedPath: "/tmp", totalBytes: 1000, freeBytes: 500, usedBytes: 500, cacheBudgetBytes: 0 });
      return jsonResponse({});
    }));
  });
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it("walks from a claimed Plex step to providers and gates on the provider test", async () => {
    render(<MemoryRouter><Setup /></MemoryRouter>);
    expect(await screen.findByText(/Signed in as alice/)).toBeInTheDocument();
    const next = screen.getByRole("button", { name: /Save and continue/ });
    expect(next).not.toBeDisabled(); // Plex step already claimed

    fireEvent.click(next);
    expect(await screen.findByText("Usenet providers", { selector: "h2" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Save and continue/ })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    await waitFor(() => expect(screen.getByText(/OK · 12 ms/)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: /Save and continue/ })).not.toBeDisabled();
  });

  it("skip-test checkbox unblocks an untested step", async () => {
    render(<MemoryRouter><Setup /></MemoryRouter>);
    await screen.findByText(/Signed in as alice/);
    fireEvent.click(screen.getByRole("button", { name: /Save and continue/ }));
    await screen.findByText("Usenet providers", { selector: "h2" });
    expect(screen.getByRole("button", { name: /Save and continue/ })).toBeDisabled();
    fireEvent.click(screen.getByLabelText(/Skip test for this step/));
    expect(screen.getByRole("button", { name: /Save and continue/ })).not.toBeDisabled();
  });
});
