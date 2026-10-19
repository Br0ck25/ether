import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };
describe("Owner usability", () => {
  it("closes a nested collection assistant with Escape while keeping the editor open", async () => {
    const { fake } = renderApp("/collections/new?draft=nested-usability");
    await screen.findByLabelText("Collection name", {}, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Describe it" }));
    await screen.findByRole("dialog", { name: "Describe a collection" });
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Describe a collection" }),
      ).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("region", { name: "Collection Studio" })).toBeInTheDocument();
    expect(screen.getByLabelText("Collection name")).toBeInTheDocument();
    expect(
      calls(fake, "POST", "/collections").filter((c) => c.path === "/collections"),
    ).toHaveLength(0);
  });

  it("shows newest logs first, including entries sharing a timestamp", async () => {
    const { fake } = renderApp("/logs", {}, (f) => {
      f.state.logs = [
        { time: "2026-10-19T09:00:00Z", level: "info", message: "oldest" },
        { time: "2026-10-19T10:00:00Z", level: "info", message: "first at ten" },
        { time: "2026-10-19T10:00:00Z", level: "warn", message: "last at ten" },
      ];
    });
    const log = await screen.findByRole("log", { name: "Server logs" }, WAIT);
    await within(log).findByText("last at ten");
    expect(
      Array.from(log.querySelectorAll("time")).map((t) => t.parentElement!.textContent),
    ).toEqual([
      expect.stringContaining("last at ten"),
      expect.stringContaining("first at ten"),
      expect.stringContaining("oldest"),
    ]);
    fake.state.logs.push({ time: "2026-10-19T11:00:00Z", level: "info", message: "new arrival" });
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await within(log).findByText("new arrival");
    expect(log.querySelector("time")!.parentElement).toHaveTextContent("new arrival");
    expect(screen.getByLabelText("Live updates")).not.toBeChecked();
  });
});
