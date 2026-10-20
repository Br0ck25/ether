import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { BackupsAndImport } from "../pages/settings/Backups";

function backend() {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({ url, init });
      let data: unknown = {};
      if (url.endsWith("/users"))
        data = {
          users: [
            { id: 7, title: "Sam" },
            { id: 12, title: "Alex" },
          ],
        };
      if (url.endsWith("/adoption/backups/"))
        data = {
          snapshots: [
            { name: "ether-2026-10-18.tar.gz", size: 4 * 1048576, created: "2026-10-18T03:00:00Z" },
          ],
          settings: { intervalHours: 24, retain: 7 },
          warning: "",
        };
      if (url.endsWith("/migration/preview"))
        data = {
          id: "plan-1",
          imported: false,
          items: [{ title: "A remembered film", key: 7, views: 1 }],
          lists: [],
          warnings: [],
        };
      return new Response(JSON.stringify(data), { status: 200 });
    }),
  );
  return calls;
}

async function choose(label: string, option: string) {
  fireEvent.click(screen.getByRole("button", { name: label }));
  fireEvent.click(await screen.findByRole("option", { name: option }));
}

describe("Backups & import", () => {
  it("previews a Plex import inline and waits for the explicit import", async () => {
    const calls = backend();
    render(<BackupsAndImport />);
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/users"))).toBe(true));
    await choose("Ether viewer", "Alex");
    fireEvent.change(screen.getByLabelText("Plex server URL"), {
      target: { value: "http://source.example:32400" },
    });
    fireEvent.change(screen.getByLabelText("Source viewer’s Plex token"), {
      target: { value: "fixture-token" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));
    const review = await screen.findByRole("region", { name: "Review import" });
    expect(within(review).getByText(/A remembered film/)).toBeInTheDocument();
    expect(
      within(review).getByRole("button", { name: "Import reviewed records" }),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith("/migration/import"))).toBe(false);
    expect(
      JSON.parse(String(calls.find((c) => c.url.endsWith("/migration/preview"))?.init?.body))
        .viewers[0].localUserId,
    ).toBe(12);
    expect(screen.getByLabelText("Source viewer’s Plex token")).toHaveValue("");
  });

  it("chooses a CSV viewer independently of the Plex import", async () => {
    backend();
    render(<BackupsAndImport />);
    await choose("CSV viewer", "Alex");
    expect(screen.getByRole("button", { name: "CSV viewer" })).toHaveTextContent("Alex");
    expect(screen.getByRole("button", { name: "Ether viewer" })).toHaveTextContent("Sam");
    expect(screen.getByRole("button", { name: /Export CSV/ })).toBeEnabled();
  });

  it("edits the backup schedule in place and saves it from the save bar", async () => {
    const calls = backend();
    render(<BackupsAndImport />);
    expect(screen.queryByRole("button", { name: /Edit backup schedule/ })).toBeNull();
    const every = await screen.findByLabelText("Back up every");
    expect(every).toHaveValue(24);
    expect(await screen.findByText(/ether-2026-10-18|2026/)).toBeInTheDocument();
    fireEvent.change(every, { target: { value: "12" } });
    fireEvent.click(await screen.findByRole("button", { name: /Save changes/ }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/adoption/backups/settings"))).toBe(true),
    );
    const save = calls.find((c) => c.url.endsWith("/adoption/backups/settings"))!;
    expect(save.init?.method).toBe("POST");
    expect(JSON.parse(String(save.init?.body))).toEqual({ intervalHours: 12, retain: 7 });
  });

  it("has no local library tools", async () => {
    const calls = backend();
    render(<BackupsAndImport />);
    await screen.findByLabelText("Back up every");
    expect(screen.queryByText(/Local libraries/i)).toBeNull();
    expect(screen.queryByRole("button", { name: /Add folder|Scan now|Match file/ })).toBeNull();
    expect(calls.some((c) => c.url.includes("/libraries"))).toBe(false);
  });
});
