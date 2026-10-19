import { Avatar, Badge, cn } from "@braedonsaunders/appkit-ui";
import { Globe2 } from "lucide-react";
import { viewerName } from "../../components/pickers";
import type { HomeLayout, Viewer } from "../../lib/contract";
import { plural } from "../../lib/format";
import { overrideCount, type LayerKey } from "../../lib/layout";

/** Which layer the controls edit: the Global Home, or one viewer's overrides. */
export function LayerSwitcher({ layer, onChange, viewers, draft }: {
  layer: LayerKey; onChange: (l: LayerKey) => void; viewers: Viewer[]; draft: HomeLayout;
}) {
  const item = (key: LayerKey, label: string, sub: string, icon: React.ReactNode, count?: number) => {
    const on = layer === key;
    return (
      <button key={String(key)} type="button" role="radio" aria-checked={on} onClick={() => onChange(key)}
        className={cn(
          "flex w-full items-center gap-2.5 rounded-lg border px-2.5 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          on ? "border-primary/40 bg-primary-subtle" : "border-transparent hover:bg-surface-hover",
        )}>
        {icon}
        <span className="min-w-0 flex-1">
          <span className={cn("block truncate text-sm font-medium", on ? "text-primary" : "text-fg")}>{label}</span>
          <span className="block truncate text-xs text-fg-muted">{sub}</span>
        </span>
        {count ? <Badge variant="info" className="shrink-0">{plural(count, "override")}</Badge> : null}
      </button>
    );
  };
  return (
    <section className="space-y-2">
      <h2 className="text-xs font-semibold uppercase tracking-wider text-fg-subtle">Editing</h2>
      <div role="radiogroup" aria-label="Editing layer" className="space-y-1">
        {item("global", "Global Home", `Everyone · ${plural(viewers.length, "viewer")}`,
          <span className="grid size-7 shrink-0 place-items-center rounded-full bg-primary-subtle text-primary"><Globe2 size={15} /></span>)}
        <p className="px-1 pt-2 text-[11px] font-medium uppercase tracking-wider text-fg-subtle">Overrides for one viewer</p>
        {viewers.map((v) => item(v.id, viewerName(v), v.isOwner ? "Owner" : `@${v.username}`, <Avatar src={v.thumb} name={viewerName(v)} size={28} />, overrideCount(draft, v.id)))}
      </div>
    </section>
  );
}
