import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import Metadata from "../pages/Metadata";
describe("Metadata episode orders", () => {
  it("selects a show, persists the owner order, previews canonical identities and resets viewer overrides", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        calls.push({ url, init });
        let value: unknown = {};
        if (url.endsWith("/users")) value = { users: [{ id: 42, title: "Viewer" }] };
        else if (url.includes("/catalog/search"))
          value = { items: [{ key: "2000000000000010", title: "Fixture Show", type: "show" }] };
        else if (url.endsWith("/groups"))
          value = [{ id: "dvd", name: "DVD order", type: 3, episode_count: 2 }];
        else if (url.endsWith("/preview"))
          value = [
            {
              key: "4000000100010002",
              title: "Second canonical episode",
              canonicalSeason: 1,
              canonicalEpisode: 2,
              group: "Disc 1",
              index: 1,
            },
          ];
        else
          value =
            init?.method === "PUT"
              ? JSON.parse(String(init.body))
              : { groupId: "", specials: "last", inherited: url.endsWith("/42") };
        return new Response(JSON.stringify(value), { status: 200 });
      }),
    );
    render(<Metadata />);
    fireEvent.change(screen.getByLabelText("Find a show"), { target: { value: "Fixture" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: "Fixture Show" }));
    fireEvent.change(await screen.findByLabelText("Episode order"), { target: { value: "dvd" } });
    fireEvent.click(screen.getByRole("button", { name: "Save episode order" }));
    expect(await screen.findByText("Second canonical episode")).toBeInTheDocument();
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.url.endsWith("/users/0") &&
            call.init?.method === "PUT" &&
            JSON.parse(String(call.init.body)).groupId === "dvd",
        ),
      ).toBe(true),
    );
    fireEvent.change(screen.getByLabelText("Apply to"), { target: { value: "42" } });
    expect(await screen.findByText(/currently follows the owner default/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Follow owner default" }));
    await waitFor(() =>
      expect(
        calls.some((call) => call.url.endsWith("/users/42") && call.init?.method === "DELETE"),
      ).toBe(true),
    );
  });
});
