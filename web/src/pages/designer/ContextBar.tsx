import { Avatar, SearchSelect } from "@braedonsaunders/appkit-ui";
import { Globe, Laptop, Smartphone, Tv } from "lucide-react";
import { Segmented } from "../../components/fields";
import { viewerName } from "../../components/pickers";
import type { LayoutSection, Viewer } from "../../lib/contract";
import { WEEKDAYS_LONG, formatHour } from "../../lib/format";

export interface ViewContext {
  viewerId: number;
  device: "tv" | "mobile" | "web" | "desktop";
  day: number;
  hour: number;
  section: LayoutSection;
}

export const DEVICE_OPTIONS = [
  { value: "tv" as const, label: "TV", icon: <Tv size={14} /> },
  { value: "mobile" as const, label: "Phone", icon: <Smartphone size={14} /> },
  { value: "web" as const, label: "Web", icon: <Globe size={14} /> },
  { value: "desktop" as const, label: "Desktop", icon: <Laptop size={14} /> },
];

export function ContextBar({ ctx, onChange, viewers, compact, idPrefix }: {
  ctx: ViewContext; onChange: (c: ViewContext) => void; viewers: Viewer[]; compact?: boolean; idPrefix: string;
}) {
  const current = viewers.find((v) => v.id === ctx.viewerId);
  return (
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label={`${idPrefix} context`}>
      <div className="flex min-w-48 items-center gap-2">
        {current ? <Avatar src={current.thumb} name={viewerName(current)} size={28} /> : null}
        <div className="min-w-40 flex-1">
          <SearchSelect value={String(ctx.viewerId)} onChange={(v) => v && onChange({ ...ctx, viewerId: Number(v) })} ariaLabel={`${idPrefix} viewer`}
            options={viewers.map((v) => ({ value: String(v.id), label: viewerName(v), hint: v.isOwner ? "Owner" : undefined }))} />
        </div>
      </div>
      <Segmented ariaLabel={`${idPrefix} device`} size="sm" value={ctx.device} onChange={(d) => onChange({ ...ctx, device: d })}
        options={DEVICE_OPTIONS.map((d) => ({ value: d.value, label: compact ? <span className="sr-only">{d.label}</span> : d.label, icon: d.icon }))} />
      <div className="w-36">
        <SearchSelect value={String(ctx.day)} onChange={(v) => v !== "" && onChange({ ...ctx, day: Number(v) })} ariaLabel={`${idPrefix} day`} searchable={false}
          options={WEEKDAYS_LONG.map((d, i) => ({ value: String(i), label: d }))} />
      </div>
      <div className="w-28">
        <SearchSelect value={String(ctx.hour)} onChange={(v) => v !== "" && onChange({ ...ctx, hour: Number(v) })} ariaLabel={`${idPrefix} time`}
          options={Array.from({ length: 24 }, (_, h) => ({ value: String(h), label: formatHour(h) }))} />
      </div>
      <Segmented ariaLabel={`${idPrefix} section`} size="sm" value={ctx.section} onChange={(s) => onChange({ ...ctx, section: s })}
        options={[{ value: "home", label: "Home" }, { value: "movies", label: "Movies" }, { value: "shows", label: "TV" }]} />
    </div>
  );
}
