import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import Insights from "../pages/Insights";
const emptySummary = {
  plays: 0,
  watchedMs: 0,
  bytes: 0,
  completed: 0,
  daily: [],
  hourly: [],
  platforms: [],
  titles: [],
  users: [],
};
function backend() {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({ url, init });
      let v: unknown = { ok: true };
      if (url.includes("/stats/summary")) v = emptySummary;
      else if (url.includes("/stats/history")) v = [];
      else if (url.endsWith("/users"))
        v = {
          users: [
            { id: 7, title: "Sam", username: "sam-account", isOwner: true },
            { id: 12, title: "Alex", username: "alex-account", isOwner: false },
          ],
        };
      else if (url.endsWith("/status/sessions"))
        v = {
          value: [
            {
              ID: "live",
              TitleName: "Example film",
              Username: "Sam",
              Decision: "directplay",
              SpeedBps: 125000,
              Client: { deviceName: "Phone" },
            },
          ],
        };
      else if (url.endsWith("/stats/infrastructure"))
        v = {
          snapshots: {
            providers: {
              at: 1,
              data: [
                {
                  id: "provider-key",
                  name: "My news provider",
                  host: "news.example",
                  state: "ok",
                  avgLatency: 0,
                },
              ],
            },
            indexers: {
              at: 1,
              data: [
                {
                  indexer: "My indexer",
                  hitsUsed: 2,
                  hitsRemaining: 98,
                  grabsUsed: 1,
                  grabsRemaining: 49,
                },
              ],
            },
          },
          providerMonth: [],
          indexerMonth: [],
          costs: [],
          notes: [],
        };
      else if (url.endsWith("/notifications/"))
        v = { channels: [], events: ["playback.error", "newsletter.weekly"], deliveries: [] };
      else if (url.endsWith("/status/hwaccel"))
        v = { value: { hwaccel: { name: "none", encoders: [] } } };
      else if (url.endsWith("/newsletter/settings") && !init?.method)
        v = {
          enabled: false,
          weekday: 1,
          hour: 9,
          userId: 0,
          nextDue: 0,
          lastAttempt: 0,
          lastStatus: "",
        };
      return new Response(JSON.stringify(v), { status: 200 });
    }),
  );
  return calls;
}
describe("Owner insights", () => {
  it("shows actual live streams and sends stop action", async () => {
    const calls = backend();
    render(<Insights />);
    expect(await screen.findByText("Example film")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop stream" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.url.endsWith("/stats/sessions/live/stop") && c.init?.method === "POST"),
      ).toBe(true),
    );
    fireEvent.click(screen.getByRole("tab", { name: "History" }));
    expect(
      screen.getByText(
        "History starts when Ether observes playback. There are no recorded plays in this period.",
      ),
    ).toBeInTheDocument();
  });
  it("filters by a named viewer while sending the existing account ID", async () => {
    const calls = backend();
    render(<Insights />);
    await screen.findByText("Example film");
    fireEvent.click(screen.getByRole("tab", { name: "History" }));
    expect(screen.getByRole("option", { name: "Sam (owner)" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Viewer"), { target: { value: "12" } });
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/stats/summary?days=30&user=12"))).toBe(true),
    );
  });
  it("saves an opt-in weekly schedule for a named viewer and real channel rules", async () => {
    const calls = backend();
    render(<Insights />);
    await screen.findByText("Example film");
    fireEvent.click(screen.getByRole("tab", { name: "Weekly report" }));
    fireEvent.click(screen.getByRole("button", { name: "Configure weekly report" }));
    fireEvent.click(screen.getByLabelText("Email the weekly report"));
    fireEvent.change(screen.getByLabelText("Report viewer"), { target: { value: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview weekly report" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/stats/newsletter?user=12"))).toBe(true),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save weekly schedule" })).not.toBeDisabled(),
    );
    expect(screen.getByLabelText("Email the weekly report")).toBeChecked();
    expect(screen.getByLabelText("Report viewer")).toHaveValue("12");
    fireEvent.click(screen.getByRole("button", { name: "Save weekly schedule" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url.endsWith("/newsletter/settings") &&
            c.init?.method === "PUT" &&
            JSON.parse(String(c.init.body)).enabled &&
            JSON.parse(String(c.init.body)).userId === 12,
        ),
      ).toBe(true),
    );
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    fireEvent.click(screen.getByRole("tab", { name: "Notifications" }));
    fireEvent.click(screen.getByRole("button", { name: "New channel" }));
    fireEvent.change(screen.getByLabelText("Endpoint URL"), {
      target: { value: "https://notify.example/hook" },
    });
    fireEvent.click(screen.getByLabelText("Weekly newsletter"));
    fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url.includes("/notifications/") &&
            c.init?.method === "PUT" &&
            JSON.parse(String(c.init.body)).events.includes("newsletter.weekly"),
        ),
      ).toBe(true),
    );
  });
  it("saves provider and indexer prices from existing services without asking for IDs", async () => {
    const calls = backend();
    render(<Insights />);
    await screen.findByText("Example film");
    fireEvent.click(screen.getByRole("tab", { name: "Providers & indexers" }));
    expect(screen.queryByLabelText("Service ID")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit plan price" }));
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: "provider-key" } });
    fireEvent.change(screen.getByLabelText("Monthly price"), { target: { value: "8.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Save plan price" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url.endsWith("/stats/costs") &&
            JSON.parse(String(c.init?.body)).id === "provider-key" &&
            JSON.parse(String(c.init?.body)).monthly === 8.5,
        ),
      ).toBe(true),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    fireEvent.click(screen.getByRole("tab", { name: "Costs" }));
    fireEvent.click(screen.getByRole("button", { name: "Add plan price" }));
    fireEvent.change(screen.getByLabelText("Service kind"), { target: { value: "indexer" } });
    expect(screen.getByLabelText("Service")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Save plan price" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: "My indexer" } });
    fireEvent.click(screen.getByRole("button", { name: "Save plan price" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url.endsWith("/stats/costs") &&
            JSON.parse(String(c.init?.body)).kind === "indexer" &&
            JSON.parse(String(c.init?.body)).id === "My indexer",
        ),
      ).toBe(true),
    );
  });
});
