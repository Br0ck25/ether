import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import StreamPreferences from "../pages/StreamPreferences";

it("opens an empty installation with nullable language lists and saves preferences", async () => {
  let saved: Record<string, unknown> | undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      let data: unknown = [];
      if (url.endsWith("/users")) data = { users: [] };
      else if (url.endsWith("/stream-preferences/0")) {
        data = {
          audioLanguages: null,
          subtitleLanguages: null,
          subtitleMode: "off",
          subtitleStyle: "normal",
        };
        if (init?.method === "PUT") {
          saved = JSON.parse(String(init.body));
          data = saved;
        }
      }
      return new Response(JSON.stringify(data), { status: 200 });
    }),
  );
  render(<StreamPreferences />);
  fireEvent.click(screen.getByRole("button", { name: "Edit preferences" }));
  const audio = screen.getByLabelText("Audio languages, in preference order");
  await waitFor(() => expect(audio).toBeEnabled());
  expect(audio).toHaveValue("");
  fireEvent.change(audio, { target: { value: "en, fr" } });
  fireEvent.click(screen.getByRole("button", { name: "Save preferences" }));
  await waitFor(() => expect(saved?.audioLanguages).toEqual(["en", "fr"]));
  expect(await screen.findByRole("status")).toHaveTextContent("Stream preferences saved");
});

it("loads viewer languages and restores both language fields from owner defaults", async () => {
  let overridden = true;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      let data: unknown = [];
      if (url.endsWith("/users")) data = { users: [{ id: 7, title: "Sam", username: "sam" }] };
      else if (url.endsWith("/stream-preferences/0"))
        data = { audioLanguages: ["en"], subtitleLanguages: ["fr"] };
      else if (url.endsWith("/stream-preferences/7")) {
        if (init?.method === "DELETE") {
          overridden = false;
          data = { ok: true };
        } else
          data = overridden
            ? { audioLanguages: ["ja"], subtitleLanguages: ["en"] }
            : { audioLanguages: ["en"], subtitleLanguages: ["fr"] };
      }
      return new Response(JSON.stringify(data), { status: 200 });
    }),
  );
  render(<StreamPreferences />);
  fireEvent.click(screen.getByRole("button", { name: "Edit preferences" }));
  const audio = screen.getByLabelText("Audio languages, in preference order");
  const subtitles = screen.getByLabelText("Subtitle languages, in preference order");
  await waitFor(() => expect(audio).toHaveValue("en"));
  fireEvent.change(screen.getByLabelText("Viewer"), { target: { value: "7" } });
  await waitFor(() => expect(audio).toHaveValue("ja"));
  expect(subtitles).toHaveValue("en");
  fireEvent.change(audio, { target: { value: "de" } });
  fireEvent.click(screen.getByRole("button", { name: "Use owner defaults" }));
  await waitFor(() => expect(audio).toHaveValue("en"));
  expect(subtitles).toHaveValue("fr");
  expect(await screen.findByRole("status")).toHaveTextContent("Owner defaults restored");
});
