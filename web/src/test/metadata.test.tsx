import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { calls } from "./harness";
import { renderAppWith } from "./routes";

const WAIT = { timeout: 4000 };
const SHOW = "2000000000000010";

describe("Metadata episode orders", () => {
  it("selects a show, persists the owner order, previews canonical identities and resets viewer overrides", async () => {
    // Sam Rivera (viewer 2) follows the default until an override is saved.
    const overrides = new Map<number, { groupId: string; specials: string }>();
    const { fake } = renderAppWith("/metadata/episodes", (method, path, body) => {
      if (path === "/catalog/search")
        return { items: [{ key: SHOW, title: "Fixture Show", type: "show" }] };
      if (!path.startsWith(`/metadata/shows/${SHOW}`)) return undefined;
      if (path.endsWith("/groups"))
        return [{ id: "dvd", name: "DVD order", type: 3, episode_count: 2 }];
      if (path.endsWith("/preview"))
        return [
          {
            key: "4000000100010002",
            title: "Second canonical episode",
            canonicalSeason: 1,
            canonicalEpisode: 2,
            group: "Disc 1",
            index: 1,
          },
        ];
      const id = Number(path.split("/").at(-1));
      if (method === "PUT") {
        overrides.set(id, body as { groupId: string; specials: string });
        return body;
      }
      if (method === "DELETE") overrides.delete(id);
      return overrides.get(id) ?? { groupId: "", specials: "last", inherited: id !== 0 };
    });

    fireEvent.change(await screen.findByLabelText("Find a show", {}, WAIT), {
      target: { value: "Fixture" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: "Fixture Show" }, WAIT));

    const drawer = await screen.findByRole("dialog", { name: "Fixture Show" });
    const defaults = within(drawer).getByRole("region", { name: "Default order" });
    fireEvent.change(await within(defaults).findByLabelText("Episode order"), {
      target: { value: "dvd" },
    });
    fireEvent.click(within(defaults).getByRole("button", { name: "Save episode order" }));
    expect(await screen.findByText("Second canonical episode")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        calls(fake, "PUT", `/metadata/shows/${SHOW}/users/0`).some(
          (c) => (c.body as { groupId: string }).groupId === "dvd",
        ),
      ).toBe(true),
    );

    // Viewers are listed with their override state, not picked from a dropdown.
    const overridesList = within(drawer).getByRole("region", { name: "Viewer overrides" });
    const sam = (await within(overridesList).findByText("Sam Rivera")).closest("li")!;
    expect(await within(sam).findByText("Uses default")).toBeInTheDocument();
    fireEvent.click(within(sam).getByRole("button", { name: "Edit order for Sam Rivera" }));
    const viewer = await screen.findByRole("dialog", { name: "Fixture Show for Sam Rivera" });
    expect(await within(viewer).findByText(/uses the default order/)).toBeInTheDocument();
    expect(within(viewer).queryByRole("button", { name: "Use default" })).toBeNull();

    fireEvent.change(within(viewer).getByLabelText("Episode order"), { target: { value: "dvd" } });
    fireEvent.click(within(viewer).getByRole("button", { name: "Save episode order" }));
    await waitFor(() =>
      expect(calls(fake, "PUT", `/metadata/shows/${SHOW}/users/2`)).toHaveLength(1),
    );
    await waitFor(() =>
      expect(within(sam).getByText("Custom for this viewer")).toBeInTheDocument(),
    );

    fireEvent.click(await within(viewer).findByRole("button", { name: "Use default" }));
    await waitFor(() =>
      expect(calls(fake, "DELETE", `/metadata/shows/${SHOW}/users/2`)).toHaveLength(1),
    );
    await waitFor(() => expect(within(sam).getByText("Uses default")).toBeInTheDocument());
  });
});
