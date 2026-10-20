import { Badge, Button, SettingsSection, toast } from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { LevelBadge } from "../../components/status";
import { SettingsRow } from "../../components/SettingsRow";
import { api } from "../../lib/api";
import { isPlainObject, relativeTime } from "../../lib/format";
import { plexDirectHealth, remoteHealth } from "../../lib/health";
import { useAsync } from "../../lib/hooks";
import type { EtherConfig, UpdateStatus } from "../../lib/types";

/**
 * Settings helpers shared with the Settings page. Usenet providers and indexers
 * now live in pages/sources, quality in pages/releases, cache and transcoding
 * status in pages/playback.
 */

export type Errors = Record<string, string>;
export type RunTest = (
  key: string,
  fn: () => Promise<{ ok: boolean; error?: string; durationMs: number }>,
) => void;

// ---- Status cards -----------------------------------------------------------------------

export function PlexStatus({
  cfg,
  remote,
  onReopen,
}: {
  cfg: EtherConfig;
  remote: unknown;
  onReopen: () => void;
}) {
  const r = remoteHealth(remote);
  const c = plexDirectHealth(remote);
  const conns =
    isPlainObject(remote) && Array.isArray(remote.connections)
      ? remote.connections.filter(isPlainObject)
      : [];
  return (
    <SettingsSection title="Status">
      <SettingsRow
        title="Plex account"
        description={
          cfg.plex.claimed && cfg.plex.token
            ? `Claimed by ${cfg.plex.ownerUsername || "your account"}.`
            : "Not signed in to plex.tv. Run the setup wizard to sign in and claim this server."
        }
        control={
          <Button variant="outline" size="sm" onClick={onReopen}>
            Reopen setup wizard
          </Button>
        }
      />
      <SettingsRow
        title="Remote access"
        description={r.details.join(" · ") || r.summary}
        control={<LevelBadge level={r.level} label={r.summary} />}
      />
      <SettingsRow
        title="plex.direct certificate"
        description={c.details.join(" · ") || c.summary}
        control={<LevelBadge level={c.level} label={c.summary} />}
      />
      {conns.length ? (
        <SettingsRow stacked title="Published addresses">
          <ul className="space-y-1 text-xs">
            {conns.map((x, i) => (
              <li key={i} className="flex items-center gap-2">
                <Badge variant={x.local ? "secondary" : "info"}>
                  {x.local ? "Local" : "Remote"}
                </Badge>
                <code className="truncate text-fg-muted">{String(x.uri ?? "")}</code>
              </li>
            ))}
          </ul>
        </SettingsRow>
      ) : null}
    </SettingsSection>
  );
}

// ---- Updates -----------------------------------------------------------------------------

const UPDATE_STATE: Record<UpdateStatus["state"], string> = {
  idle: "Idle",
  downloading: "Downloading",
  staged: "Downloaded",
  "waiting-for-window": "Waiting for maintenance window",
  "waiting-for-idle": "Waiting until nobody is watching",
  applying: "Installing — Ether will restart",
  "rolled-back": "Rolled back",
};

export function UpdatesStatus() {
  const status = useAsync(() => api.updateStatus(), []);
  const [busy, setBusy] = useState(false);
  const s = status.data;
  useEffect(() => {
    if (!s || !(s.requested || s.state === "downloading" || s.state === "applying")) return;
    const t = setInterval(() => void status.reload(), 5000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [s?.requested, s?.state]);
  if (status.error) return <ErrorPanel error={status.error} onRetry={() => void status.reload()} />;
  if (!s) return <LoadingRows rows={2} />;

  const updateNow = async (force: boolean) => {
    setBusy(true);
    try {
      await api.updateNow(force);
      toast.success(
        force ? "Installing update now" : "Update will install once nobody is watching",
      );
      await status.reload();
    } catch (e) {
      toast.error("Couldn't start the update", { description: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };
  const canApply = s.capability === "self-apply" && s.state !== "applying";
  let availableText = "Ether is up to date";
  if (s.available) availableText = `Version ${s.available.version} — ${UPDATE_STATE[s.state]}`;
  else if (s.state === "rolled-back" && s.failedVersion)
    availableText = `Version ${s.failedVersion} was rolled back and will be skipped`;
  else if (!s.enabled) availableText = s.disabledReason || "Updates are disabled in this build";

  return (
    <SettingsSection title="Status">
      <SettingsRow
        title="Installed"
        description={
          s.confirming ? `Version ${s.current} — confirming the update` : `Version ${s.current}`
        }
        control={
          s.state === "rolled-back" ? (
            <LevelBadge level="warn" label="Rolled back" />
          ) : (
            <LevelBadge level={s.lastError ? "warn" : "ok"} label={s.lastError ? "Issue" : "OK"} />
          )
        }
      />
      <SettingsRow
        title="Available"
        description={
          <>
            {availableText}
            {s.available?.notesUrl ? (
              <>
                {" · "}
                <a
                  className="underline"
                  href={s.available.notesUrl}
                  target="_blank"
                  rel="noreferrer"
                >
                  Release notes
                </a>
              </>
            ) : null}
            {s.capability === "notify-only" && s.capabilityReason ? (
              <span className="block">Install manually: {s.capabilityReason}.</span>
            ) : null}
          </>
        }
        control={
          s.available && canApply ? (
            s.requested && s.state === "waiting-for-idle" ? (
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => void updateNow(true)}
              >
                Install now anyway
              </Button>
            ) : (
              <Button
                size="sm"
                disabled={busy || s.requested}
                onClick={() => void updateNow(false)}
              >
                Update now
              </Button>
            )
          ) : null
        }
      />
      <SettingsRow
        title="Maintenance window"
        description={`${s.window.start}–${s.window.end} local time${
          s.window.open ? " (open now)" : ""
        } · ${s.activeSessions} active stream${s.activeSessions === 1 ? "" : "s"}`}
      />
      <SettingsRow
        title="Last check"
        description={
          s.lastError
            ? s.lastError
            : s.lastCheck
              ? relativeTime(s.lastCheck)
              : s.enabled
                ? "Not checked yet"
                : "Never"
        }
      />
    </SettingsSection>
  );
}
