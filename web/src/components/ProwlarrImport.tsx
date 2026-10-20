import { Badge, Button, Input, SettingsSection, toast } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { apiFetch, errorMessage } from "../lib/api";
import { SettingsRow } from "./SettingsRow";

type Result = {
  indexers: {
    id: string;
    name: string;
    dailyLimit: number;
    dailyGrabs: number;
    /** "torrent" for Torznab indexers; empty or "usenet" for Newznab. */
    protocol?: string;
  }[];
  imported: boolean;
};

/** Preview and import the Usenet and torrent indexers a Prowlarr instance already manages. */
export function ProwlarrImport({ onImported }: { onImported?: () => void }) {
  const [url, setURL] = useState("");
  const [key, setKey] = useState("");
  const [limit, setLimit] = useState(100);
  const [grabs, setGrabs] = useState(30);
  const [result, setResult] = useState<Result | null>(null);
  const [busy, setBusy] = useState(false);
  async function run(commit: boolean) {
    setBusy(true);
    try {
      const r = await apiFetch<Result>("/playback/prowlarr", {
        method: "POST",
        body: JSON.stringify({
          config: { url, apiKey: key, dailyLimit: limit, dailyGrabs: grabs, priority: 0 },
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
        Bring in the Usenet (Newznab) and torrent (Torznab) indexers a Prowlarr instance manages.
        Ether applies its own search and download budgets. Torrent indexers stream through a debrid
        account. You don't need Prowlarr: built-in sites and Newznab/Torznab URLs can be added
        directly.
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
        <SettingsRow
          title="Daily searches per indexer"
          description="0 = unlimited."
          control={
            <Input
              aria-label="Daily searches per indexer"
              type="number"
              min={0}
              className="w-28"
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
            />
          }
        />
        <SettingsRow
          title="Daily downloads per indexer"
          description="0 = unlimited."
          control={
            <Input
              aria-label="Daily downloads per indexer"
              type="number"
              min={0}
              className="w-28"
              value={grabs}
              onChange={(e) => setGrabs(Number(e.target.value))}
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
            <li key={i.id} className="flex flex-wrap items-center gap-2">
              <Badge variant="outline">{i.protocol === "torrent" ? "Torznab" : "Newznab"}</Badge>
              <span>
                {i.name} · {i.dailyLimit || "unlimited"} searches · {i.dailyGrabs || "unlimited"}{" "}
                downloads
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
