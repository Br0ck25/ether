import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CollectionSpec, Rule } from "../lib/contract";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

function previewBodies(fake: Parameters<typeof calls>[0]): CollectionSpec[] {
  return calls(fake, "POST", "/collections/preview").map((c) => c.body as CollectionSpec);
}
function hasRule(spec: CollectionSpec, pred: (r: Rule) => boolean) {
  return spec.rules.some(pred);
}

describe("Collection Studio editor", () => {
  it("creates a collection in a drawer over the gallery", async () => {
    renderApp("/collections/new?draft=test-drawer");
    const drawer = await screen.findByRole("dialog", { name: "New collection" }, WAIT);
    expect(await within(drawer).findByLabelText("Collection name", {}, WAIT)).toBeInTheDocument();
    fireEvent.click(within(drawer).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), WAIT);
    expect(await screen.findByRole("heading", { name: "Collections" }, WAIT)).toBeInTheDocument();
  });

  it("edits rules with live, debounced previews, undo, and saves", async () => {
    const { fake } = renderApp("/collections/new?draft=test-1");
    const name = await screen.findByLabelText("Collection name", {}, WAIT);
    fireEvent.change(name, { target: { value: "Late-night thrillers" } });

    // The initial preview arrives without any rules.
    await waitFor(() => expect(previewBodies(fake).length).toBeGreaterThan(0), WAIT);
    expect(
      await within(await screen.findByRole("region", { name: "Live preview" }, WAIT)).findByText(
        /\d+ titles/,
        {},
        WAIT,
      ),
    ).toBeInTheDocument();

    // Add a rating rule from the field menu.
    fireEvent.click(screen.getByRole("button", { name: /Add rule/ }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Rating" }));
    const value = await screen.findByLabelText("Rule 1 value");
    await waitFor(
      () =>
        expect(
          previewBodies(fake).some((s) =>
            hasRule(s, (r) => r.field === "rating" && r.op === "gte" && r.value === 7),
          ),
        ).toBe(true),
      WAIT,
    );

    // Typing a new value re-runs the preview once the edits settle.
    const before = previewBodies(fake).length;
    fireEvent.change(value, { target: { value: "8.5" } });
    await waitFor(
      () =>
        expect(
          previewBodies(fake).some((s) =>
            hasRule(s, (r) => r.field === "rating" && r.value === 8.5),
          ),
        ).toBe(true),
      WAIT,
    );
    expect(previewBodies(fake).length).toBe(before + 1);
    expect(screen.getByText("Rating is at least 8.5")).toBeInTheDocument();

    // Undo restores the previous value.
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(screen.getByLabelText("Rule 1 value")).toHaveValue(7));

    // Save creates the collection with the edited spec, unpublished.
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
    await waitFor(
      () =>
        expect(
          calls(fake, "POST", "/collections").filter((c) => c.path === "/collections"),
        ).toHaveLength(1),
      WAIT,
    );
    const created = calls(fake, "POST", "/collections").find((c) => c.path === "/collections")!
      .body as CollectionSpec;
    expect(created.name).toBe("Late-night thrillers");
    expect(created.enabled).toBe(false);
    expect(created.rules).toEqual([{ field: "rating", op: "gte", value: 7 }]);
    // The editor now shows the saved collection.
    await waitFor(
      () => expect(calls(fake, "GET", "/collections/c").length).toBeGreaterThan(0),
      WAIT,
    );
    expect(
      await within(
        await screen.findByRole("region", { name: "Collection Studio" }, WAIT),
      ).findByText("Draft", {}, WAIT),
    ).toBeInTheDocument();
  });

  it("refuses to save an incomplete collection and says why", async () => {
    const { fake } = renderApp("/collections/new?draft=test-2");
    await screen.findByLabelText("Collection name", {}, WAIT);
    fireEvent.click(screen.getByRole("button", { name: /^Publish$/ }));
    expect((await screen.findAllByText("Give the collection a name.")).length).toBeGreaterThan(0);
    expect(
      calls(fake, "POST", "/collections").filter((c) => c.path === "/collections"),
    ).toHaveLength(0);
  });

  it("keeps an unsaved draft on the device and restores it", async () => {
    const first = renderApp("/collections/new?draft=test-3");
    fireEvent.change(await screen.findByLabelText("Collection name", {}, WAIT), {
      target: { value: "Saturday matinee" },
    });
    await waitFor(
      () =>
        expect(localStorage.getItem("ether.draft.collection.test-3")).toContain("Saturday matinee"),
      WAIT,
    );
    first.unmount();
    renderApp("/collections/new?draft=test-3");
    expect(await screen.findByDisplayValue("Saturday matinee", {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText(/Restored unsaved changes/)).toBeInTheDocument();
  });
});

describe("Collection Studio gallery", () => {
  it("previews a selected collection in place and publishes it", async () => {
    const { fake } = renderApp("/collections?collection=c3");
    expect(
      await screen.findByRole("heading", { name: "Family movie night" }, WAIT),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    const row = screen.getByRole("button", { name: "Preview Family movie night" });
    expect(row).toHaveAttribute("aria-current", "true");
    await waitFor(
      () => expect(previewBodies(fake).some((s) => s.name === "Family movie night")).toBe(true),
      WAIT,
    );
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    await waitFor(() => expect(calls(fake, "PUT", "/collections/c3")).toHaveLength(1), WAIT);
    expect(await screen.findByRole("button", { name: "Unpublish" }, WAIT)).toBeInTheDocument();
    expect(within(row).getByText("Published")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview New to Ether" }));
    expect(await screen.findByRole("heading", { name: "New to Ether" }, WAIT)).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("filters the library and recovers from an empty search", async () => {
    renderApp("/collections");
    await screen.findByRole("heading", { name: "Edge of your seat" }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: "Collection status" }));
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "Drafts" }));
    expect(
      await screen.findByRole("heading", { name: "Family movie night" }, WAIT),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview Edge of your seat" })).toBeNull();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search collections" }), {
      target: { value: "no such collection" },
    });
    expect(await screen.findByRole("heading", { name: "No matches" }, WAIT)).toBeInTheDocument();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search collections" }), {
      target: { value: "" },
    });
    expect(
      await screen.findByRole("heading", { name: "Family movie night" }, WAIT),
    ).toBeInTheDocument();
  });

  it("keeps built-in collections read-only and duplicates them as editable drafts", async () => {
    renderApp("/collections?tab=builtin");
    await screen.findByRole("heading", { name: "Recently added" }, WAIT);
    expect(screen.queryByRole("button", { name: "Edit collection" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Unpublish" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Duplicate to edit" }));
    const drawer = await screen.findByRole("dialog", { name: "New collection" }, WAIT);
    expect(await within(drawer).findByLabelText("Collection name", {}, WAIT)).toHaveValue(
      "Recently added (copy)",
    );
    expect(within(drawer).getByLabelText("Collection name")).toBeEnabled();
  });

  it("lists collections and starts a new one from a template", async () => {
    renderApp("/collections");
    expect(
      await screen.findByRole("heading", { name: "Edge of your seat" }, WAIT),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: /Templates/ }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Preview template Hidden gems" }, WAIT),
    );
    const card = await screen.findByRole("button", { name: "Use template Hidden gems" }, WAIT);
    fireEvent.click(card);
    expect(await screen.findByDisplayValue("Hidden gems", {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText("Rating is at least 7.5")).toBeInTheDocument();
  });

  it("drafts a collection from a plain-English description", async () => {
    const { fake } = renderApp("/collections");
    await screen.findByRole("heading", { name: "Edge of your seat" }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: /Describe it/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Describe the collection"), {
      target: { value: "Acclaimed horror from the 80s" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Draft it/ }));
    expect(await within(dialog).findByText(/Genre is any of Horror/, {}, WAIT)).toBeInTheDocument();
    expect(calls(fake, "POST", "/collections/generate")[0]?.body).toEqual({
      prompt: "Acclaimed horror from the 80s",
      mediaType: "movie",
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Open as draft" }));
    expect(await screen.findByLabelText("Collection name", {}, WAIT)).toHaveValue(
      "Acclaimed horror from the 80s",
    );
  });

  it("explains how to enable Describe it when no language model is set up", async () => {
    renderApp("/collections", { llm: false });
    await screen.findByRole("heading", { name: "Edge of your seat" }, WAIT);
    fireEvent.click(screen.getByRole("button", { name: /Describe it/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Describe the collection"), {
      target: { value: "Cosy mysteries" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Draft it/ }));
    expect(
      await within(dialog).findByText("Describe it needs a language model", {}, WAIT),
    ).toBeInTheDocument();
  });
});
