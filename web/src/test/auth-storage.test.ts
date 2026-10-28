import { afterEach, describe, expect, it, vi } from "vitest";
import { apiFetch, getToken, setToken } from "../lib/api";

afterEach(() => {
  vi.restoreAllMocks();
  setToken("");
});

describe("owner credentials when browser storage is unavailable", () => {
  it("uses the supplied credential in memory and clears it on sign out", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    setToken("session-only-token");
    expect(getToken()).toBe("session-only-token");
    await apiFetch("/config");
    expect(new Headers(fetcher.mock.calls[0]?.[1]?.headers).get("X-Plex-Token")).toBe(
      "session-only-token",
    );
    setToken("");
    expect(getToken()).toBe("");
  });

  it("uses the new token if writes fail but reads still return an old one", () => {
    setToken("old-token");
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    setToken("new-token");
    expect(getToken()).toBe("new-token");
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    setToken("");
    expect(getToken()).toBe("");
  });
});
