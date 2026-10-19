import { humanizeKey } from "./format";
import { REDACTED } from "./types";
import { SecretInput } from "./components";

type AnyObj = Record<string, unknown>;

const SECRET_KEYS = new Set(["password", "apiKey", "token", "tmdbKey", "clientSecret"]);
const READONLY_KEYS = new Set(["machineIdentifier", "clientIdentifier"]);

const HINTS: Record<string, string> = {
  budgetBytes: "Disk budget for cached segments, in bytes.",
  prefetchBytes: "Opening bytes pulled ahead for the next episode.",
  nextEpisodeAt: "Fraction of the current episode (0–1) at which prefetch starts.",
  allowCam: "Allow CAM / telesync releases. Off by default.",
  dailyLimit: "API hits per day. 0 = unlimited.",
  dailyGrabs: "NZB downloads per day. 0 = unlimited.",
  priority: "Lower is tried first.",
  backfill: "Block account: only used for articles missing elsewhere.",
  refreshInterval: "Go duration, e.g. 24h.",
  searchCacheTtl: "Go duration, e.g. 6h.",
  unavailableCooloff: "How long a title with no release stays hidden.",
  minSizeMbPerMin: "Size sanity floor per minute of runtime (MB).",
  maxSizeMbPerMin: "Size sanity ceiling per minute of runtime (MB).",
};

function blankFor(sample: unknown): unknown {
  if (Array.isArray(sample)) return [];
  if (typeof sample === "boolean") return false;
  if (typeof sample === "number") return 0;
  if (typeof sample === "string") return "";
  if (sample && typeof sample === "object") {
    const out: AnyObj = {};
    for (const [k, v] of Object.entries(sample as AnyObj)) out[k] = blankFor(v);
    if ("enabled" in out) out.enabled = true;
    if ("id" in out) out.id = `item-${Math.random().toString(36).slice(2, 8)}`;
    return out;
  }
  return "";
}

interface Props {
  value: unknown;
  onChange: (v: unknown) => void;
  path?: string;
}

/** Recursive editor for any config value: guarantees every field the Go
 * schema exposes stays editable even as the schema grows. */
export function ValueEditor({ value, onChange, path = "" }: Props) {
  if (Array.isArray(value)) {
    const isPrimitive = value.length === 0 || value.every((v) => ["string", "number"].includes(typeof v));
    if (isPrimitive) {
      const numeric = value.length > 0 && typeof value[0] === "number";
      const text = value.join(", ");
      return (
        <input
          className="input"
          defaultValue={text}
          key={text}
          aria-label={path || "list"}
          placeholder="comma-separated"
          onBlur={(e) => {
            const parts = e.target.value.split(",").map((s) => s.trim()).filter(Boolean);
            onChange(numeric ? parts.map(Number).filter((n) => Number.isFinite(n)) : parts);
          }}
        />
      );
    }
    return (
      <div className="editor-list">
        {value.map((item, i) => (
          <div key={i} className="editor-item">
            <ValueEditor
              value={item}
              path={`${path}[${i}]`}
              onChange={(v) => { const next = [...value]; next[i] = v; onChange(next); }}
            />
            <div className="row-actions">
              <button type="button" className="btn btn-ghost btn-small" disabled={i === 0}
                onClick={() => { const n = [...value]; [n[i - 1], n[i]] = [n[i], n[i - 1]]; onChange(n); }}>Move up</button>
              <button type="button" className="btn btn-ghost btn-small"
                onClick={() => onChange(value.filter((_, j) => j !== i))}>Remove</button>
            </div>
          </div>
        ))}
        <button type="button" className="btn btn-small"
          onClick={() => onChange([...value, blankFor(value[0])])}>Add item</button>
      </div>
    );
  }
  if (value && typeof value === "object") {
    const obj = value as AnyObj;
    return (
      <div className="editor-object">
        {Object.keys(obj).map((key) => (
          <div key={key} className="editor-row">
            <span className="editor-key">{humanizeKey(key)}{HINTS[key] ? <span className="field-hint block">{HINTS[key]}</span> : null}</span>
            <ValueEditor value={obj[key]} path={key} onChange={(v) => onChange({ ...obj, [key]: v })} />
          </div>
        ))}
      </div>
    );
  }
  const keyName = path.split(".").pop() ?? path;
  if (typeof value === "boolean") {
    return <input type="checkbox" className="checkbox" checked={value} aria-label={keyName} onChange={(e) => onChange(e.target.checked)} />;
  }
  if (typeof value === "number") {
    return (
      <input className="input input-num" type="number" value={Number.isFinite(value) ? value : 0} aria-label={keyName}
        step={Number.isInteger(value) ? 1 : "any"}
        onChange={(e) => { const n = e.target.valueAsNumber; onChange(Number.isFinite(n) ? n : 0); }} />
    );
  }
  if (SECRET_KEYS.has(keyName) || value === REDACTED) {
    return <SecretInput value={String(value ?? "")} onChange={onChange} />;
  }
  return (
    <input className="input" type="text" value={String(value ?? "")} aria-label={keyName}
      readOnly={READONLY_KEYS.has(keyName)} onChange={(e) => onChange(e.target.value)} />
  );
}
