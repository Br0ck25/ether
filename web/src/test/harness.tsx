import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { vi } from "vitest";
import App from "../App";
import { createFakeServer, type FakeOptions } from "../mock/server";

/** Render the whole app at `path` against a fresh in-memory fake backend. */
export function renderApp(
  path: string,
  opts: FakeOptions = {},
  setup?: (fake: ReturnType<typeof createFakeServer>) => void,
) {
  const fake = createFakeServer(opts);
  setup?.(fake);
  vi.stubGlobal("fetch", vi.fn(fake.fetch));
  const utils = render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
  return { ...utils, fake };
}

/** Requests the app made, optionally filtered by method and path prefix. */
export function calls(
  fake: ReturnType<typeof createFakeServer>,
  method: string,
  pathPrefix: string,
) {
  return fake.state.calls.filter((c) => c.method === method && c.path.startsWith(pathPrefix));
}
