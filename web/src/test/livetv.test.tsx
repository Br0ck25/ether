import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import LiveTV from "../pages/livetv/LiveTV";
import { calls as fakeCalls } from "./harness";
import { renderAppWith } from "./routes";

const WAIT = { timeout: 4000 };

function renderLiveTV(path = "/livetv") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/livetv/:tab?" element={<LiveTV />} />
      </Routes>
    </MemoryRouter>,
  );
}

function backend(channels: unknown[] = []) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({ url, init });
      let data: unknown = [];
      if (url.endsWith("/livetv/channels")) data = channels;
      else if (url.endsWith("/users"))
        data = { users: [{ id: 12, title: "Sam", username: "sam" }] };
      else if (url.endsWith("/collections"))
        data = {
          collections: [
            { id: "42", ratingKey: "6000000000000042", name: "Family favourites", itemCount: 8 },
          ],
        };
      else if (url.includes("/catalog/search"))
        data = {
          items: [
            {
              key: "1000000000000007",
              type: "movie",
              title: "Adventure",
              runtimeMinutes: 108,
              contentRating: "PG",
            },
          ],
        };
      else if (url.endsWith("/livetv/policies/12"))
        data = { userId: 12, disabled: false, channels: [], favorites: [], maxRating: "" };
      return new Response(JSON.stringify(data), { status: 200 });
    }),
  );
  return calls;
}
const channelSave = (calls: ReturnType<typeof backend>) =>
  calls.find((c) => c.url.endsWith("/livetv/channels") && c.init?.method === "POST");

describe("Live TV owner programming", () => {
  it("finds channels beyond the first page and narrows to favourites", async () => {
    backend(
      Array.from({ length: 25 }, (_, i) => ({
        id: String(i),
        number: String(i + 1),
        name: `Station ${i + 1}`,
        kind: "iptv",
        enabled: true,
        favorite: i === 24,
        group: i === 24 ? "Weather" : "General",
      })),
    );
    renderLiveTV();
    const search = await screen.findByRole("searchbox", { name: "Search channels" });
    expect(screen.queryByText("★ Station 25")).not.toBeInTheDocument();
    fireEvent.change(search, { target: { value: "Weather" } });
    expect(screen.getByText("★ Station 25")).toBeInTheDocument();
    expect(screen.queryByText("Station 1")).not.toBeInTheDocument();
    fireEvent.change(search, { target: { value: "" } });
    fireEvent.click(screen.getByRole("switch", { name: "Favourites only" }));
    expect(screen.getByText("★ Station 25")).toBeInTheDocument();
    expect(screen.queryByText("Station 1")).not.toBeInTheDocument();
  });
  it("builds a channel from a catalogue search and named viewer without raw keys", async () => {
    const calls = backend();
    renderLiveTV();
    await screen.findByRole("button", { name: "Create channel" });
    fireEvent.click(screen.getByRole("button", { name: "Create channel" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Movie night" } });
    fireEvent.click(screen.getByRole("tab", { name: "Programming" }));
    fireEvent.change(screen.getByLabelText("Search titles"), { target: { value: "Adventure" } });
    fireEvent.click(await screen.findByRole("button", { name: /Adventure/ }));
    expect(await screen.findByRole("button", { name: "Remove Adventure" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Viewers" }));
    fireEvent.click(await screen.findByLabelText("Sam"));
    fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
    await waitFor(() => expect(channelSave(calls)).toBeDefined());
    const saved = JSON.parse(String(channelSave(calls)?.init?.body));
    expect(saved).toMatchObject({
      name: "Movie night",
      allowedUsers: [12],
      items: [{ key: 1000000000000007, title: "Adventure", durationMs: 6480000, rating: "PG" }],
    });
    expect(screen.queryByText(/catalogue key \| title/)).not.toBeInTheDocument();
  });
  it("selects a collection by name and submits its published identity", async () => {
    const calls = backend();
    renderLiveTV();
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/collections"))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Create channel" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Family TV" } });
    fireEvent.click(screen.getByRole("tab", { name: "Programming" }));
    fireEvent.change(screen.getByLabelText("Programming source"), {
      target: { value: "collection" },
    });
    fireEvent.change(await screen.findByLabelText(/^Collection/), {
      target: { value: "6000000000000042" },
    });
    expect(
      screen.getByRole("option", { name: "Family favourites · 8 titles" }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
    await waitFor(() => expect(channelSave(calls)).toBeDefined());
    expect(JSON.parse(String(channelSave(calls)?.init?.body)).collection).toBe(6000000000000042);
  });
  it("explains an empty time block and lets its programming be selected visually", async () => {
    const calls = backend();
    renderLiveTV();
    fireEvent.click(screen.getByRole("button", { name: "80s action template" }));
    fireEvent.click(screen.getByRole("tab", { name: "Schedule" }));
    fireEvent.click(screen.getByRole("button", { name: "Add time block" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview six hours" }));
    expect(await screen.findByRole("status")).toHaveTextContent(
      "Add at least one movie to each time block",
    );
    expect(calls.some((c) => c.url.endsWith("/livetv/preview"))).toBe(false);
    const block = screen.getByLabelText("Time block 1 programming");
    fireEvent.change(within(block).getByLabelText("Search titles"), {
      target: { value: "Adventure" },
    });
    fireEvent.click(await within(block).findByRole("button", { name: /Adventure/ }));
    await within(block).findByRole("button", { name: "Remove Adventure" });
    fireEvent.click(screen.getByRole("button", { name: "Preview six hours" }));
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/livetv/preview"))).toBe(true));
    const preview = calls.find((c) => c.url.endsWith("/livetv/preview"));
    expect(JSON.parse(String(preview?.init?.body))).toMatchObject({
      filter: { yearFrom: 1980, yearTo: 1989, genres: ["Action"] },
      blocks: [{ start: 0, end: 60, items: [{ key: 1000000000000007 }] }],
    });
  });
  it("puts each tab on its own URL and leaves viewer access to the profile", async () => {
    backend();
    renderLiveTV("/livetv/guide");
    expect(await screen.findByRole("button", { name: "Refresh guide" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Viewer access" })).toBeNull();
    expect(screen.queryByLabelText("Viewer")).toBeNull();
    fireEvent.click(screen.getByRole("tab", { name: "IPTV sources" }));
    expect(await screen.findByRole("button", { name: "Import IPTV source" })).toBeInTheDocument();
  });
});

describe("Live TV access on the viewer profile", () => {
  it("edits one viewer's Live TV policy in place on their Access tab", async () => {
    let policy = {
      userId: 2,
      disabled: false,
      channels: [] as string[],
      favorites: [],
      maxRating: "",
    };
    const { fake } = renderAppWith("/viewers/2/access", (method, path, body) => {
      if (path === "/livetv/channels")
        return [
          { id: "c1", number: "1", name: "Movie night", kind: "virtual", enabled: true },
          { id: "c2", number: "2", name: "Cartoons", kind: "virtual", enabled: true },
        ];
      if (path === "/livetv/policies/2") {
        if (method === "PUT") policy = body as typeof policy;
        return policy;
      }
      return undefined;
    });
    fireEvent.click(await screen.findByLabelText("Disable Live TV for this viewer", {}, WAIT));
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(
        fakeCalls(fake, "PUT", "/livetv/policies/2").some(
          (c) => (c.body as { disabled: boolean }).disabled,
        ),
      ).toBe(true),
    );
    await waitFor(() => expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull());
    fireEvent.click(screen.getByLabelText("Disable Live TV for this viewer"));
    fireEvent.click(screen.getByLabelText("Allow Cartoons"));
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(fakeCalls(fake, "PUT", "/livetv/policies/2").at(-1)?.body).toMatchObject({
        disabled: false,
        channels: ["c2"],
      }),
    );
  });
});
