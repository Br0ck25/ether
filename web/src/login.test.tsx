import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { api, setToken } from "./api";

const boot = { ok: true, version: "dev", setupComplete: true, authenticated: false, authRequired: true, signIn: true, serverName: "Ether" };

afterEach(() => { cleanup(); vi.restoreAllMocks(); setToken(""); });

function renderApp() {
  return render(<MemoryRouter><App /></MemoryRouter>);
}

describe("login screen", () => {
  it("offers Sign in with Plex, with the token form tucked away", async () => {
    vi.spyOn(api, "bootstrap").mockResolvedValue(boot);
    renderApp();
    expect(await screen.findByRole("button", { name: "Sign in with Plex" })).toBeTruthy();
    expect(screen.getByText("Use a Plex token instead")).toBeTruthy();
  });

  it("stores the owner token once plex.tv approves", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(api, "bootstrap").mockResolvedValue(boot);
    vi.spyOn(api, "signInStart").mockResolvedValue({ handle: "h1", code: "ABCD", authUrl: "https://app.plex.tv/auth#?code=ABCD" });
    const poll = vi.spyOn(api, "signInPoll")
      .mockResolvedValueOnce({ done: false })
      .mockResolvedValueOnce({ done: true, token: "owner-token" });
    vi.spyOn(window, "open").mockReturnValue(null);
    vi.spyOn(api, "dashboard").mockResolvedValue({} as never);
    renderApp();
    fireEvent.click(await screen.findByRole("button", { name: "Sign in with Plex" }));
    // Popup blocked: offer the sign-in page as a link.
    expect((await screen.findByRole("link", { name: "Open the Plex sign-in page" })).getAttribute("href")).toContain("app.plex.tv/auth");
    await vi.advanceTimersByTimeAsync(4500);
    // Approved: the login screen gives way to the app.
    await waitFor(() => expect(screen.queryByRole("button", { name: /Sign in with Plex|Waiting for Plex/ })).toBeNull());
    expect(poll).toHaveBeenCalledTimes(2);
    expect(poll).toHaveBeenCalledWith("h1");
    vi.useRealTimers();
  });

  it("is skipped on the server machine itself", async () => {
    vi.spyOn(api, "bootstrap").mockResolvedValue({ ...boot, authenticated: true, authRequired: false, localTrusted: true });
    vi.spyOn(api, "dashboard").mockResolvedValue({} as never);
    renderApp();
    await waitFor(() => expect(screen.queryByRole("button", { name: "Sign in with Plex" })).toBeNull());
    expect((await screen.findAllByText("Dashboard")).length).toBeGreaterThan(0);
  });
});
