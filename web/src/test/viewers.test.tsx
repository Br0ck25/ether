import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { TasteOverride } from "../lib/contract";
import type { FakeServer } from "../mock/server";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

type Route = (method: string, path: string, body: unknown) => unknown;

/**
 * Serve endpoints the shared fake doesn't model (safety policies, invites).
 * The fake still records every call; only its 404s are replaced.
 */
function extraRoutes(route: Route) {
  return (fake: FakeServer) => {
    const base = fake.fetch;
    fake.fetch = async (input, init) => {
      const res = await base(input, init);
      if (res.status !== 404) return res;
      const url = new URL(String(input), "http://localhost");
      const path = url.pathname.replace(/^\/ether\/api/, "") + url.search;
      const raw = typeof init?.body === "string" && init.body ? init.body : "";
      const out = route((init?.method ?? "GET").toUpperCase(), path, raw ? JSON.parse(raw) : null);
      if (out === undefined) return res;
      return new Response(JSON.stringify(out), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    };
  };
}

const POLICY = {
  maxContentRating: "",
  blockedGenres: [],
  blockedKeywords: [],
  remoteDisabled: false,
  maxBitrateKbps: 0,
  streamLimit: 0,
  allowedHours: [],
  timezone: "UTC",
};

describe("Viewers", () => {
  it("lists viewers with activity and opens a profile", async () => {
    renderApp("/viewers");
    const row = await screen.findByText("Sam Rivera", {}, WAIT);
    expect(screen.getByText("The Kids")).toBeInTheDocument();
    // The kids' rating cap shows in the limits summary.
    expect(await screen.findByText("Up to PG", {}, WAIT)).toBeInTheDocument();
    fireEvent.click(row);
    expect(await screen.findByRole("heading", { name: /Sam Rivera/ }, WAIT)).toBeInTheDocument();
    expect(await screen.findByText("What Ether has learned", {}, WAIT)).toBeInTheDocument();
  });

  it("corrects taste from the genre chart and saves overrides", async () => {
    const { fake } = renderApp("/viewers/2");
    fireEvent.click(await screen.findByRole("tab", { name: "Genres" }));
    const genres = await screen.findByRole("list", { name: /Genre affinity/ }, WAIT);
    fireEvent.click(within(genres).getByRole("button", { name: /Horror/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Not into it" }));
    expect(screen.getByRole("dialog", { name: "Horror" })).toHaveTextContent("Not into it");
    fireEvent.click(screen.getByRole("button", { name: /Save correction/ }));
    await waitFor(
      () => expect(calls(fake, "PUT", "/viewers/2/taste/overrides")).toHaveLength(1),
      WAIT,
    );
    const body = calls(fake, "PUT", "/viewers/2/taste/overrides")[0]!.body as {
      overrides: TasteOverride[];
    };
    expect(body.overrides).toEqual([{ kind: "genre", value: "Horror", weight: -1 }]);
    // The returned profile now carries the override; nothing is left unsaved.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Save correction/ })).toBeDisabled(),
    );
  });

  it("adds a person correction through search", async () => {
    const { fake } = renderApp("/viewers/1");
    fireEvent.click(await screen.findByRole("tab", { name: "Corrections" }));
    await screen.findByText("Your corrections", {}, WAIT);
    fireEvent.click(screen.getByRole("radio", { name: "Person" }));
    fireEvent.click(screen.getByRole("radio", { name: "Loves it" }));
    const search = screen.getByRole("combobox", { name: "Person to correct" });
    fireEvent.focus(search);
    fireEvent.change(search, { target: { value: "Ada" } });
    fireEvent.click(await screen.findByRole("button", { name: /Ada Moreau/ }, WAIT));
    fireEvent.click(await screen.findByRole("button", { name: /Save changes/ }));
    await waitFor(
      () => expect(calls(fake, "PUT", "/viewers/1/taste/overrides")).toHaveLength(1),
      WAIT,
    );
    expect(
      (calls(fake, "PUT", "/viewers/1/taste/overrides")[0]!.body as { overrides: TasteOverride[] })
        .overrides,
    ).toEqual([{ kind: "person", value: "108", weight: 1 }]);
  });

  it("sets a viewer's stream limit and rating cap", async () => {
    const { fake } = renderApp("/viewers/4/access");
    const limit = await screen.findByLabelText("Stream limit", {}, WAIT);
    fireEvent.change(limit, { target: { value: "1" } });
    fireEvent.click(await screen.findByRole("button", { name: /Save changes/ }));
    await waitFor(() => expect(calls(fake, "PATCH", "/users/4")).toHaveLength(1), WAIT);
    expect(calls(fake, "PATCH", "/users/4")[0]!.body).toMatchObject({ streamLimit: 1 });
    // Only the part that changed is written.
    expect(calls(fake, "PUT", "/safety/policies/4")).toHaveLength(0);
  });

  it("opens the old limits link on the Access tab", async () => {
    renderApp("/viewers/4?tab=limits");
    expect(await screen.findByLabelText("Stream limit", {}, WAIT)).toBeInTheDocument();
  });

  it("saves a viewer's parental controls from their Access tab", async () => {
    const { fake } = renderApp(
      "/viewers/3/access",
      {},
      extraRoutes((method, path) => {
        if (method === "GET" && path === "/safety/policies/3") return POLICY;
        if (method === "GET" && path === "/safety/policies/presets/kids?age=family")
          return { maxContentRating: "PG", blockedKeywords: ["pornography"] };
        if (method === "PUT" && path === "/safety/policies/3") return { ok: true };
        return undefined;
      }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Family" }, WAIT));
    await waitFor(() =>
      expect(screen.getByLabelText("Blocked keywords")).toHaveValue("pornography"),
    );
    fireEvent.change(screen.getByLabelText("Blocked genres"), {
      target: { value: "Horror, Thriller" },
    });
    fireEvent.click(screen.getByLabelText("Local network only"));
    fireEvent.change(screen.getByLabelText("Concurrent streams"), { target: { value: "2" } });
    fireEvent.click(
      within(screen.getByRole("group", { name: "Allowed hours" })).getByRole("button", {
        name: "20:00",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /Save changes/ }));
    await waitFor(() => expect(calls(fake, "PUT", "/safety/policies/3")).toHaveLength(1), WAIT);
    expect(calls(fake, "PUT", "/safety/policies/3")[0]!.body).toMatchObject({
      maxContentRating: "PG",
      blockedGenres: ["Horror", "Thriller"],
      blockedKeywords: ["pornography"],
      remoteDisabled: true,
      streamLimit: 2,
      allowedHours: [20],
    });
    // The user record (stream limit, rating cap) was untouched.
    expect(calls(fake, "PATCH", "/users/3")).toHaveLength(0);
    await waitFor(() => expect(screen.queryByText("You have unsaved changes")).toBeNull());
  });

  it("creates an invitation with a parental-controls preset", async () => {
    const invites = [
      {
        id: 9,
        label: "Cousins",
        expires: "2099-01-01T00:00:00Z",
        maxUses: 3,
        uses: 1,
        revoked: false,
      },
    ];
    const { fake } = renderApp(
      "/viewers/invitations",
      {},
      extraRoutes((method, path, body) => {
        if (method === "GET" && path === "/safety/invites/") return invites;
        if (method === "GET" && path === "/safety/policies/presets/kids?age=teens")
          return { maxContentRating: "PG-13" };
        if (method === "POST" && path === "/safety/invites/") {
          const b = body as { label: string; maxUses: number };
          invites.push({
            id: 10,
            label: b.label,
            expires: "2099-01-01T00:00:00Z",
            maxUses: b.maxUses,
            uses: 0,
            revoked: false,
          });
          return { path: "/invite/abc123" };
        }
        return undefined;
      }),
    );
    expect(await screen.findByText("Cousins", {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText("1 of 3")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Invite a friend/ }));
    const drawer = await screen.findByRole("dialog", { name: "Invite a friend" }, WAIT);
    fireEvent.change(within(drawer).getByLabelText("Invitation label"), {
      target: { value: "Neighbours" },
    });
    fireEvent.change(within(drawer).getByLabelText("Maximum people"), { target: { value: "2" } });
    fireEvent.click(within(drawer).getByRole("radio", { name: "Teens" }));
    const create = within(drawer).getByRole("button", { name: "Create invitation" });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create);
    await waitFor(() => expect(calls(fake, "POST", "/safety/invites/")).toHaveLength(1), WAIT);
    expect(calls(fake, "POST", "/safety/invites/")[0]!.body).toMatchObject({
      label: "Neighbours",
      maxUses: 2,
      sections: [1, 2],
      policy: { maxContentRating: "PG-13" },
      streamLimit: 0,
    });
    expect(await within(drawer).findByLabelText("Invitation link", {}, WAIT)).toHaveValue(
      `${location.origin}/invite/abc123`,
    );
    expect(await screen.findByText("Neighbours", {}, WAIT)).toBeInTheDocument();
  });
  it("starts an invitation from a viewer's saved parental controls", async () => {
    const { fake } = renderApp(
      "/viewers/3/access",
      {},
      extraRoutes((method, path) => {
        if (method === "GET" && path === "/safety/policies/3")
          return { ...POLICY, maxContentRating: "PG", blockedGenres: ["Horror"] };
        if (method === "GET" && path === "/safety/invites/") return [];
        if (method === "POST" && path === "/safety/invites/") return { path: "/invite/kids" };
        return undefined;
      }),
    );
    fireEvent.click(await screen.findByRole("button", { name: /Invite a friend/ }, WAIT));
    const drawer = await screen.findByRole("dialog", { name: "Invite a friend" }, WAIT);
    expect(
      await within(drawer).findByRole("radio", { name: "Same as The Kids" }, WAIT),
    ).toHaveAttribute("aria-checked", "true");
    const create = within(drawer).getByRole("button", { name: "Create invitation" });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create);
    await waitFor(() => expect(calls(fake, "POST", "/safety/invites/")).toHaveLength(1), WAIT);
    expect(calls(fake, "POST", "/safety/invites/")[0]!.body).toMatchObject({
      policy: { maxContentRating: "PG", blockedGenres: ["Horror"] },
    });
  });
});
