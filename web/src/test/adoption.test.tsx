import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import Adoption from "../pages/Adoption";

function backend() {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input); calls.push({ url, init });
    let data: unknown = {};
    if (url.endsWith("/users")) data = { users: [{ id: 7, title: "Sam" }, { id: 12, title: "Alex" }] };
    if (url.endsWith("/migration/preview")) data = { id: "plan-1", imported: false, items: [{ title: "A remembered film", key: 7, views: 1 }], lists: [], warnings: [] };
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  return calls;
}

describe("Library migration drawers", () => {
  it("keeps the import review and explicit import action in the active drawer", async () => {
    const calls = backend(); render(<Adoption />);
    fireEvent.click(screen.getByRole("button", { name: "Start Plex import" }));
    const drawer = await screen.findByRole("dialog", { name: "Import from Plex" });
    await within(drawer).findByRole("option", { name: "Alex" });
    fireEvent.change(screen.getByLabelText("Ether viewer"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("Plex server URL"), { target: { value: "http://source.example:32400" } });
    fireEvent.change(screen.getByLabelText("Source viewer’s Plex token"), { target: { value: "fixture-token" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));
    expect(await within(drawer).findByRole("heading", { name: "Review import" })).toBeInTheDocument();
    expect(within(drawer).getByRole("button", { name: "Import reviewed records" })).toBeInTheDocument();
    expect(calls.some(c => c.url.endsWith("/migration/import"))).toBe(false);
    expect(JSON.parse(String(calls.find(c => c.url.endsWith("/migration/preview"))?.init?.body)).viewers[0].localUserId).toBe(12);
  });

  it("chooses a CSV viewer without opening the Plex migration form", async () => {
    backend(); render(<Adoption />);
    fireEvent.click(screen.getByRole("button", { name: "Open CSV tools" }));
    const drawer = await screen.findByRole("dialog", { name: "CSV import & export" });
    await within(drawer).findByRole("option", { name: "Alex" });
    fireEvent.change(screen.getByLabelText("CSV viewer"), { target: { value: "12" } });
    await waitFor(() => expect(screen.getByLabelText("CSV viewer")).toHaveValue("12"));
    expect(within(drawer).getByRole("button", { name: "Export CSV" })).toBeEnabled();
  });
});
