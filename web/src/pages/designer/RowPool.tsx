import { Drawer } from "../../components/Drawer";
import { Badge, Button, Input, cn } from "@braedonsaunders/appkit-ui";
import { ChevronDown, Plus, RotateCcw, Search, SlidersHorizontal } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Segmented } from "../../components/fields";
import type {
  AvailableRow,
  HomeLayout,
  LayoutSection,
  RowPolicy,
  RowStatus,
  Viewer,
} from "../../lib/contract";
import { WEEKDAYS, plural } from "../../lib/format";
import {
  FRONT_END_ROWS,
  STATUS_HINT,
  STATUS_LABEL,
  pageOf,
  policyChips,
  policyIn,
  removePolicy,
  setPolicy,
  viewersOverriding,
  type LayerKey,
} from "../../lib/layout";
import { PolicyEditor } from "./PolicyEditor";

interface PoolEntry {
  row: string;
  title: string;
  description: string;
}

/**
 * The row catalogue: every row the engine, Plex or the owner's collections
 * can put on the page, with its status and constraints in this layer.
 */
export function RowPool({
  draft,
  onChange,
  layer,
  section,
  viewers,
  available,
  focusRow,
  onFocusRow,
}: {
  draft: HomeLayout;
  onChange: (d: HomeLayout) => void;
  layer: LayerKey;
  section: LayoutSection;
  viewers: Viewer[];
  available: AvailableRow[];
  focusRow: string | null;
  onFocusRow: (row: string | null) => void;
}) {
  const [q, setQ] = useState("");
  const [custom, setCustom] = useState("");
  const [patternOpen, setPatternOpen] = useState(false);
  const entries = useMemo<PoolEntry[]>(() => {
    const out: PoolEntry[] = available
      .filter((a) => !FRONT_END_ROWS.has(a.kind))
      .map((a) => ({ row: a.hubId, title: a.title, description: a.description }));
    // Policies for patterns or hub ids that are not in the catalogue.
    for (const layerKey of ["global", layer] as LayerKey[]) {
      for (const p of pageOf(draft, layerKey, section)?.rows ?? []) {
        if (!out.some((e) => e.row === p.row))
          out.push({
            row: p.row,
            title: p.row,
            description: "Custom row pattern (hub id, kind or glob).",
          });
      }
    }
    return out;
  }, [available, draft, layer, section]);
  const needle = q.trim().toLowerCase();
  const shown = needle
    ? entries.filter(
        (e) => e.title.toLowerCase().includes(needle) || e.row.toLowerCase().includes(needle),
      )
    : entries;
  return (
    <section className="space-y-2" aria-label="Row catalogue">
      <div>
        <h2 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">
          Row catalogue
        </h2>
        <p className="mt-0.5 text-xs text-fg-muted">
          {layer === "global"
            ? "Always: on the page whenever it has titles. Eligible: personalization decides. Off: never."
            : "Changes here override Global for this viewer only; reset to follow Global again."}
        </p>
      </div>
      <div className="relative">
        <Search
          size={14}
          className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
        />
        <Input
          value={q}
          onChange={(e) => setQ(e.currentTarget.value)}
          placeholder="Filter rows…"
          aria-label="Filter rows"
          className="h-8 pl-8"
        />
      </div>
      <ul className="space-y-1.5">
        {shown.map((e) => (
          <PoolItem
            key={e.row}
            entry={e}
            draft={draft}
            onChange={onChange}
            layer={layer}
            section={section}
            viewers={viewers}
            open={focusRow === e.row}
            onToggle={() => onFocusRow(focusRow === e.row ? null : e.row)}
          />
        ))}
      </ul>
      <Button
        variant="ghost"
        size="sm"
        className="h-8 text-xs text-fg-muted"
        onClick={() => setPatternOpen(true)}
      >
        <Plus size={14} /> Add custom pattern
      </Button>
      <Drawer
        open={patternOpen}
        onClose={() => {
          setPatternOpen(false);
          setCustom("");
        }}
        title="Custom row pattern"
        description="Advanced: target row IDs, kinds or glob patterns."
        size="md"
      >
        <form
          className="flex gap-2"
          onSubmit={(ev) => {
            ev.preventDefault();
            const row = custom.trim();
            if (!row) return;
            if (!policyIn(draft, layer, section, row))
              onChange(setPolicy(draft, layer, section, { row, status: "eligible" }));
            setCustom("");
            setPatternOpen(false);
            onFocusRow(row);
          }}
        >
          <Input
            value={custom}
            onChange={(e) => setCustom(e.currentTarget.value)}
            placeholder="Pattern, e.g. because.*"
            aria-label="Custom row pattern"
            className="h-8 flex-1 font-mono text-xs"
          />
          <Button
            type="submit"
            variant="outline"
            size="sm"
            className="h-8"
            disabled={!custom.trim()}
          >
            <Plus size={14} /> Add
          </Button>
        </form>
      </Drawer>
    </section>
  );
}

function PoolItem({
  entry,
  draft,
  onChange,
  layer,
  section,
  viewers,
  open,
  onToggle,
}: {
  entry: PoolEntry;
  draft: HomeLayout;
  onChange: (d: HomeLayout) => void;
  layer: LayerKey;
  section: LayoutSection;
  viewers: Viewer[];
  open: boolean;
  onToggle: () => void;
}) {
  const ref = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (open) ref.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [open]);
  const own = policyIn(draft, layer, section, entry.row);
  const global = layer === "global" ? undefined : policyIn(draft, "global", section, entry.row);
  const effective: RowPolicy = own ?? global ?? { row: entry.row, status: "eligible" };
  const source: "override" | "global" | "default" = own
    ? layer === "global"
      ? "global"
      : "override"
    : global
      ? "global"
      : "default";
  const edit = (p: RowPolicy) => onChange(setPolicy(draft, layer, section, p));
  const reset = () => onChange(removePolicy(draft, layer, section, entry.row));
  const overriding =
    layer === "global"
      ? viewersOverriding(draft, section, entry.row).filter((id) =>
          viewers.some((v) => v.id === id),
        )
      : [];
  const chips = policyChips(effective, WEEKDAYS);
  const statuses: RowStatus[] = ["always", "eligible", "off"];
  return (
    <li
      ref={ref}
      aria-label={`Row ${entry.title}`}
      className={cn(
        "rounded-lg border bg-surface p-2.5",
        source === "override" ? "border-info/40" : "border-border",
        open && "ring-2 ring-primary/30",
      )}
    >
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="truncate text-sm font-medium text-fg" title={entry.description}>
              {entry.title}
            </span>
            {source === "override" ? (
              <Badge variant="info">Override</Badge>
            ) : source === "global" && layer !== "global" ? (
              <Badge variant="secondary">From Global</Badge>
            ) : source === "default" ? (
              <span className="text-[11px] text-fg-subtle">Default</span>
            ) : null}
          </div>
          {chips.length ? (
            <div className="mt-1 flex flex-wrap gap-1">
              {chips.map((c) => (
                <Badge key={c} variant="outline" className="font-normal">
                  {c}
                </Badge>
              ))}
            </div>
          ) : null}
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="size-7 shrink-0"
          aria-expanded={open}
          aria-label={`Constraints for ${entry.title}`}
          onClick={onToggle}
        >
          {open ? <ChevronDown size={15} /> : <SlidersHorizontal size={14} />}
        </Button>
      </div>
      <p className="mt-2 text-xs text-fg-muted">{STATUS_LABEL[effective.status]}</p>
      {layer === "global" && own ? (
        <p className="mt-1.5 text-[11px] text-fg-muted">
          Affects {plural(viewers.length - overriding.length, "viewer")}
          {overriding.length
            ? ` · ${plural(overriding.length, "viewer")} ${overriding.length === 1 ? "overrides" : "override"} it`
            : ""}
        </p>
      ) : null}
      <Drawer
        open={open}
        onClose={onToggle}
        title={entry.title}
        description="Adjust this row in the current Home draft. Save the Home layout to apply it."
        size="lg"
      >
        {" "}
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Segmented
            ariaLabel={`Status of ${entry.title}`}
            size="sm"
            value={effective.status}
            onChange={(s) =>
              edit({ ...effective, status: s, slot: s === "off" ? undefined : effective.slot })
            }
            options={statuses.map((s) => ({
              value: s,
              label: <span title={STATUS_HINT[s]}>{STATUS_LABEL[s]}</span>,
            }))}
          />
          {own ? (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={reset}
              aria-label={`${layer === "global" ? "Back to default" : "Reset to global"}: ${entry.title}`}
            >
              <RotateCcw size={12} /> {layer === "global" ? "Default" : "Reset to global"}
            </Button>
          ) : null}
        </div>
        <PolicyEditor policy={effective} title={entry.title} onChange={edit} />
      </Drawer>
    </li>
  );
}
