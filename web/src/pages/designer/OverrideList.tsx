import { Avatar, Button, Input } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { ChevronRight, RotateCcw } from "lucide-react";
import { viewerName } from "../../components/pickers";
import type { HomeLayout, LayoutSection, Viewer } from "../../lib/contract";
import { WEEKDAYS, plural } from "../../lib/format";
import {
  DEVICES,
  SECTIONS,
  STATUS_LABEL,
  overrideCount,
  pageOf,
  policyChips,
  removePolicy,
  resetViewer,
  setPageLength,
  type LayerKey,
} from "../../lib/layout";

/**
 * Per-viewer overrides. On Global: who overrides what (open a viewer to
 * edit). On a viewer: each override on this page with "reset to global".
 */
export function OverrideList({
  draft,
  onChange,
  layer,
  section,
  viewers,
  onOpenLayer,
}: {
  draft: HomeLayout;
  onChange: (d: HomeLayout) => void;
  layer: LayerKey;
  section: LayoutSection;
  viewers: Viewer[];
  onOpenLayer: (l: LayerKey) => void;
}) {
  const [q, setQ] = useState("");
  const sectionLabel = SECTIONS.find((s) => s.value === section)?.label ?? section;
  if (layer === "global") {
    const all = viewers.filter((v) => overrideCount(draft, v.id) > 0);
    const needle = q.trim().toLowerCase();
    const withOverrides = needle
      ? all.filter((v) => `${viewerName(v)} ${v.username}`.toLowerCase().includes(needle))
      : all;
    return (
      <section className="space-y-2" aria-label="Per-viewer overrides">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">
            Per-viewer overrides
          </h2>
          {all.length ? (
            <span className="text-xs text-fg-muted">{plural(all.length, "viewer")}</span>
          ) : null}
        </div>
        {all.length > 6 ? (
          <Input
            value={q}
            onChange={(e) => setQ(e.currentTarget.value)}
            placeholder="Find a viewer…"
            aria-label="Find a viewer with overrides"
            className="h-8 text-sm"
          />
        ) : null}
        {all.length === 0 ? (
          <p className="text-xs text-fg-muted">
            Nobody overrides everyone's Home. Switch the editing layer at the top to a viewer to
            pin, hide or add rows just for them.
          </p>
        ) : withOverrides.length === 0 ? (
          <p className="text-xs text-fg-muted">No viewer with overrides matches “{q}”.</p>
        ) : (
          <ul className="space-y-1">
            {withOverrides.map((v) => (
              <li key={v.id}>
                <button
                  type="button"
                  onClick={() => onOpenLayer(v.id)}
                  aria-label={`Open ${viewerName(v)}'s overrides`}
                  className="flex w-full items-center gap-2 rounded-lg border border-border bg-surface px-2.5 py-2 text-left hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                >
                  <Avatar src={v.thumb} name={viewerName(v)} size={24} />
                  <span className="min-w-0 flex-1 truncate text-sm text-fg">{viewerName(v)}</span>
                  <span className="text-xs text-fg-muted">
                    {plural(overrideCount(draft, v.id), "override")}
                  </span>
                  <ChevronRight size={14} className="text-fg-subtle" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    );
  }
  const v = viewers.find((x) => x.id === layer);
  const name = v ? viewerName(v) : `Viewer ${layer}`;
  const page = pageOf(draft, layer, section);
  const lengths = [
    ...(page?.maxRows ? [{ device: null, n: page.maxRows }] : []),
    ...DEVICES.filter((d) => page?.deviceMaxRows?.[d.value]).map((d) => ({
      device: d.value,
      n: page?.deviceMaxRows?.[d.value] as number,
    })),
  ];
  const total = overrideCount(draft, layer);
  return (
    <section className="space-y-2" aria-label={`Overrides for ${name}`}>
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">
          Overrides for {name}
        </h2>
        {total ? (
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-xs"
            onClick={() => onChange(resetViewer(draft, layer))}
          >
            <RotateCcw size={12} /> Reset all to Everyone
          </Button>
        ) : null}
      </div>
      {!page || (page.rows.length === 0 && lengths.length === 0) ? (
        <p className="text-xs text-fg-muted">
          {name} follows everyone's Home on {sectionLabel}.
          {total ? ` (${plural(total, "override")} on other pages.)` : ""}
        </p>
      ) : (
        <ul className="space-y-1">
          {lengths.map((l) => {
            const label = l.device
              ? `${DEVICES.find((d) => d.value === l.device)?.label} page length`
              : "Page length";
            return (
              <li
                key={l.device ?? "all"}
                className="flex items-center gap-2 rounded-lg border border-info/40 bg-surface px-2.5 py-1.5"
              >
                <span className="min-w-0 flex-1 text-sm text-fg">
                  {label}: {l.n} rows
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  aria-label={`Reset to Everyone: ${label}`}
                  onClick={() =>
                    onChange(setPageLength(draft, layer, section, l.device, undefined))
                  }
                >
                  <RotateCcw size={12} /> Reset
                </Button>
              </li>
            );
          })}
          {page.rows.map((p) => (
            <li
              key={p.row}
              className="flex items-center gap-2 rounded-lg border border-info/40 bg-surface px-2.5 py-1.5"
            >
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm text-fg">{p.row}</span>
                <span className="block truncate text-[11px] text-fg-muted">
                  {[STATUS_LABEL[p.status], ...policyChips(p, WEEKDAYS)].join(" · ")}
                </span>
              </span>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                aria-label={`Reset to Everyone: ${p.row}`}
                onClick={() => onChange(removePolicy(draft, layer, section, p.row))}
              >
                <RotateCcw size={12} /> Reset
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
