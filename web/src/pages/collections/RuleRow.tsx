import { Button, Input, SearchSelect, Slider, cn } from "@braedonsaunders/appkit-ui";
import { AlertCircle, Trash2 } from "lucide-react";
import { Segmented, NumberInput } from "../../components/fields";
import { FacetPicker, FacetSelect, LookupPicker, type Picked } from "../../components/pickers";
import type { Rule, RuleField, RuleOp } from "../../lib/contract";
import {
  FIELDS,
  WITHIN_UNITS,
  describeRule,
  fieldMeta,
  isRuleComplete,
  opLabel,
  parseWithin,
  withField,
  withOp,
  type FieldMeta,
} from "../../lib/rules";

const FIELD_OPTIONS = FIELDS.map((f) => ({
  value: f.field,
  label: f.label,
  hint: f.hint,
  group: f.group,
}));

/** One condition: field, operator and a value editor that fits the field. */
export function RuleRow({
  rule,
  onChange,
  onRemove,
  mediaType,
  region,
  index,
  kind,
}: {
  rule: Rule;
  onChange: (r: Rule) => void;
  onRemove: () => void;
  mediaType: string;
  region: string;
  index: number;
  kind: "include" | "exclude";
}) {
  const meta = fieldMeta(rule.field);
  const complete = isRuleComplete(rule);
  const label = `${kind === "exclude" ? "Exclusion" : "Rule"} ${index + 1}`;
  return (
    <div
      className={cn(
        "rounded-lg border bg-surface p-3",
        complete ? "border-border" : "border-warning/40",
      )}
      role="group"
      aria-label={label}
    >
      <div className="flex flex-wrap items-start gap-2">
        <div className="w-full sm:w-48">
          <SearchSelect
            value={rule.field}
            onChange={(v) => v && onChange(withField(rule, v as RuleField))}
            options={FIELD_OPTIONS}
            ariaLabel={`${label} field`}
            searchable
          />
        </div>
        {meta ? (
          <div className="w-full sm:w-40">
            <SearchSelect
              value={rule.op}
              onChange={(v) => v && onChange(withOp(rule, v as RuleOp))}
              options={meta.ops.map((op) => ({ value: op, label: opLabel(op, meta.kind) }))}
              ariaLabel={`${label} condition`}
              searchable={false}
            />
          </div>
        ) : null}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="ml-auto size-9 text-fg-muted hover:text-danger"
          onClick={onRemove}
          aria-label={`Remove ${label.toLowerCase()}`}
        >
          <Trash2 size={15} />
        </Button>
      </div>
      <div className="mt-2">
        {meta ? (
          <ValueEditor
            rule={rule}
            meta={meta}
            onChange={onChange}
            mediaType={mediaType}
            region={region}
            label={label}
          />
        ) : (
          <RawValue rule={rule} onChange={onChange} label={label} />
        )}
      </div>
      <p
        className={cn(
          "mt-2 flex items-center gap-1.5 text-xs",
          complete ? "text-fg-muted" : "text-warning",
        )}
      >
        {complete ? null : <AlertCircle size={12} aria-hidden />}
        {complete
          ? describeRule(rule)
          : "Pick a value to apply this rule. It's left out of the preview until then."}
      </p>
    </div>
  );
}

function toPicked(rule: Rule): Picked[] {
  const ids = (
    Array.isArray(rule.value)
      ? rule.value
      : rule.value === "" || rule.value == null
        ? []
        : [rule.value]
  )
    .map(Number)
    .filter((n) => Number.isFinite(n) && n > 0);
  const names = rule.label ? rule.label.split(", ") : [];
  return ids.map((id, i) => ({
    id,
    name: names.length === ids.length ? (names[i] ?? `#${id}`) : `#${id}`,
  }));
}

function ValueEditor({
  rule,
  meta,
  onChange,
  mediaType,
  region,
  label,
}: {
  rule: Rule;
  meta: FieldMeta;
  onChange: (r: Rule) => void;
  mediaType: string;
  region: string;
  label: string;
}) {
  const multi = rule.op === "in" || rule.op === "notIn";
  const facetType = mediaType === "show" ? "show" : "movie";
  switch (meta.kind) {
    case "facet":
      return multi ? (
        <FacetPicker
          type={facetType}
          facet={meta.facet!}
          value={(Array.isArray(rule.value) ? rule.value : []).map(String)}
          onChange={(v) => onChange({ ...rule, value: v })}
          ariaLabel={`${label} values`}
        />
      ) : (
        <FacetSelect
          type={facetType}
          facet={meta.facet!}
          value={String(rule.value ?? "")}
          onChange={(v) => onChange({ ...rule, value: v })}
          ariaLabel={`${label} value`}
        />
      );
    case "lookup":
      return (
        <LookupPicker
          kind={meta.lookup!}
          region={region}
          multiple={multi}
          value={toPicked(rule)}
          ariaLabel={`${label} search`}
          onChange={(picked) =>
            onChange({
              ...rule,
              value: multi ? picked.map((p) => p.id) : (picked[0]?.id ?? ""),
              label: picked.map((p) => p.name).join(", ") || undefined,
            })
          }
        />
      );
    case "bool":
      return (
        <Segmented
          ariaLabel={`${label} value`}
          size="sm"
          value={rule.value === false ? "no" : "yes"}
          onChange={(v) => onChange({ ...rule, value: v === "yes" })}
          options={[
            { value: "yes", label: "Yes" },
            { value: "no", label: "No" },
          ]}
        />
      );
    case "number": {
      if (rule.op === "between") {
        const [lo, hi] = Array.isArray(rule.value) ? rule.value : [0, 0];
        return (
          <div className="flex items-center gap-2">
            <NumberInput
              aria-label={`${label} minimum`}
              value={Number(lo)}
              min={meta.min}
              max={meta.max}
              step={meta.step}
              onChange={(n) => onChange({ ...rule, value: [n, Number(hi)] })}
            />
            <span className="text-sm text-fg-muted">and</span>
            <NumberInput
              aria-label={`${label} maximum`}
              value={Number(hi)}
              min={meta.min}
              max={meta.max}
              step={meta.step}
              onChange={(n) => onChange({ ...rule, value: [Number(lo), n] })}
            />
            {meta.unit ? <span className="shrink-0 text-sm text-fg-muted">{meta.unit}</span> : null}
          </div>
        );
      }
      return (
        <div className="flex items-center gap-2">
          <NumberInput
            aria-label={`${label} value`}
            value={Number(rule.value)}
            min={meta.min}
            max={meta.max}
            step={meta.step}
            onChange={(n) => onChange({ ...rule, value: n })}
            className="max-w-40"
          />
          {meta.unit ? <span className="text-sm text-fg-muted">{meta.unit}</span> : null}
          {meta.field === "rating" ? (
            <Slider
              min={0}
              max={10}
              step={0.1}
              value={Number(rule.value)}
              aria-label={`${label} slider`}
              onChange={(e) => onChange({ ...rule, value: e.currentTarget.valueAsNumber })}
              className="ml-2 flex-1"
            />
          ) : null}
        </div>
      );
    }
    case "date": {
      if (rule.op === "within") {
        const w = parseWithin(rule.value);
        return (
          <div className="flex items-center gap-2">
            <NumberInput
              aria-label={`${label} amount`}
              value={w.n}
              min={1}
              step={1}
              className="max-w-24"
              onChange={(n) =>
                onChange({ ...rule, value: `${Math.max(1, Math.round(n))}${w.unit}` })
              }
            />
            <div className="w-32">
              <SearchSelect
                value={w.unit}
                onChange={(u) => u && onChange({ ...rule, value: `${w.n}${u}` })}
                options={WITHIN_UNITS.map((u) => ({ value: u.value, label: u.label }))}
                ariaLabel={`${label} unit`}
                searchable={false}
              />
            </div>
          </div>
        );
      }
      if (rule.op === "between") {
        const [a, b] = Array.isArray(rule.value) ? rule.value.map(String) : ["", ""];
        return (
          <div className="flex flex-wrap items-center gap-2">
            <Input
              type="date"
              aria-label={`${label} from`}
              value={a}
              onChange={(e) => onChange({ ...rule, value: [e.currentTarget.value, b] })}
              className="w-44"
            />
            <span className="text-sm text-fg-muted">and</span>
            <Input
              type="date"
              aria-label={`${label} to`}
              value={b}
              onChange={(e) => onChange({ ...rule, value: [a, e.currentTarget.value] })}
              className="w-44"
            />
          </div>
        );
      }
      return (
        <Input
          type="date"
          aria-label={`${label} date`}
          value={String(rule.value ?? "")}
          onChange={(e) => onChange({ ...rule, value: e.currentTarget.value })}
          className="w-44"
        />
      );
    }
  }
}

/** Fallback for a field this UI doesn't know (e.g. a newer server template). */
function RawValue({
  rule,
  onChange,
  label,
}: {
  rule: Rule;
  onChange: (r: Rule) => void;
  label: string;
}) {
  const text = JSON.stringify(rule.value);
  return (
    <Input
      key={text}
      defaultValue={text}
      aria-label={`${label} raw value`}
      className="font-mono text-xs"
      onBlur={(e) => {
        try {
          onChange({ ...rule, value: JSON.parse(e.currentTarget.value) });
        } catch {
          /* keep previous */
        }
      }}
    />
  );
}
