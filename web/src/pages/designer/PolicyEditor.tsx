import { Input, SearchSelect } from "@braedonsaunders/appkit-ui";
import { ChipGroup, Segmented } from "../../components/fields";
import type { Daypart, LayoutWhen, RowPolicy } from "../../lib/contract";
import { WEEKDAYS, formatHour } from "../../lib/format";
import { DAYPARTS, DEVICES, MONTHS, PRIORITIES, hasWhen } from "../../lib/layout";

/** An optional whole number (empty = unset). */
function OptNumber({
  value,
  onChange,
  label,
  min = 1,
  max = 50,
  placeholder,
  disabled,
}: {
  value: number | undefined;
  onChange: (n: number | undefined) => void;
  label: string;
  min?: number;
  max?: number;
  placeholder?: string;
  disabled?: boolean;
}) {
  return (
    <Input
      type="number"
      inputMode="numeric"
      min={min}
      max={max}
      aria-label={label}
      className="h-8 w-20 tabular-nums"
      disabled={disabled}
      value={value ?? ""}
      placeholder={placeholder ?? "–"}
      onChange={(e) => {
        const n = e.currentTarget.valueAsNumber;
        onChange(Number.isFinite(n) && n >= min ? Math.min(max, Math.round(n)) : undefined);
      }}
    />
  );
}

/** Every constraint of one row policy. */
export function PolicyEditor({
  policy,
  title,
  onChange,
}: {
  policy: RowPolicy;
  title: string;
  onChange: (p: RowPolicy) => void;
}) {
  const when = policy.when ?? {};
  const setWhen = (patch: Partial<LayoutWhen>) => {
    const next: LayoutWhen = { ...when, ...patch };
    for (const k of Object.keys(next) as (keyof LayoutWhen)[]) {
      const v = next[k];
      if (v === undefined || v === "" || (Array.isArray(v) && v.length === 0)) delete next[k];
    }
    onChange({ ...policy, when: hasWhen(next) ? next : undefined });
  };
  const hours = Array.from({ length: 25 }, (_, h) => ({
    value: String(h),
    label: h === 24 ? "Midnight" : formatHour(h),
  }));
  const label = (s: string) => `${s} for ${title}`;
  return (
    <div className="space-y-3 border-t border-border-subtle pt-3 text-xs">
      <div className="grid grid-cols-[auto_1fr] items-center gap-x-3 gap-y-2">
        <span className="text-fg-muted">Anchor to slot</span>
        <OptNumber
          label={label("Anchor slot")}
          value={policy.slot}
          placeholder="None"
          onChange={(n) =>
            onChange({
              ...policy,
              slot: n,
              status: n ? "always" : policy.status,
              minSlot: n ? undefined : policy.minSlot,
              maxSlot: n ? undefined : policy.maxSlot,
            })
          }
        />
        <span className="text-fg-muted">Never above slot</span>
        <OptNumber
          label={label("Never above slot")}
          value={policy.minSlot}
          disabled={Boolean(policy.slot)}
          onChange={(n) => onChange({ ...policy, minSlot: n })}
        />
        <span className="text-fg-muted">Never below slot</span>
        <OptNumber
          label={label("Never below slot")}
          value={policy.maxSlot}
          disabled={Boolean(policy.slot)}
          onChange={(n) => onChange({ ...policy, maxSlot: n })}
        />
        {
          <>
            <span className="text-fg-muted">At most per page</span>
            <OptNumber
              label={label("Rows per page")}
              value={policy.maxPerPage}
              max={10}
              placeholder="Default"
              onChange={(n) => onChange({ ...policy, maxPerPage: n })}
            />
            <span className="text-fg-muted">Days a week</span>
            <OptNumber
              label={label("Days a week")}
              value={policy.maxDaysPerWeek}
              max={6}
              placeholder="Every day"
              onChange={(n) => onChange({ ...policy, maxDaysPerWeek: n })}
            />
          </>
        }
      </div>
      <div className="space-y-1.5">
        <p className="text-fg-muted">Priority</p>
        <Segmented
          ariaLabel={label("Priority")}
          size="sm"
          value={String(policy.priority ?? 1)}
          onChange={(v) =>
            onChange({ ...policy, priority: Number(v) === 1 ? undefined : Number(v) })
          }
          options={PRIORITIES.map((p) => ({ value: String(p.value), label: p.label }))}
        />
      </div>
      {
        <fieldset className="space-y-2">
          <legend className="mb-1 text-fg-muted">
            Only show it… <span className="text-fg-subtle">(off outside the schedule)</span>
          </legend>
          <ChipGroup
            ariaLabel={label("Days")}
            size="sm"
            value={when.days ?? []}
            onChange={(d) => setWhen({ days: [...d].sort() })}
            options={WEEKDAYS.map((d, i) => ({ value: i, label: d }))}
          />
          <ChipGroup
            ariaLabel={label("Dayparts")}
            size="sm"
            value={when.dayparts ?? []}
            onChange={(d) => setWhen({ dayparts: d as Daypart[] })}
            options={DAYPARTS}
          />
          <ChipGroup
            ariaLabel={label("Devices")}
            size="sm"
            value={when.devices ?? []}
            onChange={(d) => setWhen({ devices: d })}
            options={DEVICES}
          />
          <ChipGroup
            ariaLabel={label("Months")}
            size="sm"
            value={when.months ?? []}
            onChange={(m) => setWhen({ months: [...m].sort((a, b) => a - b) })}
            options={MONTHS.map((m, i) => ({ value: i + 1, label: m }))}
          />
          <div className="flex flex-wrap items-center gap-2 text-fg-muted">
            From
            <div className="w-28">
              <SearchSelect
                value={when.fromHour === undefined ? "" : String(when.fromHour)}
                onChange={(v) => setWhen({ fromHour: v === "" ? undefined : Number(v) })}
                options={[{ value: "", label: "Any" }, ...hours.slice(0, 24)]}
                ariaLabel={label("From hour")}
              />
            </div>
            until
            <div className="w-28">
              <SearchSelect
                value={when.toHour === undefined ? "" : String(when.toHour)}
                onChange={(v) => setWhen({ toHour: v === "" ? undefined : Number(v) })}
                options={[{ value: "", label: "Any" }, ...hours.slice(1)]}
                ariaLabel={label("Until hour")}
              />
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-fg-muted">
            Between
            <Input
              type="date"
              aria-label={label("Start date")}
              className="h-8 w-36"
              value={when.startDate ?? ""}
              onChange={(e) => setWhen({ startDate: e.currentTarget.value || undefined })}
            />
            and
            <Input
              type="date"
              aria-label={label("End date")}
              className="h-8 w-36"
              value={when.endDate ?? ""}
              onChange={(e) => setWhen({ endDate: e.currentTarget.value || undefined })}
            />
          </div>
        </fieldset>
      }
    </div>
  );
}
