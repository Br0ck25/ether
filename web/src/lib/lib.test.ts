import { describe, expect, it } from "vitest";
import type { HomeRow, LayoutRule, Rule } from "./contract";
import { formatBytes, formatNanos, relativeTime } from "./format";
import { cacheHealth, catalogHealth, computeHealth, indexersHealth, normalizeSessions, plexDirectHealth, remoteHealth, usenetHealth, worstLevel } from "./health";
import { COALESCE_MS, historyReducer, initHistory } from "./history";
import { atFor, previewLayout, rowActionFor, setRowAction, whenMatches } from "./layout";
import {
  blankSpec, describeRule, isRuleComplete, newRule, normalizeSpec, previewableSpec, specProblems, valueFor, withOp, FIELD_BY_NAME,
} from "./rules";

describe("rules", () => {
  it("creates field-appropriate defaults", () => {
    expect(newRule("genre")).toEqual({ field: "genre", op: "in", value: [] });
    expect(newRule("rating")).toEqual({ field: "rating", op: "gte", value: 7 });
    expect(newRule("released")).toEqual({ field: "released", op: "within", value: "30d" });
    expect(newRule("watched")).toEqual({ field: "watched", op: "is", value: true });
  });

  it("converts values when the operator changes", () => {
    const year: Rule = { field: "year", op: "gte", value: 1990 };
    const between = withOp(year, "between");
    expect(between.value).toEqual([1990, new Date().getFullYear()]);
    expect(withOp(between, "lte").value).toBe(1990);
    const genre: Rule = { field: "genre", op: "in", value: ["Horror", "Thriller"] };
    expect(withOp(genre, "notIn").value).toEqual(["Horror", "Thriller"]);
    expect(valueFor(FIELD_BY_NAME.released, "between", "2020-01-01")).toEqual(["2020-01-01", expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/)]);
  });

  it("knows when a rule is complete", () => {
    expect(isRuleComplete({ field: "genre", op: "in", value: [] })).toBe(false);
    expect(isRuleComplete({ field: "genre", op: "in", value: ["Drama"] })).toBe(true);
    expect(isRuleComplete({ field: "rating", op: "between", value: [6, ""] })).toBe(false);
    expect(isRuleComplete({ field: "person", op: "is", value: "" })).toBe(false);
    expect(isRuleComplete({ field: "watched", op: "is", value: false })).toBe(true);
  });

  it("describes rules in plain language using labels for ids", () => {
    expect(describeRule({ field: "genre", op: "in", value: ["Horror", "Thriller"] })).toBe("Genre is any of Horror, Thriller");
    expect(describeRule({ field: "person", op: "in", value: [1, 2, 3, 4], label: "A, B, C, D" })).toBe("Person is any of A, B and 2 more");
    expect(describeRule({ field: "runtime", op: "lte", value: 90 })).toBe("Runtime is at most 90 min");
    expect(describeRule({ field: "released", op: "within", value: "6m" })).toBe("Released in the last 6 months");
    expect(describeRule({ field: "watched", op: "is", value: false })).toBe("Not watched by the viewer");
  });

  it("previews only the complete parts of a spec", () => {
    const spec = { ...blankSpec(), sources: [{ kind: "tmdbPerson" as const, id: 0 }], rules: [newRule("genre"), newRule("rating")] };
    const p = previewableSpec(spec);
    expect(p.sources).toEqual([{ kind: "catalog" }]);
    expect(p.rules).toEqual([{ field: "rating", op: "gte", value: 7 }]);
  });

  it("lists problems that block saving", () => {
    expect(specProblems(blankSpec())).toEqual(["Give the collection a name."]);
    const bad = { ...blankSpec(), name: "X", rules: [newRule("genre")], artwork: { mode: "custom" as const } };
    expect(specProblems(bad)).toEqual(["Finish or remove the incomplete rule.", "Add an image URL for custom artwork."]);
  });

  it("normalizes partial specs from the server", () => {
    const s = normalizeSpec({ name: "N", targets: { homeRow: true } as never });
    expect(s.targets).toMatchObject({ homeRow: true, collectionsTab: true, users: "all" });
    expect(s.rules).toEqual([]);
    expect(s.sources).toEqual([{ kind: "catalog" }]);
  });
});

describe("undo history", () => {
  it("records steps, coalesces rapid edits to the same field, and undoes/redoes", () => {
    let h = initHistory("a");
    h = historyReducer(h, { type: "set", value: "ab", key: "name", at: 1000 });
    h = historyReducer(h, { type: "set", value: "abc", key: "name", at: 1000 + COALESCE_MS / 2 });
    expect(h.past).toEqual(["a"]);
    h = historyReducer(h, { type: "set", value: "abcd", key: "name", at: 1000 + COALESCE_MS * 3 });
    expect(h.past).toEqual(["a", "abc"]);
    h = historyReducer(h, { type: "undo" });
    expect(h.present).toBe("abc");
    h = historyReducer(h, { type: "redo" });
    expect(h.present).toBe("abcd");
    h = historyReducer(h, { type: "undo" });
    h = historyReducer(h, { type: "set", value: "x", at: 99999 });
    expect(h.future).toEqual([]);
  });
  it("ignores undo with nothing to undo and identical sets", () => {
    const h = initHistory(1);
    expect(historyReducer(h, { type: "undo" })).toBe(h);
    expect(historyReducer(h, { type: "set", value: 1, at: 0 })).toBe(h);
  });
});

describe("layout", () => {
  const row = (hubId: string, score = 0.5): HomeRow => ({ hubId, title: hubId, kind: hubId, reason: "", score, source: "engine", items: [] });
  const ctx = { userId: 2, section: "home" as const, device: "tv", day: 6, hour: 20 };

  it("finds the next moment for a weekday and hour", () => {
    const wed = new Date(2026, 9, 21, 10, 0, 0); // Wednesday 10:00
    expect(atFor(5, 20, wed)).toEqual(new Date(2026, 9, 23, 20, 0, 0));
    expect(atFor(3, 22, wed)).toEqual(new Date(2026, 9, 21, 22, 0, 0));
    expect(atFor(3, 7, wed)).toEqual(new Date(2026, 9, 28, 7, 0, 0));
  });

  it("toggles row actions within the right scope and drops empty rules", () => {
    let rules: LayoutRule[] = [];
    rules = setRowAction(rules, "all", "home", "a", "hide");
    expect(rules).toEqual([{ scope: "all", section: "home", rows: [{ hubId: "a", action: "hide" }] }]);
    rules = setRowAction(rules, 2, "home", "b", "pin", 1);
    expect(rules[1]).toEqual({ scope: { userIds: [2] }, section: "home", rows: [{ hubId: "b", action: "pin", position: 1 }] });
    expect(rowActionFor(rules, 2, "home", "b")?.action).toBe("pin");
    expect(rowActionFor(rules, "all", "home", "b")).toBeUndefined();
    rules = setRowAction(rules, "all", "home", "a", null);
    expect(rules).toHaveLength(1);
  });

  it("matches schedules including windows that wrap midnight", () => {
    expect(whenMatches({ days: [1, 2] }, { day: 6, hour: 20, device: "tv" })).toBe(false);
    expect(whenMatches({ fromHour: 22, toHour: 2 }, { day: 6, hour: 1, device: "tv" })).toBe(true);
    expect(whenMatches({ fromHour: 22, toHour: 2 }, { day: 6, hour: 12, device: "tv" })).toBe(false);
    expect(whenMatches({ devices: ["mobile"] }, { day: 6, hour: 12, device: "tv" })).toBe(false);
  });

  it("previews pins, hides, boosts, buries and row caps; viewer rules win", () => {
    const rows = ["a", "b", "c", "d", "e"].map((id) => row(id));
    const rules: LayoutRule[] = [
      { scope: "all", section: "home", rows: [{ hubId: "a", action: "hide" }, { hubId: "e", action: "pin", position: 1 }, { hubId: "c", action: "bury" }], maxRows: 3 },
      { scope: { userIds: [2] }, section: "home", rows: [{ hubId: "a", action: "boost" }, { hubId: "d", action: "hide", when: { days: [1] } }] },
      { scope: { userIds: [9] }, section: "home", rows: [{ hubId: "b", action: "hide" }] },
    ];
    const out = previewLayout(rows, rules, ctx);
    expect(out.map((r) => r.row.hubId)).toEqual(["a", "e", "b", "d", "c"]);
    expect(out[0]).toMatchObject({ action: "boost", actionScope: "viewer", hidden: false });
    expect(out.filter((r) => r.overLimit).map((r) => r.row.hubId)).toEqual(["d", "c"]);
  });
});

const LIVE = {
  cache: { budget: 21474836480, used: 970344835 },
  catalog: { movies: 24929, shows: 24298, browsableMovies: 24928, browsableShows: 24298, lastSync: "2026-10-19T13:16:36Z", sync: { running: false } },
  indexers: [
    { indexer: "NZBgeek", hitsUsed: 6, hitsLimit: 200, hitsRemaining: 194, grabsUsed: 10, grabsLimit: 50, grabsRemaining: 40 },
    { indexer: "DOGnzb", hitsUsed: 20, hitsLimit: 20, hitsRemaining: 0, grabsUsed: 0, grabsLimit: 10, grabsRemaining: 10 },
  ],
  providers: [
    { id: "p1", name: "Giganews", backfill: false, state: "ok", configuredConnections: 16, activeConnections: 4 },
    { id: "p2", name: "Frugal", backfill: false, state: "backoff", configuredConnections: 16, activeConnections: 0 },
  ],
  remote: { claimed: true, ownerUsername: "o", remoteAccess: false, listed: true, presence: { connected: true }, cert: { present: true, subject: "*.x.plex.direct", notAfter: "2027-01-17T04:36:33Z" } },
};

describe("health", () => {
  const now = new Date("2026-10-19T14:00:00Z");
  it("summarises the live dashboard shapes", () => {
    const h = computeHealth(LIVE, now);
    expect(h.map((x) => [x.id, x.level])).toEqual([
      ["usenet", "warn"], ["indexers", "warn"], ["cache", "ok"], ["catalog", "ok"], ["remote", "off"], ["plexdirect", "ok"],
    ]);
    expect(usenetHealth(LIVE.providers).fill).toBeCloseTo(4 / 32);
    expect(indexersHealth(LIVE.indexers).summary).toBe("1 of 2 indexers out of searches today");
    expect(cacheHealth(LIVE.cache).summary).toBe("925.4 MiB of 20.0 GiB");
    expect(worstLevel(h.map((x) => x.level))).toBe("warn");
  });
  it("flags real problems", () => {
    expect(usenetHealth([]).level).toBe("error");
    expect(usenetHealth({ error: "boom" }).summary).toBe("boom");
    expect(catalogHealth({ lastSync: "2026-10-01T00:00:00Z" }, now).level).toBe("warn");
    expect(remoteHealth({ claimed: false }).level).toBe("error");
    expect(remoteHealth({ claimed: true, remoteAccess: true, presence: { connected: false } }).level).toBe("warn");
    expect(plexDirectHealth({ cert: { present: true, notAfter: "2026-10-25T00:00:00Z" } }, now).level).toBe("warn");
    expect(plexDirectHealth({ cert: { present: false } }, now).level).toBe("warn");
    expect(computeHealth(undefined, now).every((x) => x.level === "unknown" || x.level === "warn")).toBe(true);
  });
  it("normalizes now-playing sessions from Go field names", () => {
    const [s] = normalizeSessions([{ ID: "s1", Key: 123, TitleName: "Dune (2021)", Username: "Sam", Client: { product: "Plex Web", deviceName: "Chrome", remote: true }, Decision: "transcode", BytesServed: 50, Info: { size: 100, duration: 7.2e12 }, viewOffset: 3.6e6, Resolution: { release: { name: "Dune.2021.1080p" } } }]);
    expect(s).toMatchObject({ id: "s1", key: "123", title: "Dune (2021)", user: "Sam", device: "Chrome", decision: "transcode", remote: true, size: 100, release: "Dune.2021.1080p" });
    expect(s!.progress).toBeCloseTo(0.5);
    expect(normalizeSessions(null)).toEqual([]);
  });
});

describe("format", () => {
  it("formats bytes, durations and relative times", () => {
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(-1)).toBe("—");
    expect(formatNanos(1.8e9)).toBe("1.8 s");
    expect(formatNanos(3.1e8)).toBe("310 ms");
    expect(relativeTime("0001-01-01T00:00:00Z")).toBe("never");
    expect(relativeTime(new Date(Date.now() - 3 * 3600_000))).toMatch(/3 hours ago/);
  });
});
