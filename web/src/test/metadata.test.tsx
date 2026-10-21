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

describe("Metadata artwork studio", () => {
  it("previews a draft without saving, compares originals, discards and persists the complete design", async () => {
    const { fake } = renderAppWith("/metadata", () => undefined);
    const style = await screen.findByLabelText("Badge finish", {}, WAIT);
    fireEvent.click(screen.getByRole("button", { name: /Spotlight/ }));
    expect(style).toHaveValue("solid");
    expect(screen.getByLabelText("Placement")).toHaveValue("top");
    expect(calls(fake, "PUT", "/discovery/overlays/settings")).toHaveLength(0);
    await waitFor(() => {
      const img = screen.getByRole("img", { name: "After the Tide Original sample artwork" });
      const settings = JSON.parse(
        new URL(img.getAttribute("src")!, "http://localhost").searchParams.get("settings")!,
      );
      expect(settings).toMatchObject({ style: "solid", position: "top", accent: "#a7c4b5" });
    });
    fireEvent.click(screen.getByRole("button", { name: "Compare" }));
    const original = await screen.findByRole("img", {
      name: "After the Tide Original",
    });
    expect(
      JSON.parse(
        new URL(original.getAttribute("src")!, "http://localhost").searchParams.get("settings")!,
      ),
    ).toMatchObject({ enabled: false });
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(style).toHaveValue("glass"));
    fireEvent.change(screen.getByLabelText("Badge size"), { target: { value: "large" } });
    fireEvent.click(screen.getByRole("button", { name: "Lavender" }));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(calls(fake, "PUT", "/discovery/overlays/settings").at(-1)?.body).toMatchObject({
        size: "large",
        accent: "#c0a6d8",
        style: "glass",
        position: "bottom",
      }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Unsaved changes" })).not.toBeInTheDocument(),
    );
  });

  it("keeps an unsuccessful save editable and supports retry", async () => {
    let failed = true;
    const { fake } = renderAppWith("/metadata/artwork", (method, path) => {
      if (method === "PUT" && path === "/discovery/overlays/settings" && failed)
        return new Response("Storage unavailable", { status: 500 });
      return undefined;
    });
    fireEvent.change(await screen.findByLabelText("Placement", {}, WAIT), {
      target: { value: "top" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await screen.findByRole("alert");
    expect(screen.getByLabelText("Placement")).toHaveValue("top");
    failed = false;
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls(fake, "PUT", "/discovery/overlays/settings")).toHaveLength(2));
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Unsaved changes" })).not.toBeInTheDocument(),
    );
  });

  it("preserves the old Home URL and points navigation to /home", async () => {
    renderAppWith("/home-screens", () => undefined);
    expect(await screen.findByRole("heading", { name: "Home" }, WAIT)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Home" })).toHaveAttribute("href", "/home");
  });
});
