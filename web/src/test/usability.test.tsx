import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };
async function choose(label: string, option: string) {
  fireEvent.click(screen.getByRole("button", { name: label }));
  fireEvent.click(await screen.findByRole("option", { name: option }));
}

describe("Owner usability", () => {
  it("edits client rules in a drawer using a device and named viewer", async () => {
    const { fake } = renderApp("/playback?tab=rules");
    expect(await screen.findByRole("button", { name: "Edit client rules" }, WAIT)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rule 1 device" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit client rules" }));
    const drawer = await screen.findByRole("dialog", { name: "Client rules" });
    expect(within(drawer).getByRole("button", { name: "Rule 1 device" })).toBeInTheDocument();
    await choose("Rule 1 device", "Plex Web");
    await choose("Rule 1 viewer", "The Kids");
    await choose("Rule 1 profile", "1080p");
    fireEvent.click(screen.getByRole("button", { name: "Save quality settings" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/quality")).toHaveLength(1));
    expect(calls(fake, "PUT", "/config/quality")[0]!.body).toMatchObject({ rules: [{ product: "Plex Web", userId: 3, profile: "1080p" }] });
  });

  it("discards unsaved provider edits when its drawer closes", async () => {
    const { fake } = renderApp("/settings/usenet");
    fireEvent.click((await screen.findAllByRole("button", { name: "Edit provider" }, WAIT))[0]!);
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "draft.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(screen.getAllByRole("button", { name: "Edit provider" })[0]!);
    expect(await screen.findByLabelText("Host")).toHaveValue("news.example.com");
    expect(calls(fake, "PUT", "/config/usenet")).toHaveLength(0);
  });

  it("persists provider removal from its drawer and retains remaining credentials", async () => {
    const { fake } = renderApp("/settings/usenet");
    fireEvent.click((await screen.findAllByRole("button", { name: "Edit provider" }, WAIT))[0]!);
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/usenet")).toHaveLength(1));
    expect(calls(fake, "PUT", "/config/usenet")[0]!.body).toMatchObject({ providers: [{ id: "p2", priority: 0, password: "••••" }] });
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Edit provider" })).toHaveLength(1));
  });

  it("closes a nested collection assistant with Escape while keeping the editor open", async () => {
    const { fake } = renderApp("/collections/new?draft=nested-usability");
    await screen.findByLabelText("Collection name", {}, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Describe it" }));
    await screen.findByRole("dialog", { name: "Describe a collection" });
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Describe a collection" })).not.toBeInTheDocument());
    expect(screen.getByRole("dialog", { name: "Collection Studio" })).toBeInTheDocument();
    expect(calls(fake, "POST", "/collections").filter(c => c.path === "/collections")).toHaveLength(0);
  });

  it("discards invalid scoring drafts before editing startup settings", async () => {
    const { fake } = renderApp("/playback?tab=scoring");
    fireEvent.click(await screen.findByRole("button", { name: "Edit custom formats" }, WAIT));
    fireEvent.change(screen.getByLabelText("Custom format definitions"), { target: { value: "unfinished JSON" } });
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole("tab", { name: "Playback start" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit startup behavior" }));
    fireEvent.click(screen.getByRole("button", { name: "Save startup settings" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/resolver")).toHaveLength(1));
    expect(calls(fake, "PUT", "/config/resolver")[0]!.body).toMatchObject({ customFormats: [] });
  });

  it("shows newest logs first, including entries sharing a timestamp", async () => {
    const { fake } = renderApp("/logs", {}, f => {
      f.state.logs = [
        { time: "2026-10-19T09:00:00Z", level: "info", message: "oldest" },
        { time: "2026-10-19T10:00:00Z", level: "info", message: "first at ten" },
        { time: "2026-10-19T10:00:00Z", level: "warn", message: "last at ten" },
      ];
    });
    const log = await screen.findByRole("log", { name: "Server logs" }, WAIT);
    await within(log).findByText("last at ten");
    expect(Array.from(log.querySelectorAll("time")).map(t => t.parentElement!.textContent)).toEqual([
      expect.stringContaining("last at ten"), expect.stringContaining("first at ten"), expect.stringContaining("oldest"),
    ]);
    fake.state.logs.push({ time: "2026-10-19T11:00:00Z", level: "info", message: "new arrival" });
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await within(log).findByText("new arrival");
    expect(log.querySelector("time")!.parentElement).toHaveTextContent("new arrival");
    expect(screen.getByLabelText("Live updates")).not.toBeChecked();
  });
});
