import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { normalizeTraces } from "../pages/Traces";
import type { UsenetProvider } from "../lib/types";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

describe("Home dashboard", () => {
  it("shows now playing, health, activity and best rows", async () => {
    renderApp("/");
    expect(await screen.findByText(/Sam Rivera · Living Room/, {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText("Direct play")).toBeInTheDocument();
    expect(screen.getByText(/% watched/)).toBeInTheDocument();
    const health = screen.getByText("Health").closest("section")!;
    for (const label of [
      "Usenet",
      "Indexers",
      "Cache",
      "Catalogue",
      "Remote access",
      "plex.direct",
    ]) {
      expect(within(health).getByText(label)).toBeInTheDocument();
    }
    expect(
      await screen.findByRole("img", { name: /Plays per day over the last 30 days/ }, WAIT),
    ).toBeInTheDocument();
    expect(screen.getByText("Plays · 30 days")).toBeInTheDocument();
    expect(await screen.findByText("Best-performing rows")).toBeInTheDocument();
    // The header health indicator summarises everything.
    expect(screen.getByRole("button", { name: /Server health/ })).toBeInTheDocument();
  });

  it("degrades gracefully on servers without the insights API", async () => {
    renderApp("/", {}, (fake) => {
      const original = fake.fetch;
      fake.fetch = async (input, init) =>
        String(input).includes("/insights/")
          ? new Response(
              JSON.stringify({ error: { code: "not_found", message: "no such API endpoint" } }),
              { status: 404 },
            )
          : original(input, init);
    });
    expect(
      await screen.findByText(/^Activity insights comes with a newer version of Ether/, {}, WAIT),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/^Row insights comes with a newer version of Ether/),
    ).toBeInTheDocument();
    // Live status still works.
    expect(await screen.findByText(/Sam Rivera · Living Room/, {}, WAIT)).toBeInTheDocument();
  });
});

describe("Settings", () => {
  it("round-trips redacted secrets and saves one section", async () => {
    const { fake } = renderApp("/settings/usenet");
    expect(
      await screen.findByRole("heading", { name: "Usenet providers" }, WAIT),
    ).toBeInTheDocument();
    fireEvent.click((await screen.findAllByRole("button", { name: "Edit provider" }))[0]!);
    const host = (await screen.findAllByLabelText("Host"))[0]!;
    fireEvent.change(host, { target: { value: "eu.news.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: /Save changes/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/usenet")).toHaveLength(1), WAIT);
    const body = calls(fake, "PUT", "/config/usenet")[0]!.body as { providers: UsenetProvider[] };
    expect(body.providers[0]).toMatchObject({
      host: "eu.news.example.com",
      password: "••••",
      priority: 0,
    });
    expect(body.providers[1]).toMatchObject({ password: "••••", priority: 1, backfill: true });
  });

  it("shows server validation errors next to the field", async () => {
    const { fake } = renderApp("/settings/server");
    fireEvent.click(await screen.findByRole("button", { name: "Edit server" }));
    const name = await screen.findByLabelText("Server name", {}, WAIT);
    const original = fake.fetch;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).endsWith("/config/server") && init?.method === "PUT") {
          return new Response(
            JSON.stringify({
              error: {
                code: "validation",
                message: "configuration is invalid",
                fields: [{ field: "server.friendlyName", message: "is required" }],
              },
            }),
            { status: 400 },
          );
        }
        return original(input, init);
      }),
    );
    fireEvent.change(name, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: /Save changes/ }));
    expect(
      (await screen.findAllByText("Please fix the highlighted fields", {}, WAIT)).length,
    ).toBeGreaterThan(0);
    expect(screen.getAllByText("is required").length).toBeGreaterThan(0);
  });

  it("offers config sections the UI has no dedicated form for, and saves them whole", async () => {
    const { fake } = renderApp("/settings/x-personalize", {}, (f) => {
      f.state.config = { ...f.state.config, personalize: { enabled: true, explore: 0.2 } } as never;
    });
    expect(await screen.findByRole("heading", { name: "Personalize" }, WAIT)).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Edit personalize" }));
    fireEvent.change(await screen.findByLabelText("Explore"), { target: { value: "0.4" } });
    fireEvent.click(screen.getByRole("button", { name: /Save changes/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/config")).toHaveLength(1), WAIT);
    expect(
      (calls(fake, "PUT", "/config")[0]!.body as { personalize: unknown }).personalize,
    ).toEqual({ enabled: true, explore: 0.4 });
  });
});

describe("Resolver traces", () => {
  it("normalizes typed and loosely shaped traces", () => {
    const t = normalizeTraces([
      {
        titleName: "Dune",
        candidates: [{ name: "Dune.1080p", score: 5, rejected: true, reason: "too big" }],
        err: "no luck",
      },
      "junk",
    ]);
    expect(t).toHaveLength(1);
    expect(t[0]).toMatchObject({ titleName: "Dune", err: "no luck" });
    expect(t[0]!.candidates![0]).toMatchObject({
      name: "Dune.1080p",
      rejected: true,
      reason: "too big",
    });
  });

  it("lists traces and opens one in a drawer", async () => {
    renderApp("/traces");
    const row = await screen.findByText(/\(\d{4}\)/, {}, WAIT);
    fireEvent.click(row);
    const drawer = await screen.findByRole("dialog");
    expect(within(drawer).getByText("CAM releases are disabled")).toBeInTheDocument();
    expect(within(drawer).getAllByText("Picked").length).toBeGreaterThan(0);
  });
});

describe("Updates settings", () => {
  it("shows update status and asks the server to install", async () => {
    const { fake } = renderApp("/settings/updates");
    expect(await screen.findByText(/Version 0\.3\.0/, {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText(/03:00–06:00 local time/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    expect(
      await screen.findByRole("button", { name: "Install now anyway" }, WAIT),
    ).toBeInTheDocument();
    expect(calls(fake, "POST", "/updates/apply").length).toBe(1);
  });
});
