import { SettingsSection, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { useCallback, useState, type ReactNode } from "react";
import { useApp } from "../app-context";
import { api, ApiError } from "../lib/api";
import { useAsync } from "../lib/hooks";
import type { EtherConfig } from "../lib/types";
import { asObj, GenericFields, SchemaField, type FieldGroup } from "../pages/settings/SchemaForm";
import { ErrorPanel } from "./feedback";
import { SaveBar, useDraft } from "./SaveBar";

type Obj = Record<string, unknown>;

/**
 * One owner config section (`config.json` key) edited in place: every field
 * group stacked on the page, server-validated errors beside each field, and the
 * shared save bar while there are unsaved changes.
 */
export function ConfigSection({
  configKey,
  endpoint,
  groups,
  label,
  before,
  after,
  test,
  onSaved,
  omit,
}: {
  /** Key in config.json, e.g. "cache" or "openSubtitles". */
  configKey: string;
  /**
   * `/config/<endpoint>` for sections the server saves on their own; omit to
   * save the whole config (sections this UI has no dedicated endpoint for).
   */
  endpoint?: string;
  groups: FieldGroup[];
  /** Used in the "Settings saved" toast. */
  label: string;
  /** Status or context rendered above the fields, given the live draft. */
  before?: (draft: Obj, cfg: EtherConfig) => ReactNode;
  after?: (draft: Obj, cfg: EtherConfig) => ReactNode;
  /** A connection test rendered under the fields, given the live draft. */
  test?: (draft: Obj) => ReactNode;
  onSaved?: (value: Obj) => void;
  /** Keys edited elsewhere: kept in the saved value, never shown under "Advanced". */
  omit?: string[];
}) {
  const { refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const cfg = config.data;
  const stored = cfg ? asObj((cfg as unknown as Obj)[configKey]) : null;
  const { draft, setDraft, dirty, reset } = useDraft<Obj>(stored);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);

  const save = useCallback(async () => {
    if (!cfg || !draft) return;
    setSaving(true);
    setError(null);
    setErrors({});
    try {
      if (endpoint) await api.putSection(endpoint, draft);
      else await api.putConfig({ ...cfg, [configKey]: draft } as EtherConfig);
      toast.success("Settings saved", { description: label });
      await config.reload();
      void refreshDashboard();
      onSaved?.(draft);
    } catch (e) {
      setError(e);
      if (e instanceof ApiError && e.fields)
        setErrors(Object.fromEntries(e.fields.map((f) => [f.field, f.message])));
    } finally {
      setSaving(false);
    }
  }, [cfg, config, configKey, draft, endpoint, label, onSaved, refreshDashboard]);

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!cfg || !draft)
    return (
      <div className="py-10 text-center">
        <Spinner size={20} label="Loading settings" />
      </div>
    );

  const known = new Set([...groups.flatMap((g) => g.fields.map((f) => f.key)), ...(omit ?? [])]);
  const extra = Object.keys(draft).filter((k) => !known.has(k));
  const change = (next: Obj) => setDraft(next);
  return (
    <>
      {before?.(draft, cfg)}
      {groups.map((g, i) => (
        <SettingsSection
          key={g.title ?? `group-${i}`}
          title={g.title ?? (i === 0 ? label : undefined)}
          description={g.description}
        >
          {g.fields.map((f) => (
            <SchemaField
              key={f.key}
              def={f}
              value={draft[f.key]}
              path={`${configKey}.${f.key}`}
              error={errors[`${configKey}.${f.key}`]}
              onChange={(v) => change({ ...draft, [f.key]: v })}
            />
          ))}
        </SettingsSection>
      ))}
      {extra.length ? (
        <SettingsSection title="Advanced" description="Additional options this server supports.">
          <GenericFields
            value={draft}
            keys={extra}
            onChange={change}
            errors={errors}
            prefix={configKey}
          />
        </SettingsSection>
      ) : null}
      {test ? <div className="flex justify-end">{test(draft)}</div> : null}
      {after?.(draft, cfg)}
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={save}
        onDiscard={() => {
          reset();
          setErrors({});
          setError(null);
        }}
      />
    </>
  );
}
