import { routeApiImagesThroughFetch } from "../lib/api";
import { createFakeServer } from "./server";

/** Route same-origin /ether/api requests to the in-browser fake (mock mode only). */
export function installMockBackend(): void {
  // `?cluster` previews the owner UI as a node of a cluster.
  const cluster = new URLSearchParams(window.location.search).has("cluster");
  const fake = createFakeServer({ latencyMs: 120, cluster });
  const real = window.fetch.bind(window);
  window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(
      input instanceof Request ? input.url : String(input),
      window.location.origin,
    );
    if (url.origin === window.location.origin && url.pathname.startsWith("/ether/api/"))
      return fake.fetch(url.toString(), init);
    return real(input, init);
  };
  // Generated artwork (/ether/api/collections/{id}/artwork) must come from the fake too.
  routeApiImagesThroughFetch(true);
  console.info("[ether] mock backend active: all /ether/api calls are served in the browser.");
}
