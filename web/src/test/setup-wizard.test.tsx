import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { createFakeServer } from "../mock/server";
import Setup, { canAdvance } from "../pages/Setup";
import type { Indexer, UsenetProvider } from "../lib/types";
import { calls, renderApp } from "./harness";

const provider: UsenetProvider = {
  id: "p1", name: "Main", host: "news.example.com", port: 563, tls: true,
  username: "u", password: "pw", connections: 20, priority: 0, backfill: false, enabled: true,
};
const indexer: Indexer = {
  id: "i1", name: "Idx", url: "https://idx.example.com", apiKey: "k",
  dailyLimit: 0, dailyGrabs: 0, priority: 0, enabled: true, movieCategories: null, tvCategories: null,
};

describe("canAdvance gating", () => {
  const base = { plexDone: false, providers: [provider], indexers: [indexer], providerTests: {}, indexerTests: {}, skipped: {} };
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
  it("blocks indexers until tested; the skip escape hatch always passes", () => {
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

function renderWizard() {
  const fake = createFakeServer();
  fake.state.config.setupComplete = false;
  fake.state.config.plex.ownerUsername = "alice";
  vi.stubGlobal("fetch", vi.fn(fake.fetch));
  render(<MemoryRouter><Setup /></MemoryRouter>);
  return fake;
}

describe("setup wizard", () => {
  it("walks from a claimed Plex step to providers and gates on the provider test", async () => {
    const fake = renderWizard();
    expect(await screen.findByText(/Signed in as alice/)).toBeInTheDocument();
    const next = screen.getByRole("button", { name: "Save and continue" });
    expect(next).toBeEnabled();
    fireEvent.click(next);
    expect(await screen.findByRole("heading", { name: "Usenet providers" })).toBeInTheDocument();
    expect(calls(fake, "PUT", "/config")).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Save and continue" })[0]).toBeDisabled();
    fireEvent.click(screen.getAllByRole("button", { name: /^Test$/ })[0]!);
    await waitFor(() => expect(screen.getAllByText(/OK · \d+ ms/).length).toBeGreaterThan(0));
    // Both seeded providers must pass before continuing.
    expect(screen.getByRole("button", { name: "Save and continue" })).toBeDisabled();
    fireEvent.click(screen.getAllByRole("button", { name: /^Test$/ })[1]!);
    await waitFor(() => expect(screen.getByRole("button", { name: "Save and continue" })).toBeEnabled());
  });

  it("the skip-test checkbox unblocks an untested step", async () => {
    renderWizard();
    await screen.findByText(/Signed in as alice/);
    fireEvent.click(screen.getByRole("button", { name: "Save and continue" }));
    await screen.findByRole("heading", { name: "Usenet providers" });
    expect(screen.getByRole("button", { name: "Save and continue" })).toBeDisabled();
    fireEvent.click(screen.getByLabelText(/Skip test for this step/));
    expect(screen.getByRole("button", { name: "Save and continue" })).toBeEnabled();
  });

  it("an unfinished setup sends the owner to the wizard instead of Home", async () => {
    renderApp("/", { bootstrap: { setupComplete: false } });
    expect(await screen.findByRole("heading", { name: "Set up Ether" })).toBeInTheDocument();
    expect(screen.getAllByText("Finish setup").length).toBeGreaterThan(0);
  });
});
