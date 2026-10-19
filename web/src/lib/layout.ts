import type { HomeRow, LayoutAction, LayoutRule, LayoutRowRule, LayoutSection, LayoutWhen } from "./contract";

/**
 * Home Designer helpers. Position semantics: `position` on a pin is the
 * 0-based index the row should occupy in the served list; a pin without a
 * position goes to the top (pins keep their relative order).
 */

export type LayoutScope = "all" | number; // a viewer id

export interface DesignerContext {
  userId: number;
  section: LayoutSection;
  device: string;
  day: number; // 0 = Sunday
  hour: number; // 0..23
}

/** The next moment (from `now`, local time) that falls on `day` at `hour`:00. */
export function atFor(day: number, hour: number, now: Date = new Date()): Date {
  const d = new Date(now);
  d.setHours(hour, 0, 0, 0);
  let delta = (day - now.getDay() + 7) % 7;
  if (delta === 0 && d.getTime() < now.getTime() - 3600_000) delta = 7;
  d.setDate(d.getDate() + delta);
  return d;
}

export function scopeOf(rule: LayoutRule): LayoutScope | "group" {
  if (rule.scope === "all") return "all";
  const ids = rule.scope.userIds ?? [];
  return ids.length === 1 ? (ids[0] as number) : "group";
}

function sameSection(rule: LayoutRule, section: LayoutSection): boolean {
  return (rule.section ?? "home") === section;
}

export function ruleIndexFor(rules: LayoutRule[], scope: LayoutScope, section: LayoutSection): number {
  return rules.findIndex((r) => sameSection(r, section) && scopeOf(r) === scope);
}

/** The unconditional action (no `when`) for a hub in the scope's rule. */
export function rowActionFor(rules: LayoutRule[], scope: LayoutScope, section: LayoutSection, hubId: string): LayoutRowRule | undefined {
  const i = ruleIndexFor(rules, scope, section);
  if (i < 0) return undefined;
  return rules[i]?.rows.find((r) => r.hubId === hubId && !hasWhen(r.when));
}

export function hasWhen(w: LayoutWhen | undefined): boolean {
  return Boolean(w && ((w.days?.length ?? 0) > 0 || w.fromHour !== undefined || w.toHour !== undefined || (w.devices?.length ?? 0) > 0));
}

/**
 * Set (or clear, with action null) the unconditional action for a hub within
 * the rule for scope+section, creating the rule when needed and dropping it
 * when it becomes empty.
 */
export function setRowAction(
  rules: LayoutRule[], scope: LayoutScope, section: LayoutSection, hubId: string,
  action: LayoutAction | null, position?: number,
): LayoutRule[] {
  const next = rules.map((r) => ({ ...r, rows: [...r.rows] }));
  let i = ruleIndexFor(next, scope, section);
  if (i < 0) {
    if (action === null) return rules;
    next.push({ scope: scope === "all" ? "all" : { userIds: [scope] }, section, rows: [] });
    i = next.length - 1;
  }
  const rule = next[i] as LayoutRule;
  rule.rows = rule.rows.filter((r) => !(r.hubId === hubId && !hasWhen(r.when)));
  if (action !== null) {
    const entry: LayoutRowRule = { hubId, action };
    if (action === "pin" && position !== undefined) entry.position = position;
    rule.rows.push(entry);
  }
  if (rule.rows.length === 0 && rule.maxRows === undefined && !rule.id) next.splice(i, 1);
  return next;
}

export function whenMatches(w: LayoutWhen | undefined, ctx: Pick<DesignerContext, "day" | "hour" | "device">): boolean {
  if (!w) return true;
  if (w.days?.length && !w.days.includes(ctx.day)) return false;
  if (w.devices?.length && !w.devices.includes(ctx.device)) return false;
  const from = w.fromHour;
  const to = w.toHour;
  if (from !== undefined || to !== undefined) {
    const f = from ?? 0;
    const t = to ?? 24;
    const inside = f <= t ? ctx.hour >= f && ctx.hour < t : ctx.hour >= f || ctx.hour < t;
    if (!inside) return false;
  }
  return true;
}

export function ruleApplies(rule: LayoutRule, ctx: DesignerContext): boolean {
  if (!sameSection(rule, ctx.section)) return false;
  if (rule.scope === "all") return true;
  return (rule.scope.userIds ?? []).includes(ctx.userId);
}

export interface PreviewRow {
  row: HomeRow;
  action?: LayoutAction;
  /** Rule scope that produced the action. */
  actionScope?: "all" | "viewer";
  hidden: boolean;
  overLimit: boolean;
}

/**
 * Approximate how a draft layout reshapes the rows the server returned, so
 * the owner sees the effect of unsaved edits. The server remains the source
 * of truth once saved: rows it already hid or pinned keep those flags.
 */
export function previewLayout(rows: HomeRow[], rules: LayoutRule[], ctx: DesignerContext): PreviewRow[] {
  const applicable = rules.filter((r) => ruleApplies(r, ctx));
  // Server-wide first, then per-viewer, so viewer rules override.
  applicable.sort((a, b) => (a.scope === "all" ? 0 : 1) - (b.scope === "all" ? 0 : 1));
  const actions = new Map<string, { rule: LayoutRowRule; scope: "all" | "viewer" }>();
  let maxRows: number | undefined;
  for (const rule of applicable) {
    if (rule.maxRows !== undefined && rule.maxRows > 0) maxRows = rule.maxRows;
    for (const rr of rule.rows) {
      if (whenMatches(rr.when, ctx)) actions.set(rr.hubId, { rule: rr, scope: rule.scope === "all" ? "all" : "viewer" });
    }
  }
  const annotated: PreviewRow[] = rows.map((row) => {
    const a = actions.get(row.hubId);
    return {
      row,
      action: a?.rule.action,
      actionScope: a?.scope,
      hidden: a ? a.rule.action === "hide" : Boolean(row.hidden),
      overLimit: false,
    };
  });
  const visible = annotated.filter((r) => !r.hidden);
  const hidden = annotated.filter((r) => r.hidden);
  const buried = visible.filter((r) => r.action === "bury");
  const boosted = visible.filter((r) => r.action === "boost");
  const pinned = visible.filter((r) => r.action === "pin");
  const rest = visible.filter((r) => !r.action || r.action === "hide");
  // Boosted rows move ahead of unmodified rows; buried rows go last.
  let ordered = [...boosted, ...rest, ...buried];
  const positioned = pinned
    .map((r) => ({ r, pos: actions.get(r.row.hubId)?.rule.position }))
    .sort((a, b) => (a.pos ?? -1) - (b.pos ?? -1));
  const unpositioned = positioned.filter((p) => p.pos === undefined).map((p) => p.r);
  ordered = [...unpositioned, ...ordered];
  for (const p of positioned) {
    if (p.pos === undefined) continue;
    ordered.splice(Math.max(0, Math.min(ordered.length, p.pos)), 0, p.r);
  }
  if (maxRows !== undefined) ordered.forEach((r, i) => { r.overLimit = i >= maxRows!; });
  return [...ordered, ...hidden];
}

export const ACTION_LABELS: Record<LayoutAction, string> = {
  pin: "Pinned", hide: "Hidden", boost: "Boosted", bury: "Buried",
};

export function describeWhen(w: LayoutWhen | undefined, dayNames: readonly string[]): string {
  if (!hasWhen(w) || !w) return "Always";
  const parts: string[] = [];
  if (w.days?.length) {
    const d = [...w.days].sort();
    const weeknights = [1, 2, 3, 4, 5];
    const weekend = [0, 6];
    if (d.length === 5 && weeknights.every((x) => d.includes(x))) parts.push("Weekdays");
    else if (d.length === 2 && weekend.every((x) => d.includes(x))) parts.push("Weekends");
    else parts.push(d.map((x) => dayNames[x]).join(", "));
  }
  if (w.fromHour !== undefined || w.toHour !== undefined) {
    const fmt = (h: number) => `${String(h).padStart(2, "0")}:00`;
    parts.push(`${fmt(w.fromHour ?? 0)}–${fmt(w.toHour ?? 24)}`);
  }
  if (w.devices?.length) parts.push(w.devices.join(", "));
  return parts.join(" · ");
}
