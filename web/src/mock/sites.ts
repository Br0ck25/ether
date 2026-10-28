/**
 * In-memory fake of the built-in indexer sites API (/ether/api/indexer-sites):
 * installed definitions, a definitions source, uploads, updates and tests.
 * The sites are fictional (".invalid" addresses); used by mock mode and the
 * tests, never shipped.
 */
import type {
  CatalogEntry,
  EtherConfig,
  Indexer,
  SiteDefinition,
  SiteTestStatus,
} from "../lib/types";

type Reply = { status: number; body: unknown };

const ok = (body: unknown, status = 200): Reply => ({ status, body });
const fail = (status: number, code: string, message: string): Reply => ({
  status,
  body: { error: { code, message } },
});

const INSTALLED_AT = "2026-09-01T10:00:00Z";

function exampleTracker(): SiteDefinition {
  return {
    id: "example-tracker",
    name: "Example Tracker",
    description: "A fictional public tracker for mock mode",
    language: "en-US",
    type: "public",
    protocol: "torrent",
    links: ["https://example-tracker.invalid/", "https://mirror.example-tracker.invalid/"],
    settings: [
      {
        name: "sort",
        type: "select",
        label: "Sort requested from site",
        default: "added",
        options: [
          { value: "added", label: "Date added" },
          { value: "seeders", label: "Seeders" },
        ],
      },
      { name: "freeleech", type: "checkbox", label: "Search freeleech only", default: "false" },
      {
        name: "info_note",
        type: "info",
        label: "About this site",
        default: "No account needed.",
      },
    ],
    movies: true,
    tv: true,
    source: "source",
    installedAt: INSTALLED_AT,
    updatedAt: INSTALLED_AT,
  };
}

function examplePrivate(): SiteDefinition {
  return {
    id: "example-private",
    name: "Example Private",
    description: "A fictional private tracker with a sign-in form",
    language: "en-US",
    type: "private",
    protocol: "torrent",
    links: ["https://example-private.invalid/"],
    settings: [
      { name: "username", type: "text", label: "Username" },
      { name: "password", type: "password", label: "Password" },
    ],
    movies: true,
    tv: false,
    login: "form",
    source: "upload",
    warnings: ['filter "sparkle" is not supported and will be skipped'],
    installedAt: INSTALLED_AT,
    updatedAt: INSTALLED_AT,
  };
}

/** Definitions a configured source offers, beyond the installed ones. */
const SOURCE_ONLY: SiteDefinition[] = [
  {
    id: "sample-anime",
    name: "Sample Anime",
    description: "A fictional anime tracker",
    language: "ja-JP",
    type: "semi-private",
    protocol: "torrent",
    links: ["https://sample-anime.invalid/"],
    settings: [{ name: "cookie", type: "text", label: "Cookie" }],
    movies: true,
    tv: true,
    login: "cookie",
    source: "source",
    installedAt: INSTALLED_AT,
    updatedAt: INSTALLED_AT,
  },
  {
    id: "sample-usenet",
    name: "Sample Usenet",
    description: "A fictional Usenet search site",
    language: "en-US",
    type: "public",
    protocol: "usenet",
    links: ["https://sample-usenet.invalid/"],
    settings: [],
    movies: true,
    tv: true,
    source: "source",
    installedAt: INSTALLED_AT,
    updatedAt: INSTALLED_AT,
  },
];

export function createSitesMock() {
  const state = {
    definitions: [exampleTracker(), examplePrivate()] as SiteDefinition[],
    statuses: [] as SiteTestStatus[],
  };

  function catalog(): CatalogEntry[] {
    const installed = new Set(state.definitions.map((d) => d.id));
    return [exampleTracker(), ...SOURCE_ONLY].map((d) => ({
      id: d.id,
      file: `${d.id}.yml`,
      installed: installed.has(d.id),
    }));
  }

  function parseUpload(yaml: string): SiteDefinition | string {
    const field = (k: string) => yaml.match(new RegExp(`^${k}:\\s*(.+)$`, "m"))?.[1]?.trim();
    const id = field("id");
    const name = field("name");
    const problems = [
      id ? "" : "id is required",
      name ? "" : "name is required",
      /^links:/m.test(yaml) ? "" : "links must list at least one site URL",
    ].filter(Boolean);
    if (problems.length) return problems.join("; ");
    return {
      id: id!,
      name: name!,
      protocol: "torrent",
      links: [yaml.match(/https?:\/\/\S+/)?.[0] ?? "https://site.invalid/"],
      settings: [],
      movies: true,
      tv: true,
      source: "upload",
      installedAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    };
  }

  function handle(method: string, seg: string[], body: unknown, config: EtherConfig): Reply | null {
    if (seg[0] !== "indexer-sites") return null;
    const route = `${method} /${seg.slice(1).join("/")}`;
    switch (route) {
      case "GET /definitions":
        return ok({ definitions: state.definitions });
      case "POST /definitions": {
        const parsed = parseUpload(String((body as { yaml?: string })?.yaml ?? ""));
        if (typeof parsed === "string")
          return ok(
            {
              error: {
                code: "invalid_definition",
                message: `invalid definition: ${parsed}`,
                problems: parsed.split("; "),
              },
            },
            400,
          );
        state.definitions = [...state.definitions.filter((d) => d.id !== parsed.id), parsed];
        return ok({ definition: parsed });
      }
      case "GET /catalog":
        if (!config.indexerSites?.definitionsUrl)
          return fail(409, "no_source", "set a definitions source URL first");
        return ok({ entries: catalog() });
      case "POST /catalog/install": {
        const id = (body as { id?: string })?.id;
        const def = [exampleTracker(), ...SOURCE_ONLY].find((d) => d.id === id);
        if (!def)
          return fail(502, "definition_error", `the definitions source does not offer "${id}"`);
        state.definitions = [...state.definitions.filter((d) => d.id !== def.id), def];
        return ok({ definition: def });
      }
      case "POST /update": {
        const fromSource = state.definitions.filter((d) => d.source === "source");
        return ok({
          updated: fromSource.slice(0, 1).map((d) => d.id),
          unchanged: Math.max(0, fromSource.length - 1),
          failed: [],
        });
      }
      case "POST /test": {
        const ix = body as Indexer;
        const def = state.definitions.find((d) => d.id === ix.definition);
        const result = !def
          ? { ok: false, results: 0, error: `"${ix.definition}": definition is not installed` }
          : ix.secretSettings?.password === "wrong"
            ? {
                ok: false,
                results: 0,
                error: `${def.name}: sign-in failed: Wrong username or password`,
              }
            : {
                ok: true,
                results: 37,
                titles: [
                  "Example.Film.2024.1080p.WEB-DL",
                  "Example.Show.S01E01.720p.HDTV",
                  "Another.Film.2023.2160p",
                ],
              };
        if (ix.id)
          state.statuses = [
            ...state.statuses.filter((s) => s.indexerId !== ix.id),
            {
              indexerId: ix.id,
              ok: result.ok,
              message: result.ok ? `${result.results} results` : result.error,
              results: result.results,
              testedAt: new Date().toISOString(),
            },
          ];
        return ok({ ...result, durationMs: 420 });
      }
      case "GET /status":
        return ok({ statuses: state.statuses });
    }
    if (method === "GET" && seg[1] === "definitions" && seg[2]) {
      const def = state.definitions.find((d) => d.id === seg[2]);
      return def ? ok({ definition: def }) : fail(404, "not_found", "definition is not installed");
    }
    if (method === "DELETE" && seg[1] === "definitions" && seg[2]) {
      const users = (config.indexers ?? []).filter(
        (ix) => ix.kind === "cardigann" && ix.definition === seg[2],
      );
      if (users.length)
        return fail(
          409,
          "in_use",
          `remove the indexers that use this definition first: ${users.map((u) => u.name).join(", ")}`,
        );
      state.definitions = state.definitions.filter((d) => d.id !== seg[2]);
      return ok({ removed: seg[2] });
    }
    return fail(404, "not_found", "no such API endpoint");
  }

  return { state, handle };
}
