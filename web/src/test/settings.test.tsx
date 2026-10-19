import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

describe("Settings", () => {
  it("round-trips redacted secrets and saves only the changed service", async () => {
    const { fake } = renderApp("/services", {}, (f) => {
      f.state.config.openSubtitles = {
        enabled: true,
        apiKey: "••••",
        username: "sam",
        password: "••••",
      };
    });
    expect(
      await screen.findByRole("heading", { name: "Connected services" }, WAIT),
    ).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("Username", {}, WAIT), {
      target: { value: "sam.rivera" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Save changes/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/opensubtitles")).toHaveLength(1), WAIT);
    expect(calls(fake, "PUT", "/config/opensubtitles")[0]!.body).toEqual({
      enabled: true,
      apiKey: "••••",
      username: "sam.rivera",
      password: "••••",
    });
    expect(calls(fake, "PUT", "/config/trakt")).toHaveLength(0);
    expect(calls(fake, "PUT", "/config/llm")).toHaveLength(0);
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull(),
    );
  });

  it("lists every other integration with a link to where it is configured", async () => {
    renderApp("/services");
    const directory = (await screen.findByText("Other integrations", {}, WAIT)).closest("section")!;
    for (const [name, href] of [
      ["Metadata › Catalogue", "/metadata/catalogue"],
      ["Metadata › Ratings", "/metadata/ratings"],
      ["Playback › Markers", "/playback/markers"],
      ["Sources › Indexers", "/sources/indexers"],
      ["Plex & remote access", "/remote-access"],
    ])
      expect(within(directory).getByRole("link", { name })).toHaveAttribute("href", href);
    expect(screen.getByRole("button", { name: "Test Trakt" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Test endpoint" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Test OpenSubtitles" })).toBeInTheDocument();
  });

  it("shows server validation errors next to the field", async () => {
    const { fake } = renderApp("/server");
    const name = await screen.findByLabelText("Server name", {}, WAIT);
    expect(screen.queryByRole("button", { name: /^Edit / })).toBeNull();
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

  it("lists config sections the UI has no page for on General, and saves them whole", async () => {
    const { fake } = renderApp("/server", {}, (f) => {
      f.state.config = { ...f.state.config, personalize: { enabled: true, explore: 0.2 } } as never;
    });
    expect(await screen.findByRole("heading", { name: "Personalize" }, WAIT)).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("Explore", {}, WAIT), {
      target: { value: "0.4" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Save changes/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/config")).toHaveLength(1), WAIT);
    expect(
      (calls(fake, "PUT", "/config")[0]!.body as { personalize: unknown }).personalize,
    ).toEqual({ enabled: true, explore: 0.4 });
  });

  it("puts every server page in the main sidebar, with no separate settings menu", async () => {
    renderApp("/server");
    await screen.findByRole("heading", { name: "General" }, WAIT);
    const sidebar = (name: string) => screen.queryAllByRole("link", { name });
    for (const name of [
      "General",
      "Plex & remote access",
      "Connected services",
      "Notifications",
      "Backups & import",
      "Updates",
      "Troubleshooting",
      "Logs",
    ])
      expect(sidebar(name).length).toBeGreaterThan(0);
    expect(sidebar("Settings")).toHaveLength(0);
    // Config keys owned by other pages never show up as extra sections here.
    for (const name of ["Usenet", "Indexers", "Cache", "Resolver", "Quality", "Catalog", "Markers"])
      expect(screen.queryByRole("heading", { name })).toBeNull();
  });

  it("labels local file playback as a troubleshooting tool", async () => {
    renderApp("/troubleshooting");
    expect(
      await screen.findByRole("heading", { name: "Test playback with a local file" }, WAIT),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Serve test files")).toBeInTheDocument();
  });
});
