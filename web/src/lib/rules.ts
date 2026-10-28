import type {
  CollectionSort,
  CollectionSpec,
  FacetName,
  LookupKind,
  LookupSourceKind,
  Rule,
  RuleField,
  RuleOp,
  Source,
  SourceKind,
  TmdbListName,
} from "./contract";

/**
 * Rule value encoding (the contract leaves `value` typed by field):
 *   in / notIn   → array of keys (facet keys as strings, lookup ids as numbers)
 *   is / isNot   → a single key, number or boolean
 *   gte / lte    → a number, or an ISO date (YYYY-MM-DD) for date fields
 *   between      → [min, max] with the same element type as gte/lte
 *   within       → a relative window "<n><unit>", unit d|w|m|y ("30d", "6m")
 * Lookup-backed rules also carry `label`: the display names joined by ", ".
 */

export type ValueKind = "facet" | "lookup" | "number" | "date" | "bool";

export interface FieldMeta {
  field: RuleField;
  label: string;
  group: "Content" | "People & companies" | "Numbers" | "Dates" | "Viewer";
  kind: ValueKind;
  ops: RuleOp[];
  defaultOp: RuleOp;
  facet?: FacetName;
  lookup?: LookupKind;
  unit?: string;
  min?: number;
  max?: number;
  step?: number;
  hint: string;
}

const SET_OPS: RuleOp[] = ["in", "notIn"];
const NUM_OPS: RuleOp[] = ["gte", "lte", "between", "is"];
const DATE_OPS: RuleOp[] = ["within", "gte", "lte", "between"];

export const FIELDS: FieldMeta[] = [
  {
    field: "genre",
    label: "Genre",
    group: "Content",
    kind: "facet",
    facet: "genre",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "TMDB genres",
  },
  {
    field: "keyword",
    label: "Keyword",
    group: "Content",
    kind: "lookup",
    lookup: "keyword",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Themes like “time travel” or “heist”",
  },
  {
    field: "decade",
    label: "Decade",
    group: "Content",
    kind: "facet",
    facet: "decade",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Release decade",
  },
  {
    field: "contentRating",
    label: "Content rating",
    group: "Content",
    kind: "facet",
    facet: "contentRating",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Certification such as PG-13 or TV-MA",
  },
  {
    field: "country",
    label: "Country",
    group: "Content",
    kind: "facet",
    facet: "country",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Production country",
  },
  {
    field: "language",
    label: "Original language",
    group: "Content",
    kind: "facet",
    facet: "language",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Original spoken language",
  },
  {
    field: "collection",
    label: "TMDB collection",
    group: "Content",
    kind: "lookup",
    lookup: "collection",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Franchise collections such as “Alien”",
  },
  {
    field: "watchProvider",
    label: "Streaming on",
    group: "Content",
    kind: "lookup",
    lookup: "watchProvider",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Where the title streams in your region",
  },
  {
    field: "person",
    label: "Person",
    group: "People & companies",
    kind: "lookup",
    lookup: "person",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Cast or crew",
  },
  {
    field: "studio",
    label: "Studio",
    group: "People & companies",
    kind: "lookup",
    lookup: "company",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Production company",
  },
  {
    field: "network",
    label: "Network",
    group: "People & companies",
    kind: "lookup",
    lookup: "network",
    ops: SET_OPS,
    defaultOp: "in",
    hint: "Original TV network",
  },
  {
    field: "year",
    label: "Year",
    group: "Numbers",
    kind: "number",
    ops: NUM_OPS,
    defaultOp: "gte",
    min: 1890,
    max: 2100,
    step: 1,
    hint: "Release year",
  },
  {
    field: "rating",
    label: "Rating",
    group: "Numbers",
    kind: "number",
    ops: NUM_OPS,
    defaultOp: "gte",
    min: 0,
    max: 10,
    step: 0.1,
    unit: "/ 10",
    hint: "TMDB vote average",
  },
  {
    field: "votes",
    label: "Votes",
    group: "Numbers",
    kind: "number",
    ops: NUM_OPS,
    defaultOp: "gte",
    min: 0,
    step: 50,
    hint: "Number of TMDB votes",
  },
  {
    field: "popularity",
    label: "Popularity",
    group: "Numbers",
    kind: "number",
    ops: NUM_OPS,
    defaultOp: "gte",
    min: 0,
    step: 1,
    hint: "TMDB popularity score",
  },
  {
    field: "runtime",
    label: "Runtime",
    group: "Numbers",
    kind: "number",
    ops: NUM_OPS,
    defaultOp: "lte",
    min: 0,
    max: 600,
    step: 5,
    unit: "min",
    hint: "Minutes (episode length for shows)",
  },
  {
    field: "released",
    label: "Released",
    group: "Dates",
    kind: "date",
    ops: DATE_OPS,
    defaultOp: "within",
    hint: "First release or air date",
  },
  {
    field: "addedAt",
    label: "Added to Ether",
    group: "Dates",
    kind: "date",
    ops: DATE_OPS,
    defaultOp: "within",
    hint: "When the title entered your catalogue",
  },
  {
    field: "watched",
    label: "Watched by the viewer",
    group: "Viewer",
    kind: "bool",
    ops: ["is"],
    defaultOp: "is",
    hint: "Evaluated per viewer",
  },
  {
    field: "inProgress",
    label: "In progress for the viewer",
    group: "Viewer",
    kind: "bool",
    ops: ["is"],
    defaultOp: "is",
    hint: "Started but not finished",
  },
];

export const FIELD_BY_NAME = Object.fromEntries(FIELDS.map((f) => [f.field, f])) as Record<
  RuleField,
  FieldMeta
>;

export function fieldMeta(field: string): FieldMeta | undefined {
  return (FIELD_BY_NAME as Record<string, FieldMeta | undefined>)[field];
}

export const OP_LABELS: Record<RuleOp, string> = {
  is: "is",
  isNot: "is not",
  in: "is any of",
  notIn: "is none of",
  gte: "is at least",
  lte: "is at most",
  between: "is between",
  within: "in the last",
};

export function opLabel(op: RuleOp, kind?: ValueKind): string {
  if (kind === "date") {
    if (op === "gte") return "on or after";
    if (op === "lte") return "on or before";
  }
  return OP_LABELS[op];
}

export const WITHIN_UNITS = [
  { value: "d", label: "days" },
  { value: "w", label: "weeks" },
  { value: "m", label: "months" },
  { value: "y", label: "years" },
] as const;

export function parseWithin(v: unknown): { n: number; unit: string } {
  const m = typeof v === "string" ? /^(\d+)\s*([dwmy])$/.exec(v.trim()) : null;
  return m ? { n: Number(m[1]), unit: m[2] ?? "d" } : { n: 30, unit: "d" };
}

export function formatWithin(v: unknown): string {
  const { n, unit } = parseWithin(v);
  const label = WITHIN_UNITS.find((u) => u.value === unit)?.label ?? "days";
  return `${n} ${n === 1 ? label.slice(0, -1) : label}`;
}

function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/** A fresh value for the field/op pair, reusing what's compatible from `prev`. */
export function valueFor(meta: FieldMeta, op: RuleOp, prev?: unknown): unknown {
  const list = Array.isArray(prev)
    ? prev
    : prev === undefined || prev === null || prev === ""
      ? []
      : [prev];
  switch (meta.kind) {
    case "bool":
      return typeof prev === "boolean" ? prev : true;
    case "facet":
    case "lookup":
      if (op === "in" || op === "notIn") return list;
      return list[0] ?? "";
    case "number": {
      const first = typeof list[0] === "number" ? list[0] : undefined;
      const fallback =
        meta.field === "year"
          ? new Date().getFullYear() - 10
          : meta.field === "rating"
            ? 7
            : meta.field === "runtime"
              ? 100
              : 0;
      if (op === "between") {
        const lo = first ?? fallback;
        const hi =
          typeof list[1] === "number"
            ? list[1]
            : meta.field === "year"
              ? new Date().getFullYear()
              : (meta.max ?? lo);
        return [lo, hi];
      }
      return first ?? fallback;
    }
    case "date": {
      if (op === "within")
        return typeof prev === "string" && /^\d+[dwmy]$/.test(prev) ? prev : "30d";
      const first =
        typeof list[0] === "string" && /^\d{4}-\d{2}-\d{2}/.test(list[0]) ? list[0] : today();
      if (op === "between")
        return [first, typeof list[1] === "string" && /^\d{4}/.test(list[1]) ? list[1] : today()];
      return first;
    }
  }
}

export function newRule(field: RuleField): Rule {
  const meta = FIELD_BY_NAME[field];
  return { field, op: meta.defaultOp, value: valueFor(meta, meta.defaultOp) };
}

export function withOp(rule: Rule, op: RuleOp): Rule {
  const meta = fieldMeta(rule.field);
  if (!meta) return { ...rule, op };
  return { ...rule, op, value: valueFor(meta, op, rule.value) };
}

export function withField(rule: Rule, field: RuleField): Rule {
  if (field === rule.field) return rule;
  return newRule(field);
}

/** Incomplete rules (no values picked yet) are left out of previews. */
export function isRuleComplete(rule: Rule): boolean {
  const meta = fieldMeta(rule.field);
  if (!meta) return true;
  const v = rule.value;
  if (meta.kind === "bool") return typeof v === "boolean";
  if (Array.isArray(v)) {
    if (rule.op === "between")
      return v.length === 2 && v.every((x) => x !== "" && x !== null && x !== undefined);
    return v.length > 0;
  }
  return v !== "" && v !== null && v !== undefined && !(typeof v === "number" && Number.isNaN(v));
}

function listText(rule: Rule, values: unknown[]): string {
  const labels = rule.label ? rule.label.split(", ").filter(Boolean) : [];
  const shown = labels.length === values.length ? labels : values.map(String);
  if (shown.length <= 3) return shown.join(", ");
  return `${shown.slice(0, 2).join(", ")} and ${shown.length - 2} more`;
}

/** One-line human description, e.g. "Genre is any of Horror, Thriller". */
export function describeRule(rule: Rule): string {
  const meta = fieldMeta(rule.field);
  const label = meta?.label ?? rule.field;
  const v = rule.value;
  if (meta?.kind === "bool")
    return `${v === false ? "Not " + label.charAt(0).toLowerCase() + label.slice(1) : label}`;
  const op = opLabel(rule.op, meta?.kind);
  if (rule.op === "within") return `${label} in the last ${formatWithin(v)}`;
  if (rule.op === "between" && Array.isArray(v))
    return `${label} between ${String(v[0])} and ${String(v[1])}${meta?.unit && meta.unit !== "/ 10" ? ` ${meta.unit}` : ""}`;
  if (Array.isArray(v)) return `${label} ${op} ${listText(rule, v) || "…"}`;
  const text = rule.label || (v === "" || v === undefined || v === null ? "…" : String(v));
  return `${label} ${op} ${text}${meta?.unit && meta.unit !== "/ 10" && typeof v === "number" ? ` ${meta.unit}` : ""}`;
}

// ---- Sources -------------------------------------------------------------------

export interface SourceMeta {
  kind: SourceKind;
  label: string;
  description: string;
  lookup?: LookupKind;
}

export const SOURCE_KINDS: SourceMeta[] = [
  { kind: "catalog", label: "Whole catalogue", description: "Everything browsable in Ether" },
  {
    kind: "tmdbList",
    label: "TMDB chart",
    description: "Trending, popular, top rated, now playing…",
  },
  {
    kind: "titles",
    label: "Hand-picked titles",
    description: "Choose titles one by one, in your order",
  },
  {
    kind: "tmdbPerson",
    label: "Person's filmography",
    description: "Everything an actor or director made",
    lookup: "person",
  },
  {
    kind: "tmdbCollection",
    label: "TMDB collection",
    description: "A franchise such as “The Lord of the Rings”",
    lookup: "collection",
  },
  {
    kind: "tmdbKeyword",
    label: "Keyword",
    description: "Titles tagged with a TMDB keyword",
    lookup: "keyword",
  },
  {
    kind: "tmdbCompany",
    label: "Studio",
    description: "Titles from a production company",
    lookup: "company",
  },
  {
    kind: "tmdbNetwork",
    label: "TV network",
    description: "Shows from a network such as HBO",
    lookup: "network",
  },
  {
    kind: "watchProvider",
    label: "Streaming service",
    description: "What's on a service in your region",
    lookup: "watchProvider",
  },
  {
    kind: "tmdbDiscover",
    label: "TMDB discover query",
    description: "Raw TMDB discover parameters",
  },
  { kind: "traktList", label: "Trakt list", description: "A public Trakt list URL" },
  { kind: "letterboxdList", label: "Letterboxd list", description: "A public Letterboxd list URL" },
  { kind: "mdblist", label: "MDBList", description: "A public MDBList URL" },
  {
    kind: "stremioCatalog",
    label: "Addon catalog",
    description: "A list from a Stremio addon such as My Lists Addon",
  },
];

export function sourceMeta(kind: string): SourceMeta | undefined {
  return SOURCE_KINDS.find((s) => s.kind === kind);
}

export const TMDB_LISTS: { value: TmdbListName; label: string; media: ("movie" | "show")[] }[] = [
  { value: "trending_day", label: "Trending today", media: ["movie", "show"] },
  { value: "trending_week", label: "Trending this week", media: ["movie", "show"] },
  { value: "popular", label: "Popular", media: ["movie", "show"] },
  { value: "top_rated", label: "Top rated", media: ["movie", "show"] },
  { value: "now_playing", label: "Now in cinemas", media: ["movie"] },
  { value: "upcoming", label: "Upcoming", media: ["movie"] },
  { value: "airing_today", label: "Airing today", media: ["show"] },
  { value: "on_the_air", label: "On the air", media: ["show"] },
];

export function newSource(
  kind: SourceKind,
  mediaType: CollectionSpec["mediaType"],
  region = "US",
): Source {
  switch (kind) {
    case "catalog":
      return { kind };
    case "tmdbList":
      return { kind, list: "trending_week", mediaType: mediaType === "show" ? "show" : "movie" };
    case "tmdbCollection":
      return { kind, id: 0 };
    case "tmdbDiscover":
      return { kind, params: {} };
    case "tmdbKeyword":
    case "tmdbCompany":
    case "tmdbNetwork":
    case "tmdbPerson":
      return { kind: kind as LookupSourceKind, id: 0 };
    case "watchProvider":
      return { kind, id: 0, region };
    case "traktList":
    case "letterboxdList":
    case "mdblist":
      return { kind, url: "" };
    case "stremioCatalog":
      return { kind, url: "", params: {} };
    case "titles":
      return { kind, keys: [] };
  }
}

export function isSourceComplete(s: Source): boolean {
  switch (s.kind) {
    case "tmdbCollection":
    case "tmdbKeyword":
    case "tmdbCompany":
    case "tmdbNetwork":
    case "tmdbPerson":
    case "watchProvider":
      return s.id > 0;
    case "traktList":
    case "letterboxdList":
    case "mdblist":
      return /^https?:\/\/\S+$/.test(s.url.trim());
    case "stremioCatalog":
      return /^(https?|stremio):\/\/\S+$/.test(s.url.trim());
    case "titles":
      return s.keys.length > 0;
    case "tmdbDiscover":
      return Object.keys(s.params).length > 0;
    default:
      return true;
  }
}

export function describeSource(s: Source): string {
  switch (s.kind) {
    case "catalog":
      return "Whole catalogue";
    case "tmdbList":
      return `${TMDB_LISTS.find((l) => l.value === s.list)?.label ?? s.list} (${s.mediaType === "show" ? "TV" : "movies"})`;
    case "titles":
      return `${s.keys.length} hand-picked ${s.keys.length === 1 ? "title" : "titles"}`;
    case "tmdbDiscover":
      return `Discover: ${
        Object.entries(s.params)
          .map(([k, v]) => `${k}=${v}`)
          .join("&") || "no parameters"
      }`;
    case "traktList":
    case "letterboxdList":
    case "mdblist":
      return `${sourceMeta(s.kind)?.label}: ${s.url || "no URL"}`;
    case "stremioCatalog":
      return `Addon catalog: ${s.params?.catalog || "no catalog chosen"}`;
    case "watchProvider":
      return `${s.label || "Streaming service"} (${s.region})`;
    default:
      return `${sourceMeta(s.kind)?.label}: ${s.label || (s.id ? `#${s.id}` : "not chosen")}`;
  }
}

// ---- Sort, schedule ---------------------------------------------------------------

export const SORTS: { value: CollectionSort; label: string; hint: string }[] = [
  { value: "popularity", label: "Popularity", hint: "Most popular on TMDB right now" },
  { value: "rating", label: "Rating", hint: "TMDB vote average" },
  { value: "released", label: "Release date", hint: "Newest or oldest first" },
  { value: "added", label: "Date added", hint: "When titles entered Ether" },
  { value: "title", label: "Title", hint: "Alphabetical" },
  { value: "personal", label: "Personal fit", hint: "Ordered for each viewer by their taste" },
  { value: "random", label: "Shuffle", hint: "A fresh order on every refresh" },
  { value: "source", label: "Source order", hint: "Keep the order of the list or your picks" },
];

export const REFRESH_OPTIONS: { value: CollectionSpec["refresh"]; label: string; hint: string }[] =
  [
    { value: "live", label: "Live", hint: "Evaluated whenever Plex asks" },
    { value: "hourly", label: "Hourly", hint: "Rebuilt every hour" },
    { value: "daily", label: "Daily", hint: "Rebuilt overnight" },
    { value: "weekly", label: "Weekly", hint: "Rebuilt once a week" },
  ];

export function blankSpec(mediaType: CollectionSpec["mediaType"] = "movie"): CollectionSpec {
  return {
    name: "",
    description: "",
    mediaType,
    sources: [{ kind: "catalog" }],
    match: "all",
    rules: [],
    exclude: [],
    sort: "popularity",
    sortDesc: true,
    limit: 50,
    refresh: "daily",
    rotate: null,
    artwork: { mode: "auto" },
    targets: {
      collectionsTab: true,
      homeRow: false,
      sectionRow: false,
      users: "all",
      schedule: null,
    },
    enabled: false,
  };
}

/** Fill in anything a server or template omitted so the editor never meets undefined. */
export function normalizeSpec(spec: Partial<CollectionSpec>): CollectionSpec {
  const base = blankSpec(spec.mediaType ?? "movie");
  return {
    ...base,
    ...spec,
    sources: Array.isArray(spec.sources) && spec.sources.length ? spec.sources : base.sources,
    rules: Array.isArray(spec.rules) ? spec.rules : [],
    exclude: Array.isArray(spec.exclude) ? spec.exclude : [],
    artwork: { ...base.artwork, ...(spec.artwork ?? {}) },
    targets: { ...base.targets, ...(spec.targets ?? {}) },
  };
}

/** The spec sent to /collections/preview: incomplete rules and sources are dropped. */
export function previewableSpec(spec: CollectionSpec): CollectionSpec {
  const sources = spec.sources.filter(isSourceComplete);
  return {
    ...spec,
    sources: sources.length ? sources : [{ kind: "catalog" }],
    rules: spec.rules.filter(isRuleComplete),
    exclude: spec.exclude.filter(isRuleComplete),
  };
}

/** Problems that block saving. */
export function specProblems(spec: CollectionSpec): string[] {
  const out: string[] = [];
  if (!spec.name.trim()) out.push("Give the collection a name.");
  if (spec.sources.length === 0) out.push("Add at least one source.");
  const badSources = spec.sources.filter((s) => !isSourceComplete(s));
  if (badSources.length)
    out.push(
      `Finish or remove ${badSources.length === 1 ? "the incomplete source" : `${badSources.length} incomplete sources`}.`,
    );
  const badRules = [...spec.rules, ...spec.exclude].filter((r) => !isRuleComplete(r));
  if (badRules.length)
    out.push(
      `Finish or remove ${badRules.length === 1 ? "the incomplete rule" : `${badRules.length} incomplete rules`}.`,
    );
  if (spec.artwork.mode === "custom" && !spec.artwork.url?.trim())
    out.push("Add an image URL for custom artwork.");
  if (spec.rotate && (spec.rotate.size < 1 || !Number.isFinite(spec.rotate.size)))
    out.push("Rotation needs a subset size of at least 1.");
  return out;
}

/** Where the collection appears, in words. */
export function describeTargets(spec: CollectionSpec): string[] {
  const out: string[] = [];
  if (spec.targets.collectionsTab) out.push("Collections tab");
  if (spec.targets.homeRow) out.push("Home row");
  if (spec.targets.sectionRow) out.push("Library row");
  return out;
}
