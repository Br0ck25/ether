import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import LiveTV from "../pages/livetv/LiveTV";

function backend(channels: unknown[] = []) {
 const calls: { url: string; init?: RequestInit }[] = [];
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = String(input); calls.push({ url, init }); let data: unknown = [];
  if (url.endsWith("/livetv/channels")) data = channels;
  else if (url.endsWith("/users")) data = { users: [{ id: 12, title: "Sam", username: "sam" }] };
  else if (url.endsWith("/collections")) data = { collections: [{ id: "42", ratingKey: "6000000000000042", name: "Family favourites", itemCount: 8 }] };
  else if (url.includes("/catalog/search")) data = { items: [{ key: "1000000000000007", type: "movie", title: "Adventure", runtimeMinutes: 108, contentRating: "PG" }] };
  else if (url.endsWith("/livetv/policies/12")) data = { userId: 12, disabled: false, channels: [], favorites: [], maxRating: "" };
  return new Response(JSON.stringify(data), { status: 200 });
 }));
 return calls;
}
const channelSave = (calls: ReturnType<typeof backend>) => calls.find(c => c.url.endsWith("/livetv/channels") && c.init?.method === "POST");

describe("Live TV owner programming", () => {
 it("finds channels beyond the first page and narrows to favourites", async () => {
  backend(Array.from({length: 25}, (_, i) => ({id: String(i), number: String(i + 1), name: `Station ${i + 1}`, kind: "iptv", enabled: true, favorite: i === 24, group: i === 24 ? "Weather" : "General"})));
  render(<LiveTV />);
  const search = await screen.findByRole("searchbox", {name: "Search channels"});
  expect(screen.queryByText("★ Station 25")).not.toBeInTheDocument();
  fireEvent.change(search, {target: {value: "Weather"}});
  expect(screen.getByText("★ Station 25")).toBeInTheDocument();
  expect(screen.queryByText("Station 1")).not.toBeInTheDocument();
  fireEvent.change(search, {target: {value: ""}});
  fireEvent.click(screen.getByRole("switch", {name: "Favourites only"}));
  expect(screen.getByText("★ Station 25")).toBeInTheDocument();
  expect(screen.queryByText("Station 1")).not.toBeInTheDocument();
 });
 it("builds a channel from a catalogue search and named viewer without raw keys", async () => {
  const calls = backend(); render(<LiveTV />);
  await screen.findByRole("button", { name: "Create channel" });
  fireEvent.click(screen.getByRole("button", { name: "Create channel" }));
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Movie night" } });
  fireEvent.click(screen.getByRole("tab",{name:"Programming"}));
  fireEvent.change(screen.getByLabelText("Search titles"), { target: { value: "Adventure" } });
  fireEvent.click(await screen.findByRole("button", { name: /Adventure/ }));
  expect(await screen.findByRole("button", { name: "Remove Adventure" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("tab",{name:"Viewers"}));
  fireEvent.click(await screen.findByLabelText("Sam"));
  fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
  await waitFor(() => expect(channelSave(calls)).toBeDefined());
  const saved = JSON.parse(String(channelSave(calls)?.init?.body));
  expect(saved).toMatchObject({ name: "Movie night", allowedUsers: [12], items: [{ key: 1000000000000007, title: "Adventure", durationMs: 6480000, rating: "PG" }] });
  expect(screen.queryByText(/catalogue key \| title/)).not.toBeInTheDocument();
 });
 it("selects a collection by name and submits its published identity", async () => {
  const calls = backend(); render(<LiveTV />);
  await waitFor(() => expect(calls.some(c => c.url.endsWith("/collections"))).toBe(true));
  fireEvent.click(screen.getByRole("button", { name: "Create channel" }));
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Family TV" } });
  fireEvent.click(screen.getByRole("tab",{name:"Programming"}));
  fireEvent.change(screen.getByLabelText("Programming source"), { target: { value: "collection" } });
  fireEvent.change(await screen.findByLabelText(/^Collection/), { target: { value: "6000000000000042" } });
  expect(screen.getByRole("option", { name: "Family favourites · 8 titles" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
  await waitFor(() => expect(channelSave(calls)).toBeDefined());
  expect(JSON.parse(String(channelSave(calls)?.init?.body)).collection).toBe(6000000000000042);
 });
 it("explains an empty time block and lets its programming be selected visually", async () => {
  const calls = backend(); render(<LiveTV />);
  fireEvent.click(screen.getByRole("button", { name: "80s action template" }));
  fireEvent.click(screen.getByRole("tab",{name:"Schedule"}));
  fireEvent.click(screen.getByRole("button", { name: "Add time block" }));
  fireEvent.click(screen.getByRole("button", { name: "Preview six hours" }));
  expect(await screen.findByRole("status")).toHaveTextContent("Add at least one movie to each time block");
  expect(calls.some(c => c.url.endsWith("/livetv/preview"))).toBe(false);
  const block = screen.getByLabelText("Time block 1 programming");
  fireEvent.change(within(block).getByLabelText("Search titles"), { target: { value: "Adventure" } });
  fireEvent.click(await within(block).findByRole("button", { name: /Adventure/ }));
  await within(block).findByRole("button", { name: "Remove Adventure" });
  fireEvent.click(screen.getByRole("button", { name: "Preview six hours" }));
  await waitFor(() => expect(calls.some(c => c.url.endsWith("/livetv/preview"))).toBe(true));
  const preview = calls.find(c => c.url.endsWith("/livetv/preview"));
  expect(JSON.parse(String(preview?.init?.body))).toMatchObject({ filter: { yearFrom: 1980, yearTo: 1989, genres: ["Action"] }, blocks: [{ start: 0, end: 60, items: [{ key: 1000000000000007 }] }] });
 });
 it("opens viewer policy using the selected account rather than a typed ID", async () => {
  const calls = backend(); render(<LiveTV />);
  fireEvent.click(screen.getByRole("tab", { name: "Viewer access" }));
  await screen.findByRole("option", { name: "Sam" });
  fireEvent.change(screen.getByLabelText("Viewer"), { target: { value: "12" } });
  fireEvent.click(await screen.findByRole("button",{name:"Edit Live TV access"}));
  fireEvent.click(await screen.findByLabelText("Disable Live TV for this viewer"));
  fireEvent.click(screen.getByRole("button", { name: "Save viewer access" }));
  await waitFor(() => expect(calls.some(c => c.url.endsWith("/livetv/policies/12") && c.init?.method === "PUT" && JSON.parse(String(c.init.body)).disabled)).toBe(true));
 });
});
