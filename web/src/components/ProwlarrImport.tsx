import { Button, Input, SettingsSection, toast } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { apiFetch, errorMessage } from "../lib/api";
import { SettingsRow } from "./SettingsRow";

type Result = {
  indexers: { id: string; name: string; dailyLimit: number; dailyGrabs: number }[];
  imported: boolean;
};

/** Preview and import the Usenet indexers a Prowlarr instance already manages. */
export function ProwlarrImport({ onImported }: { onImported?: () => void }) {
  const [url, setURL] = useState("");
  const [key, setKey] = useState("");
  const [result, setResult] = useState<Result | null>(null);
  const [busy, setBusy] = useState(false);
  async function run(commit: boolean) {
    setBusy(true);
    try {
      const r = await apiFetch<Result>("/playback/prowlarr", {
        method: "POST",
        body: JSON.stringify({
          config: { url, apiKey: key, dailyLimit: 0, dailyGrabs: 0, priority: 0 },
          import: commit,
        }),
      });
      setResult(r);
      if (commit) {
        setKey("");
        toast.success("Prowlarr indexers imported");
        onImported?.();
      }
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="space-y-4">
      <p className="text-sm text-fg-muted">
        Connect your existing Usenet indexers. Prowlarr manages their service limits.
      </p>
      <SettingsSection title="Prowlarr">
        <SettingsRow
          title="Prowlarr URL"
          control={
            <Input
              aria-label="Prowlarr URL"
              type="url"
              className="sm:w-72"
              placeholder="http://localhost:9696"
              value={url}
              onChange={(e) => {
                setURL(e.target.value);
                setResult(null);
              }}
            />
          }
        />
        <SettingsRow
          title="API key"
          control={
            <Input
              aria-label="Prowlarr API key"
              type="password"
              autoComplete="off"
              className="sm:w-72"
              value={key}
              onChange={(e) => {
                setKey(e.target.value);
                setResult(null);
              }}
            />
          }
        />
      </SettingsSection>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" disabled={busy || !url || !key} onClick={() => void run(false)}>
          Preview indexers
        </Button>
        <Button
          disabled={busy || !result || result.imported || !key}
          onClick={() => void run(true)}
        >
          Import indexers
        </Button>
      </div>
      {result && (
        <ul className="space-y-2 text-sm">
          {result.indexers.map((i) => (
            <li key={i.id}>{i.name} · Automatic limits</li>
          ))}
        </ul>
      )}
    </div>
  );
}
