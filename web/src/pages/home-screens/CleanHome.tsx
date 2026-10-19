import { Badge, Button, SettingsSection, Switch, toast } from "@braedonsaunders/appkit-ui";
import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { apiFetch, errorMessage } from "../../lib/api";
import { formatDateTime } from "../../lib/format";
import { useAsync } from "../../lib/hooks";

type Clean = {
  enabled: boolean;
  error: string;
  lastApplied: string;
  sources: { key: string; value: string }[];
  scope: string;
};

const checkedAt = (c: Clean) =>
  c.lastApplied && !c.lastApplied.startsWith("0001") ? formatDateTime(c.lastApplied) : "";

/**
 * Automatic Clean Home: hide Plex's own online sources from the owner's and
 * managed Home accounts so Plex Home shows this server.
 */
export function CleanHome() {
  const clean = useAsync((signal) => apiFetch<Clean>("/safety/clean-home/", { signal }), []);
  const { draft, setDraft, dirty, reset } = useDraft<{ enabled: boolean }>(
    clean.data ? { enabled: clean.data.enabled } : null,
  );
  const [saving, setSaving] = useState(false);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<unknown>(null);

  if (clean.error)
    return (
      <ErrorPanel
        error={clean.error}
        feature="Automatic Clean Home"
        onRetry={() => void clean.reload()}
      />
    );
  if (!clean.data || !draft) return <LoadingRows rows={2} />;
  const c = clean.data;
  const last = checkedAt(c);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const plex = await apiFetch<{ value: Record<string, unknown> }>("/config/plex");
      await apiFetch("/config/plex", {
        method: "PUT",
        body: JSON.stringify({ ...plex.value, disableCleanHome: !draft.enabled }),
      });
      clean.setData({ ...c, enabled: draft.enabled });
      toast.success(draft.enabled ? "Clean Home turned on" : "Clean Home turned off");
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  const applyNow = async () => {
    setApplying(true);
    try {
      clean.setData(await apiFetch<Clean>("/safety/clean-home/", { method: "POST", body: "{}" }));
      toast.success("Clean Home applied");
    } catch (e) {
      toast.error("Couldn't apply Clean Home", { description: errorMessage(e) });
    } finally {
      setApplying(false);
    }
  };

  return (
    <>
      <SettingsSection
        title="Automatic Clean Home"
        description="Turns off Plex's own online sources for you and managed Home accounts. Your channels and libraries stay. Shared friends change their own account settings."
      >
        <SettingsRow
          title="Clean Home automatically"
          description="Ether checks and reapplies this regularly."
          control={
            <Switch
              aria-label="Clean Home automatically"
              checked={draft.enabled}
              onChange={(e) => setDraft({ enabled: e.target.checked })}
            />
          }
        />
        <SettingsRow
          title="Status"
          description={
            c.error ? (
              <span className="text-danger" role="alert">
                {c.error}
              </span>
            ) : last ? (
              `Last checked ${last}`
            ) : (
              "Not checked yet"
            )
          }
          control={
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={c.error ? "destructive" : c.enabled ? "success" : "secondary"}>
                {c.error ? "Needs attention" : c.enabled ? "On" : "Off"}
              </Badge>
              <Button
                variant="outline"
                size="sm"
                disabled={applying}
                onClick={() => void applyNow()}
              >
                <RefreshCw size={14} className={applying ? "animate-spin" : ""} /> Apply now
              </Button>
            </div>
          }
        />
      </SettingsSection>
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={save}
        onDiscard={() => {
          reset();
          setError(null);
        }}
      />
    </>
  );
}
