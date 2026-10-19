import { SearchSelect } from "@braedonsaunders/appkit-ui";
import { Globe, Laptop, Smartphone, Tv } from "lucide-react";
import { Segmented } from "../../components/fields";
import type { Device, LayoutSection } from "../../lib/contract";
import { WEEKDAYS_LONG, formatHour } from "../../lib/format";
import { SECTIONS } from "../../lib/layout";

/** The context a Home is previewed in (the viewer is chosen separately). */
export interface ViewContext {
  device: Device;
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

export function ContextBar({ ctx, onChange }: { ctx: ViewContext; onChange: (c: ViewContext) => void }) {
  return (
    <div className="space-y-2" role="group" aria-label="Preview context">
      <Segmented ariaLabel="Page" size="sm" value={ctx.section} onChange={(s) => onChange({ ...ctx, section: s })} options={SECTIONS} />
      <Segmented ariaLabel="Device" size="sm" value={ctx.device} onChange={(d) => onChange({ ...ctx, device: d })}
        options={DEVICE_OPTIONS.map((d) => ({ value: d.value, label: d.label, icon: d.icon }))} />
      <div className="grid grid-cols-2 gap-2">
        <SearchSelect value={String(ctx.day)} onChange={(v) => v !== "" && onChange({ ...ctx, day: Number(v) })} ariaLabel="Day" searchable={false}
          options={WEEKDAYS_LONG.map((d, i) => ({ value: String(i), label: d }))} />
        <SearchSelect value={String(ctx.hour)} onChange={(v) => v !== "" && onChange({ ...ctx, hour: Number(v) })} ariaLabel="Time"
          options={Array.from({ length: 24 }, (_, h) => ({ value: String(h), label: formatHour(h) }))} />
      </div>
    </div>
  );
}
