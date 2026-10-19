import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { TasteOverride } from "../lib/contract";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

describe("Viewers", () => {
  it("lists viewers with activity and opens a profile", async () => {
    renderApp("/viewers");
    const row = await screen.findByText("Sam Rivera", {}, WAIT);
    expect(screen.getByText("The Kids")).toBeInTheDocument();
    fireEvent.click(row);
    expect(await screen.findByRole("heading", { name: /Sam Rivera/ }, WAIT)).toBeInTheDocument();
    expect(await screen.findByText("What Ether has learned", {}, WAIT)).toBeInTheDocument();
  });

  it("corrects taste from the genre chart and saves overrides", async () => {
    const { fake } = renderApp("/viewers/2");
    const genres = await screen.findByRole("list", { name: /Genre affinity/ }, WAIT);
    fireEvent.click(within(genres).getByRole("button", { name: /Horror/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Not into it" }));
    expect(screen.getByText("Your corrections").closest("section")).toHaveTextContent("Horror");
    fireEvent.click(screen.getByRole("button", { name: /Save corrections/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/viewers/2/taste/overrides")).toHaveLength(1), WAIT);
    const body = calls(fake, "PUT", "/viewers/2/taste/overrides")[0]!.body as { overrides: TasteOverride[] };
    expect(body.overrides).toEqual([{ kind: "genre", value: "Horror", weight: -1 }]);
    // The returned profile now carries the override; nothing is left unsaved.
    await waitFor(() => expect(screen.getByRole("button", { name: /Save corrections/ })).toBeDisabled());
  });

  it("adds a person correction through search", async () => {
    const { fake } = renderApp("/viewers/1");
    await screen.findByText("Your corrections", {}, WAIT);
    fireEvent.click(screen.getByRole("radio", { name: "Person" }));
    fireEvent.click(screen.getByRole("radio", { name: "Loves it" }));
    const search = screen.getByRole("combobox", { name: "Person to correct" });
    fireEvent.focus(search);
    fireEvent.change(search, { target: { value: "Ada" } });
    fireEvent.click(await screen.findByRole("button", { name: /Ada Moreau/ }, WAIT));
    fireEvent.click(screen.getByRole("button", { name: /Save corrections/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/viewers/1/taste/overrides")).toHaveLength(1), WAIT);
    expect((calls(fake, "PUT", "/viewers/1/taste/overrides")[0]!.body as { overrides: TasteOverride[] }).overrides)
      .toEqual([{ kind: "person", value: "108", weight: 1 }]);
  });

  it("sets a viewer's stream limit and rating cap", async () => {
    const { fake } = renderApp("/viewers/4?tab=limits");
    const limit = await screen.findByLabelText("Stream limit", {}, WAIT);
    fireEvent.change(limit, { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
    await waitFor(() => expect(calls(fake, "PATCH", "/users/4")).toHaveLength(1), WAIT);
    expect(calls(fake, "PATCH", "/users/4")[0]!.body).toMatchObject({ streamLimit: 1 });
  });
});
