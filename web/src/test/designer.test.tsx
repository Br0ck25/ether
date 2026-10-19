import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LayoutRule } from "../lib/contract";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

function savedRules(fake: Parameters<typeof calls>[0]): LayoutRule[] {
  const put = calls(fake, "PUT", "/layout").at(-1);
  return (put?.body as { rules: LayoutRule[] }).rules;
}

describe("Home Designer", () => {
  it("renders a viewer's Home as Plex rows with reasons and scores", async () => {
    const { fake } = renderApp("/designer");
    const row = await screen.findByRole("region", { name: /Row \d+: Top 10 for you/ }, WAIT);
    expect(within(row).getByText(/Ranked by predicted enjoyment/)).toBeInTheDocument();
    expect(within(row).getByText(/score 0\.91/)).toBeInTheDocument();
    expect(within(row).getAllByRole("listitem").length).toBe(10);
    const home = calls(fake, "GET", "/viewers/1/home")[0]!;
    expect(home.path).toMatch(/device=tv/);
    expect(home.path).toMatch(/at=\d{4}-\d{2}-\d{2}T/);
  });

  it("hides a row server-wide and saves the layout rule", async () => {
    const { fake } = renderApp("/designer");
    await screen.findByRole("region", { name: /Top 10 for you/ }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Hide: Top 10 for you" }));
    // The preview reflects the unsaved change immediately.
    await waitFor(() => expect(screen.queryByRole("region", { name: /Top 10 for you/ })).toBeNull());
    expect(screen.getByText("Hidden in this context")).toBeInTheDocument();
    expect(screen.getByText("Previewing unsaved changes")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Save layout/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/layout")).toHaveLength(1), WAIT);
    const everyone = savedRules(fake).find((r) => r.scope === "all" && (r.section ?? "home") === "home")!;
    expect(everyone.rows).toContainEqual({ hubId: "top-10", action: "hide" });
    // The pin that was already there is kept.
    expect(everyone.rows).toContainEqual({ hubId: "continue-watching", action: "pin", position: 0 });
  });

  it("scopes row changes to one viewer and schedules them", async () => {
    const { fake } = renderApp("/designer");
    await screen.findByRole("region", { name: /Hidden gems for you/ }, WAIT);
    fireEvent.click(screen.getByRole("radio", { name: /Only Alex Rivera/ }));
    fireEvent.click(screen.getByRole("button", { name: "Boost: Hidden gems for you" }));

    // The new per-viewer rule appears in the rules panel; give it a schedule.
    const rule = await screen.findByRole("region", { name: "Layout rule 3" });
    expect(within(rule).getByText(/Alex Rivera · Home/)).toBeInTheDocument();
    fireEvent.click(within(rule).getByRole("button", { name: /Always/ }));
    fireEvent.click(within(rule).getByRole("button", { name: "Sat" }));
    fireEvent.click(within(rule).getByRole("button", { name: "Sun" }));

    fireEvent.click(screen.getByRole("button", { name: /Save layout/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/layout")).toHaveLength(1), WAIT);
    const mine = savedRules(fake).find((r) => r.scope !== "all" && r.scope.userIds.includes(1))!;
    expect(mine.section).toBe("home");
    expect(mine.rows).toEqual([{ hubId: "hidden-gems", action: "boost", when: { days: [0, 6] } }]);
  });

  it("discards unsaved layout edits", async () => {
    const { fake } = renderApp("/designer");
    await screen.findByRole("region", { name: /Comfort rewatches/ }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Bury: Comfort rewatches" }));
    expect(await screen.findByText("Unsaved layout")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Discard/ }));
    await waitFor(() => expect(screen.queryByText("Unsaved layout")).toBeNull());
    expect(calls(fake, "PUT", "/layout")).toHaveLength(0);
  });
});
