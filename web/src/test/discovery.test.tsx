import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import Discovery from "../pages/Discovery";

function backend() {
 const calls: { url: string; init?: RequestInit }[] = [];
 const settings = { enabled: false, plex: true, trakt: true, notify: false, prewarm: false, checkHours: 6, naturalSearch: false, recaps: false };
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = String(input); calls.push({ url, init }); let value: unknown = {};
  if (url.endsWith("/users")) value = { users: [{ id: 1, title: "Owner", username: "owner", isOwner: true }] };
  else if (url.endsWith("/discovery/settings")) value = init?.method === "PUT" ? JSON.parse(String(init.body)) : settings;
  else if (url.endsWith("/discovery/ratings/settings")) value = { enabled: false, provider: "omdb", apiKey: "", critic: "tomatoes", audience: "imdb", cacheHours: 168 };
  else if (url.endsWith("/discovery/overlays/settings")) value = { enabled: false, status: false, quality: false, ratings: false, personalize: false, localize: false };
  else if (url.includes("/discovery/users/")) value = [];
  return new Response(JSON.stringify(value), { status: 200 });
 })); return calls;
}
describe("Discovery owner controls", () => {
 it("starts monitoring and notifications off and saves opt-in settings", async () => {
  const calls = backend(); render(<Discovery />);
  const enabled = await screen.findByLabelText("Enable watchlist monitoring");
  expect(enabled).not.toBeChecked();
  expect(screen.getByLabelText("Send availability events to configured owner notification channels")).not.toBeChecked();
  fireEvent.click(enabled); fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(calls.some(call => call.url.endsWith("/discovery/settings") && call.init?.method === "PUT" && JSON.parse(String(call.init.body)).enabled && !JSON.parse(String(call.init.body)).notify)).toBe(true));
 });
 it("persists LLM feature toggles through the discovery settings API", async () => {
  const calls = backend(); render(<Discovery />); await screen.findByLabelText("Enable watchlist monitoring");
  fireEvent.click(screen.getByRole("button", { name: "Search & recaps" }));
  const search = screen.getByLabelText(/Natural-language search in Plex/); expect(search).not.toBeChecked();
  fireEvent.click(search); fireEvent.click(screen.getByRole("button", { name: "Save search settings" }));
  await waitFor(() => expect(calls.some(call => call.init?.method === "PUT" && JSON.parse(String(call.init.body)).naturalSearch)).toBe(true));
 });
});
