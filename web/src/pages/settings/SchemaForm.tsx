import { Input, SearchSelect, SettingsRow, SettingsSection, Switch, Textarea } from "@braedonsaunders/appkit-ui";
import type { ReactNode } from "react";
import { ListInput, NumberInput, SecretInput } from "../../components/fields";
import { humanizeKey, isPlainObject } from "../../lib/format";
import { REDACTED } from "../../lib/types";

type Obj = Record<string, unknown>;

export type FieldDef =
  | { key: string; label: string; help?: ReactNode; kind: "text" | "secret" | "duration" | "readonly"; placeholder?: string; wide?: boolean }
  | { key: string; label: string; help?: ReactNode; kind: "number"; min?: number; max?: number; step?: number; unit?: string }
  | { key: string; label: string; help?: ReactNode; kind: "bool" }
  | { key: string; label: string; help?: ReactNode; kind: "select"; options: { value: string; label: string }[] }
  | { key: string; label: string; help?: ReactNode; kind: "list"; placeholder?: string; numeric?: boolean }
  | { key: string; label: string; help?: ReactNode; kind: "bytes"; unit: "GiB" | "MiB" };

export interface FieldGroup { title?: string; description?: string; fields: FieldDef[] }

const UNIT_BYTES = { GiB: 1024 ** 3, MiB: 1024 ** 2 } as const;

/** Renders one field as an AppKit SettingsRow. `errors` is keyed by the field's dotted path. */
export function SchemaField({ def, value, onChange, error, path }: { def: FieldDef; value: unknown; onChange: (v: unknown) => void; error?: string; path: string }) {
  const id = `f-${path.replace(/[^a-z0-9]+/gi, "-")}`;
  const description = (
    <>
      {def.help}
      {error ? <span className="mt-1 block text-xs text-danger" role="alert">{error}</span> : null}
    </>
  );
  switch (def.kind) {
    case "bool":
      return <SettingsRow title={<label htmlFor={id}>{def.label}</label>} description={description}
        control={<Switch id={id} checked={Boolean(value)} onChange={(e) => onChange(e.currentTarget.checked)} aria-label={def.label} />} />;
    case "number":
      return <SettingsRow title={<label htmlFor={id}>{def.label}</label>} description={description}
        control={<span className="flex items-center gap-2"><NumberInput id={id} aria-label={def.label} aria-invalid={error ? true : undefined} className="w-28" value={Number(value ?? 0)} min={def.min} max={def.max} step={def.step ?? "any"} onChange={onChange} />{def.unit ? <span className="text-sm text-fg-muted">{def.unit}</span> : null}</span>} />;
    case "bytes": {
      const unit = UNIT_BYTES[def.unit];
      return <SettingsRow title={<label htmlFor={id}>{def.label}</label>} description={description}
        control={<span className="flex items-center gap-2"><NumberInput id={id} aria-label={`${def.label} in ${def.unit}`} className="w-28" value={Math.round((Number(value ?? 0) / unit) * 10) / 10} min={0} step={1} onChange={(n) => onChange(Math.round(n * unit))} /><span className="text-sm text-fg-muted">{def.unit}</span></span>} />;
    }
    case "select":
      return <SettingsRow title={def.label} description={description}
        control={<div className="w-48"><SearchSelect value={String(value ?? "")} onChange={onChange} options={def.options} ariaLabel={def.label} searchable={def.options.length > 7} /></div>} />;
    case "list":
      return <SettingsRow stacked title={<label htmlFor={id}>{def.label}</label>} description={description}>
        <ListInput ariaLabel={def.label} value={(value as (string | number)[] | null) ?? []} onChange={onChange} placeholder={def.placeholder} numeric={def.numeric} />
      </SettingsRow>;
    case "secret":
      return <SettingsRow stacked title={<label htmlFor={id}>{def.label}</label>} description={description}>
        <SecretInput id={id} ariaLabel={def.label} value={String(value ?? "")} onChange={onChange} placeholder={def.placeholder} invalid={Boolean(error)} />
      </SettingsRow>;
    case "readonly":
      return <SettingsRow title={def.label} description={description} control={<code className="max-w-64 truncate rounded bg-bg-subtle px-2 py-1 text-xs text-fg-muted" title={String(value ?? "")}>{String(value ?? "") || "—"}</code>} />;
    default: {
      const input = <Input id={id} aria-label={def.label} aria-invalid={error ? true : undefined} value={String(value ?? "")} placeholder={def.placeholder ?? (def.kind === "duration" ? "e.g. 24h or 30m" : undefined)} onChange={(e) => onChange(e.currentTarget.value)} className={def.wide ? undefined : "sm:w-72"} spellCheck={false} />;
      return def.wide
        ? <SettingsRow stacked title={<label htmlFor={id}>{def.label}</label>} description={description}>{input}</SettingsRow>
        : <SettingsRow title={<label htmlFor={id}>{def.label}</label>} description={description} control={input} />;
    }
  }
}

export function SchemaGroups({ groups, value, onChange, errors, prefix, footer }: {
  groups: FieldGroup[]; value: Obj; onChange: (v: Obj) => void; errors: Record<string, string>; prefix: string; footer?: ReactNode;
}) {
  const known = new Set(groups.flatMap((g) => g.fields.map((f) => f.key)));
  const extra = Object.keys(value).filter((k) => !known.has(k));
  return (
    <>
      {groups.map((g, i) => (
        <SettingsSection key={g.title ?? i} title={g.title} description={g.description} footer={i === groups.length - 1 && !extra.length ? footer : undefined}>
          {g.fields.map((f) => (
            <SchemaField key={f.key} def={f} value={value[f.key]} path={`${prefix}.${f.key}`} error={errors[`${prefix}.${f.key}`]} onChange={(v) => onChange({ ...value, [f.key]: v })} />
          ))}
        </SettingsSection>
      ))}
      {extra.length ? (
        <SettingsSection title="Advanced" description="Additional options this server supports." footer={footer}>
          <GenericFields value={value} keys={extra} onChange={onChange} errors={errors} prefix={prefix} />
        </SettingsSection>
      ) : null}
    </>
  );
}

/** Renders keys the UI has no schema for, so new server options are never unreachable. */
export function GenericFields({ value, keys, onChange, errors, prefix }: { value: Obj; keys: string[]; onChange: (v: Obj) => void; errors: Record<string, string>; prefix: string }) {
  return (
    <>
      {keys.map((k) => {
        const v = value[k];
        const set = (nv: unknown) => onChange({ ...value, [k]: nv });
        const label = humanizeKey(k);
        const path = `${prefix}.${k}`;
        if (typeof v === "boolean") return <SchemaField key={k} def={{ key: k, label, kind: "bool" }} value={v} onChange={set} path={path} error={errors[path]} />;
        if (typeof v === "number") return <SchemaField key={k} def={{ key: k, label, kind: "number" }} value={v} onChange={set} path={path} error={errors[path]} />;
        if (typeof v === "string") return <SchemaField key={k} def={{ key: k, label, kind: v === REDACTED || /password|secret|token|apikey|key$/i.test(k) ? "secret" : "text" }} value={v} onChange={set} path={path} error={errors[path]} />;
        if (v === null || (Array.isArray(v) && v.every((x) => typeof x === "string" || typeof x === "number"))) {
          return <SchemaField key={k} def={{ key: k, label, kind: "list", numeric: Array.isArray(v) && typeof v[0] === "number" }} value={v} onChange={set} path={path} error={errors[path]} />;
        }
        return <JsonRow key={k} label={label} value={v} onChange={set} error={errors[path]} />;
      })}
    </>
  );
}

function JsonRow({ label, value, onChange, error }: { label: string; value: unknown; onChange: (v: unknown) => void; error?: string }) {
  const text = JSON.stringify(value, null, 2);
  return (
    <SettingsRow stacked title={label} description={error ? <span className="text-danger">{error}</span> : "Edited as JSON."}>
      <Textarea key={text} defaultValue={text} rows={Math.min(12, text.split("\n").length + 1)} className="font-mono text-xs" aria-label={label}
        onBlur={(e) => { try { onChange(JSON.parse(e.currentTarget.value)); } catch { /* keep the last valid value */ } }} />
    </SettingsRow>
  );
}

export function asObj(v: unknown): Obj {
  return isPlainObject(v) ? v : {};
}
