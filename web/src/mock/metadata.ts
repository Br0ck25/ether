import { normalizeOverlays, type Overlays } from "../pages/metadata/model";

/** Offline editing fixtures. Production previews always use the Go renderer. */
export function createMetadataMock() {
  let overlays = normalizeOverlays({ enabled: true, quality: true, ratings: true, size: "small" });
  let ratings = {
    enabled: false,
    provider: "omdb",
    apiKey: "",
    critic: "tomatoes",
    audience: "imdb",
    cacheHours: 168,
  };
  const artwork = new Map<string, unknown>();
  return (
    method: string,
    path: string,
    body: unknown,
    query: URLSearchParams,
  ): Response | undefined => {
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), { headers: { "Content-Type": "application/json" } });
    if (path === "/discovery/overlays/settings") {
      if (method === "PUT") overlays = normalizeOverlays(body as Overlays);
      return json(overlays);
    }
    if (path === "/discovery/ratings/settings") {
      if (method === "PUT") ratings = { ...(body as typeof ratings), apiKey: "[configured]" };
      return json(ratings);
    }
    if (path.startsWith("/discovery/ratings/refresh/"))
      return json([{ source: "imdb", value: 8.6, scale: 10 }]);
    if (path.startsWith("/discovery/overlays/artwork/")) {
      if (method === "PUT") artwork.set(path, body);
      return json(artwork.get(path) ?? { posters: [], backdrops: [] });
    }
    if (path.startsWith("/discovery/overlays/variants/"))
      return json({ posters: [], backdrops: [] });
    if (path.startsWith("/discovery/overlays/explain/")) {
      const s = normalizeOverlays(JSON.parse(query.get("settings") || "{}"));
      const facts = {
        status: ["NEW SEASON"],
        quality: [],
        ratings: ["TMDB 8.6/10"],
        details: ["2025 · 120m"],
      };
      const layers = (Object.keys(facts) as (keyof typeof facts)[]).map((key) => ({
        key,
        state: !s.enabled || !s[key] ? "off" : facts[key].length ? "ready" : "unavailable",
        labels: s.enabled && s[key] ? facts[key] : [],
        reason:
          key === "quality"
            ? "No verified playback yet; release-name guesses are excluded"
            : "Catalogue fixture data",
      }));
      return json({ labels: layers.flatMap((l) => l.labels), layers });
    }
    if (path === "/discovery/overlays/sample" || path.startsWith("/discovery/overlays/preview/")) {
      const s = normalizeOverlays(JSON.parse(query.get("settings") || "{}"));
      const labels = s.enabled
        ? [
            s.status && "NEW SEASON",
            s.quality && path === "/discovery/overlays/sample" && "4K HDR",
            s.ratings && "TMDB 8.6/10",
            s.details && "2025 · 120m",
          ].filter(Boolean)
        : [];
      const color = /^#[0-9a-f]{6}$/i.test(s.accent) ? s.accent : "#d4a574";
      const fontSize = s.size === "small" ? 15 : s.size === "large" ? 22 : 18;
      const y = s.position === "top" ? 28 : 678;
      const badges = labels
        .map(
          (label, i) =>
            `<g transform="translate(${22 + i * 153},${y})"><rect width="145" height="39" rx="9" fill="#14202a" fill-opacity="${s.style === "solid" ? 1 : 0.8}"/><circle cx="14" cy="20" r="3" fill="${color}"/><text x="25" y="26" fill="#f6f5f1" font-size="${fontSize}" font-family="sans-serif">${label}</text></g>`,
        )
        .join("");
      return new Response(
        `<svg xmlns="http://www.w3.org/2000/svg" width="500" height="750" viewBox="0 0 500 750"><defs><radialGradient id="sky" cx="60%" cy="30%"><stop stop-color="#79604e"/><stop offset="1" stop-color="#0e1e2b"/></radialGradient></defs><path fill="url(#sky)" d="M0 0h500v750H0z"/><circle cx="290" cy="200" r="70" fill="#efc38f"/><path d="M0 390Q80 290 160 420T500 350V750H0" fill="#142f39"/><path d="M0 530Q120 450 250 535T500 470V750H0" fill="#0b1f2b"/><g fill="#f4eada" text-anchor="middle" font-family="sans-serif" font-weight="bold"><text x="250" y="460" font-size="45">AFTER</text><text x="250" y="512" font-size="45">THE TIDE</text><text x="250" y="545" font-size="11">ETHER ORIGINAL ART · SAMPLE FACTS</text></g>${badges}</svg>`,
        { headers: { "Content-Type": "image/svg+xml" } },
      );
    }
  };
}
