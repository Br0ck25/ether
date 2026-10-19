import { Badge, Button, EmptyState, MultiSelect, SearchSelect, cn } from "@braedonsaunders/appkit-ui";
import { CalendarClock, ListOrdered, Plus, Trash2, X } from "lucide-react";
import { useState } from "react";
import { ChipGroup, NumberInput, Segmented } from "../../components/fields";
import { viewerName } from "../../components/pickers";
import type { AvailableRow, LayoutAction, LayoutRowRule, LayoutRule, LayoutSection, LayoutWhen, Viewer } from "../../lib/contract";
import { WEEKDAYS, formatHour } from "../../lib/format";
import { describeWhen, hasWhen } from "../../lib/layout";

const ACTION_OPTIONS: { value: LayoutAction; label: string }[] = [
  { value: "pin", label: "Pin" }, { value: "boost", label: "Boost" }, { value: "bury", label: "Bury" }, { value: "hide", label: "Hide" },
];
const SECTION_LABEL: Record<LayoutSection, string> = { home: "Home", movies: "Movies", shows: "TV" };
const DEVICES = [{ value: "tv", label: "TV" }, { value: "mobile", label: "Phone" }, { value: "web", label: "Web" }, { value: "desktop", label: "Desktop" }];

export function scopeLabel(rule: LayoutRule, viewers: Viewer[]): string {
  if (rule.scope === "all") return "Everyone";
  const names = (rule.scope.userIds ?? []).map((id) => viewers.find((v) => v.id === id)).map((v, i) => (v ? viewerName(v) : `Viewer ${(rule.scope as { userIds: number[] }).userIds[i]}`));
  return names.join(", ") || "No viewers";
}

/** Full editor for layout rules: scope, section, row actions with schedules, row cap. */
export function RulesPanel({ rules, onChange, available, viewers }: {
  rules: LayoutRule[]; onChange: (r: LayoutRule[]) => void; available: AvailableRow[]; viewers: Viewer[];
}) {
  const setRule = (i: number, r: LayoutRule) => onChange(rules.map((x, j) => (j === i ? r : x)));
  const addRule = () => onChange([...rules, { scope: "all", section: "home", rows: [] }]);
  return (
    <div className="space-y-4">
      {rules.length === 0 ? (
        <EmptyState icon={<ListOrdered />} title="No layout rules" description="Use the row buttons on the left, or add a rule here, to pin, hide or reorder rows." className="py-8" />
      ) : rules.map((r, i) => (
        <RuleCard key={r.id ?? `new-${i}`} rule={r} index={i} available={available} viewers={viewers}
          onChange={(next) => setRule(i, next)} onRemove={() => onChange(rules.filter((_, j) => j !== i))} />
      ))}
      <Button variant="outline" size="sm" onClick={addRule}><Plus size={14} /> Add rule</Button>
    </div>
  );
}

function RuleCard({ rule, index, available, viewers, onChange, onRemove }: {
  rule: LayoutRule; index: number; available: AvailableRow[]; viewers: Viewer[]; onChange: (r: LayoutRule) => void; onRemove: () => void;
}) {
  const [adding, setAdding] = useState("");
  const scopeMode = rule.scope === "all" ? "all" : "some";
  const used = new Set(rule.rows.map((r) => r.hubId));
  const rowTitle = (hubId: string) => available.find((a) => a.hubId === hubId)?.title ?? hubId;
  const label = `Layout rule ${index + 1}`;
  return (
    <section className="rounded-xl border border-border bg-surface p-3" aria-label={label}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-sm font-semibold text-fg">{scopeLabel(rule, viewers)} · {SECTION_LABEL[rule.section ?? "home"]}</p>
          <p className="text-xs text-fg-muted">{rule.rows.length} row {rule.rows.length === 1 ? "change" : "changes"}{rule.maxRows ? ` · at most ${rule.maxRows} rows` : ""}</p>
        </div>
        <Button variant="ghost" size="icon" className="size-8 text-fg-muted hover:text-danger" onClick={onRemove} aria-label={`Remove ${label.toLowerCase()}`}><Trash2 size={15} /></Button>
      </div>

      <div className="mt-3 grid gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Segmented ariaLabel={`${label} applies to`} size="sm" value={scopeMode} onChange={(m) => onChange({ ...rule, scope: m === "all" ? "all" : { userIds: [] } })}
            options={[{ value: "all", label: "Everyone" }, { value: "some", label: "Specific viewers" }]} />
          <Segmented ariaLabel={`${label} section`} size="sm" value={rule.section ?? "home"} onChange={(s) => onChange({ ...rule, section: s })}
            options={(["home", "movies", "shows"] as const).map((s) => ({ value: s, label: SECTION_LABEL[s] }))} />
        </div>
        {rule.scope !== "all" ? (
          <MultiSelect ariaLabel={`${label} viewers`} value={rule.scope.userIds.map(String)} onChange={(v) => onChange({ ...rule, scope: { userIds: v.map(Number) } })}
            options={viewers.map((v) => ({ value: String(v.id), label: viewerName(v) }))} placeholder="Choose viewers…" />
        ) : null}
        <label className="flex items-center gap-2 text-xs text-fg-muted">
          Show at most
          <NumberInput aria-label={`${label} maximum rows`} value={rule.maxRows ?? 0} min={0} className="h-8 w-20"
            onChange={(n) => onChange({ ...rule, maxRows: n > 0 ? Math.round(n) : undefined })} />
          rows (0 = no limit)
        </label>
      </div>

      <ul className="mt-3 space-y-2">
        {rule.rows.map((rr, j) => (
          <RowRuleEditor key={`${rr.hubId}-${j}`} rr={rr} title={rowTitle(rr.hubId)} label={`${label}, ${rowTitle(rr.hubId)}`}
            onChange={(next) => onChange({ ...rule, rows: rule.rows.map((x, k) => (k === j ? next : x)) })}
            onRemove={() => onChange({ ...rule, rows: rule.rows.filter((_, k) => k !== j) })} />
        ))}
      </ul>
      <div className="mt-2 flex gap-2">
        <div className="min-w-0 flex-1">
          <SearchSelect value={adding} onChange={setAdding} ariaLabel={`${label} add row`} placeholder="Add a row…"
            options={available.map((a) => ({ value: a.hubId, label: a.title, hint: used.has(a.hubId) ? "Already in this rule" : a.description }))} />
        </div>
        <Button variant="outline" size="sm" className="h-10" disabled={!adding} onClick={() => { onChange({ ...rule, rows: [...rule.rows, { hubId: adding, action: "pin" }] }); setAdding(""); }}>
          <Plus size={14} /> Add
        </Button>
      </div>
    </section>
  );
}

function RowRuleEditor({ rr, title, label, onChange, onRemove }: { rr: LayoutRowRule; title: string; label: string; onChange: (r: LayoutRowRule) => void; onRemove: () => void }) {
  const [open, setOpen] = useState(hasWhen(rr.when));
  const when = rr.when ?? {};
  const setWhen = (patch: Partial<LayoutWhen>) => {
    const next = { ...when, ...patch };
    onChange({ ...rr, when: hasWhen(next) ? next : undefined });
  };
  const hours = [{ value: "", label: "Any" }, ...Array.from({ length: 25 }, (_, h) => ({ value: String(h), label: h === 24 ? "Midnight" : formatHour(h) }))];
  return (
    <li className="rounded-lg border border-border-subtle bg-bg-subtle p-2.5">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg" title={rr.hubId}>{title}</span>
        <Button variant="ghost" size="icon" className="size-7" onClick={onRemove} aria-label={`Remove ${label}`}><X size={14} /></Button>
      </div>
      <div className="mt-1.5">
        <Segmented ariaLabel={`${label} action`} size="sm" value={rr.action} onChange={(a) => onChange({ ...rr, action: a, position: a === "pin" ? rr.position : undefined })} options={ACTION_OPTIONS} />
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        {rr.action === "pin" ? (
          <label className="flex items-center gap-1.5 text-xs text-fg-muted">Position
            <NumberInput aria-label={`${label} position`} value={(rr.position ?? -1) + 1} min={0} className="h-7 w-16 text-xs"
              onChange={(n) => onChange({ ...rr, position: n >= 1 ? Math.round(n) - 1 : undefined })} />
            <span className="text-fg-subtle">{rr.position === undefined ? "(top)" : ""}</span>
          </label>
        ) : null}
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open}
          className={cn("inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs", hasWhen(rr.when) ? "border-primary/40 bg-primary-subtle text-primary" : "border-border text-fg-muted hover:text-fg")}>
          <CalendarClock size={12} /> {describeWhen(rr.when, WEEKDAYS)}
        </button>
      </div>
      {open ? (
        <div className="mt-2 space-y-2 border-t border-border-subtle pt-2">
          <ChipGroup ariaLabel={`${label} days`} size="sm" value={when.days ?? []} onChange={(d) => setWhen({ days: d.length ? [...d].sort() : undefined })} options={WEEKDAYS.map((d, i) => ({ value: i, label: d }))} />
          <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
            From
            <div className="w-28"><SearchSelect value={when.fromHour === undefined ? "" : String(when.fromHour)} onChange={(v) => setWhen({ fromHour: v === "" ? undefined : Number(v) })} options={hours.slice(0, 25)} ariaLabel={`${label} from hour`} /></div>
            until
            <div className="w-28"><SearchSelect value={when.toHour === undefined ? "" : String(when.toHour)} onChange={(v) => setWhen({ toHour: v === "" ? undefined : Number(v) })} options={hours} ariaLabel={`${label} until hour`} /></div>
          </div>
          <ChipGroup ariaLabel={`${label} devices`} size="sm" value={when.devices ?? []} onChange={(d) => setWhen({ devices: d.length ? d : undefined })} options={DEVICES} />
          {hasWhen(rr.when) ? <Badge variant="secondary">{describeWhen(rr.when, WEEKDAYS)}</Badge> : null}
        </div>
      ) : null}
    </li>
  );
}
