import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { vi } from "vitest";
import App from "../App";
import { createFakeServer, type FakeOptions } from "../mock/server";

/**
 * Answer an API call the fake server doesn't model. Return `undefined` to let
 * the fake server handle it.
 */
export type ExtraRoute = (
  method: string,
  path: string,
  body: unknown,
  query: URLSearchParams,
) => unknown;

/**
 * Like `renderApp`, but with extra endpoints layered over the fake backend.
 * Every request, extra or not, is recorded in `fake.state.calls`.
 */
export function renderAppWith(path: string, extra: ExtraRoute, opts: FakeOptions = {}) {
  const fake = createFakeServer(opts);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const p = url.pathname.replace(/^\/ether\/api/, "");
      const method = (init?.method ?? "GET").toUpperCase();
      let body: unknown = undefined;
      if (typeof init?.body === "string" && init.body) {
        try {
          body = JSON.parse(init.body);
        } catch {
          body = init.body;
        }
      }
      const out = p.startsWith("/") ? extra(method, p, body, url.searchParams) : undefined;
      if (out === undefined) return fake.fetch(input, init);
      fake.state.calls.push({ method, path: p + url.search, body });
      if (out instanceof Response) return out;
      return new Response(JSON.stringify(out), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  const utils = render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
  return { ...utils, fake };
}

/** Render one component inside a router, against plain stubbed fetch handlers. */
export function renderRouted(ui: React.ReactNode, path = "/") {
  return render(<MemoryRouter initialEntries={[path]}>{ui}</MemoryRouter>);
}
