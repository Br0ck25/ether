import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { HomeLayout } from "../lib/contract";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

type PreviewBody = {
  viewerId: number | null;
  layout?: HomeLayout;
  device: string;
  at: string;
  section: string;
};

function lastPreview(fake: Parameters<typeof calls>[0]): PreviewBody {
  return calls(fake, "POST", "/layout/preview").at(-1)!.body as PreviewBody;
}
function savedLayout(fake: Parameters<typeof calls>[0]): HomeLayout {
  return (calls(fake, "PUT", "/layout").at(-1)!.body as { layout: HomeLayout }).layout;
}
async function closeEditor() {
  const dialog = screen.getByRole("dialog");
  fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
}
async function setStatus(title: string, value: "Always" | "Eligible" | "Off") {
  fireEvent.click(screen.getByRole("tab", { name: "Rows" }));
  fireEvent.click(screen.getByRole("button", { name: `Constraints for ${title}` }));
  const editor = await screen.findByRole("dialog", { name: title });
  fireEvent.click(
    within(within(editor).getByRole("radiogroup", { name: `Status of ${title}` })).getByRole(
      "radio",
      { name: value },
    ),
  );
  await closeEditor();
}

describe("Home screens", () => {
  it("previews the typical viewer's Global Home with each row's provenance", async () => {
    const { fake } = renderApp("/home-screens");
    const cw = await screen.findByRole("region", { name: "Slot 1: Continue Watching" }, WAIT);
    expect(within(cw).getByText(/Global anchor/)).toBeInTheDocument();
    expect(within(cw).getByText(/global anchor at slot 1/)).toBeInTheDocument();
    const top = screen.getByRole("region", { name: /Slot \d+: Top 10 on Living Room Ether Today/ });
    expect(within(top).getByText("Personalized")).toBeInTheDocument();
    expect(within(top).getByText(/Chosen by personalization/)).toBeInTheDocument();
    expect(within(top).getAllByRole("listitem").length).toBe(10);
    const body = lastPreview(fake);
    expect(body.viewerId).toBeNull();
    expect(body.layout).toBeUndefined();
    expect(body.device).toBe("tv");
    expect(body.at).toMatch(/^\d{4}-\d{2}-\d{2}T/);
    // The layer switcher starts on Global; Kids carry an override.
    expect(screen.getByRole("radio", { name: /Global Home/ })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(screen.getByRole("radio", { name: /The Kids/ })).toHaveTextContent("2 overrides");
  });

  it("edits Global: the server previews the draft and reports the impact across viewers", async () => {
    const { fake } = renderApp("/home-screens");
    await screen.findByRole("region", { name: /Top 10 on Living Room Ether Today/ }, WAIT);
    await setStatus("Top 10 Today", "Off");

    // Previewed server-side from the unsaved draft.
    const hidden = await screen.findByRole(
      "listitem",
      { name: "Not shown: Top 10 on Living Room Ether Today" },
      WAIT,
    );
    expect(within(hidden).getByText("Off in global layer")).toBeInTheDocument();
    expect(lastPreview(fake).layout?.global[0]?.rows).toContainEqual({
      row: "top-10-today",
      status: "off",
    });
    expect(screen.getByText("Previewing unsaved changes")).toBeInTheDocument();
    expect(
      within(screen.getByRole("listitem", { name: "Row Top 10 Today" })).getByText(
        "Affects 4 viewers",
      ),
    ).toBeInTheDocument();

    const impact = await screen.findByRole("status", { name: "Impact across viewers" });
    await waitFor(
      () =>
        expect(impact).toHaveTextContent(
          /Affects 4 viewers · 1 has overrides · \d+ Homes? changes? in this context/,
        ),
      WAIT,
    );
    expect(calls(fake, "POST", "/layout/impact").at(-1)!.body).toMatchObject({
      device: "tv",
      section: "home",
    });

    fireEvent.click(screen.getByRole("button", { name: /Save layout/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/layout")).toHaveLength(1), WAIT);
    const saved = savedLayout(fake);
    expect(saved.global[0]?.rows).toContainEqual({ row: "top-10-today", status: "off" });
    expect(saved.global[0]?.rows).toContainEqual({
      row: "continue-watching",
      status: "always",
      slot: 1,
    });
    expect(saved.viewers).toEqual([
      {
        userId: 3,
        pages: [
          {
            section: "home",
            maxRows: 8,
            rows: [{ row: "short-on-time", status: "always", maxSlot: 3 }],
          },
        ],
      },
    ]);
    await waitFor(() => expect(screen.queryByText("Unsaved changes")).toBeNull());
  });

  it("overrides a Global setting for one viewer and resets it to global", async () => {
    const { fake } = renderApp("/home-screens");
    await screen.findByRole("region", { name: "Slot 1: Continue Watching" }, WAIT);
    fireEvent.click(screen.getByRole("radio", { name: /Sam Rivera/ }));
    await screen.findByRole("region", { name: /Hidden Gems for You/ }, WAIT);
    await waitFor(() => expect(lastPreview(fake).viewerId).toBe(2));

    await setStatus("Hidden Gems for You", "Off");
    const hidden = await screen.findByRole(
      "listitem",
      { name: "Not shown: Hidden Gems for You" },
      WAIT,
    );
    expect(within(hidden).getByText("Off in override layer")).toBeInTheDocument();
    expect(within(hidden).getByText("Override for Sam Rivera")).toBeInTheDocument();
    expect(lastPreview(fake).layout?.viewers).toContainEqual({
      userId: 2,
      pages: [{ section: "home", rows: [{ row: "hidden-gems", status: "off" }] }],
    });
    expect(
      within(screen.getByRole("listitem", { name: "Row Hidden Gems for You" })).getByText(
        "Override",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Sam Rivera/ })).toHaveTextContent("1 override");
    fireEvent.click(screen.getByRole("tab", { name: "Overrides" }));
    expect(screen.getByRole("region", { name: "Overrides for Sam Rivera" })).toHaveTextContent(
      "hidden-gems",
    );

    // Reset to global: the row comes back, the override is gone.
    fireEvent.click(screen.getByRole("tab", { name: "Rows" }));
    fireEvent.click(screen.getByRole("button", { name: "Constraints for Hidden Gems for You" }));
    fireEvent.click(screen.getByRole("button", { name: "Reset to global: Hidden Gems for You" }));
    await closeEditor();
    await waitFor(
      () =>
        expect(
          screen.queryByRole("listitem", { name: "Not shown: Hidden Gems for You" }),
        ).toBeNull(),
      WAIT,
    );
    expect(screen.queryByText("Unsaved changes")).toBeNull();

    // An override that sticks: always show it for Sam, anchored to slot 2.
    await setStatus("Hidden Gems for You", "Always");
    fireEvent.click(screen.getByRole("button", { name: "Constraints for Hidden Gems for You" }));
    fireEvent.change(screen.getByLabelText("Anchor slot for Hidden Gems for You"), {
      target: { value: "2" },
    });
    await closeEditor();
    await waitFor(() => {
      const gems = screen.getByRole("region", { name: "Slot 2: Hidden Gems for You" });
      expect(within(gems).getByText(/override anchor at slot 2/)).toBeInTheDocument();
    }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: /Save layout/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/layout")).toHaveLength(1), WAIT);
    expect(savedLayout(fake).viewers).toContainEqual({
      userId: 2,
      pages: [{ section: "home", rows: [{ row: "hidden-gems", status: "always", slot: 2 }] }],
    });
  });

  it("anchors a row from the preview and limits the page length", async () => {
    const { fake } = renderApp("/home-screens");
    const row = await screen.findByRole("region", { name: /Slot \d+: New Releases/ }, WAIT);
    const slot = row.getAttribute("aria-label")!.match(/^Slot (\d+)/)![1];
    fireEvent.click(
      within(row).getByRole("button", { name: `Anchor New Releases to slot ${slot}` }),
    );
    await waitFor(
      () =>
        expect(
          screen.getByRole("region", { name: `Slot ${slot}: New Releases` }),
        ).toHaveTextContent("Global anchor"),
      WAIT,
    );
    expect(lastPreview(fake).layout?.global[0]?.rows).toContainEqual({
      row: "new-releases",
      status: "always",
      slot: Number(slot),
    });
    // Page structure and length are edited in place on the Layout tab, not in a drawer.
    expect(
      screen.queryByRole("button", { name: /Edit anchored slots|Edit page length/ }),
    ).toBeNull();
    expect(
      within(screen.getByRole("region", { name: "Anchored slots" })).getAllByRole("button", {
        name: /Move anchored row/,
      }),
    ).toHaveLength(2);
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.change(screen.getByLabelText("Home rows"), { target: { value: "3" } });
    const cut = await screen.findByRole("region", { name: "Not on this page" }, WAIT);
    await waitFor(
      () => expect(cut).toHaveTextContent("Page length, frequency cap or position constraints"),
      WAIT,
    );
    expect(lastPreview(fake).layout?.global[0]?.maxRows).toBe(3);
  });

  it("adds a custom row pattern from a drawer", async () => {
    renderApp("/home-screens");
    expect(await screen.findByRole("heading", { name: "Home screens" }, WAIT)).toBeInTheDocument();
    await screen.findByRole("region", { name: "Slot 1: Continue Watching" }, WAIT);
    fireEvent.click(screen.getByRole("tab", { name: "Rows" }));
    fireEvent.click(screen.getByRole("button", { name: "Add custom pattern" }));
    const drawer = await screen.findByRole("dialog", { name: "Custom row pattern" });
    fireEvent.change(within(drawer).getByLabelText("Custom row pattern"), {
      target: { value: "because.*" },
    });
    fireEvent.click(within(drawer).getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Custom row pattern" })).toBeNull(),
    );
    expect(await screen.findByRole("listitem", { name: "Row because.*" })).toBeInTheDocument();
    expect(await screen.findByText("Unsaved changes")).toBeInTheDocument();
  });

  it("discards unsaved edits", async () => {
    const { fake } = renderApp("/home-screens");
    await screen.findByRole("region", { name: /Top 10 on Living Room Ether Today/ }, WAIT);
    await setStatus("New Releases", "Off");
    expect(await screen.findByText("Unsaved changes")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Discard/ }));
    await waitFor(() => expect(screen.queryByText("Unsaved changes")).toBeNull());
    expect(calls(fake, "PUT", "/layout")).toHaveLength(0);
  });

  it("shows a viewer's saved Home read-only on their profile", async () => {
    const { fake } = renderApp("/viewers/2/home");
    const cw = await screen.findByRole("region", { name: "Slot 1: Continue Watching" }, WAIT);
    expect(within(cw).getByText("Global anchor")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Turn off/ })).toBeNull();
    expect(calls(fake, "GET", "/viewers/2/home")).not.toHaveLength(0);
  });
});
