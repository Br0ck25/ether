import { Badge, Button, SettingsSection } from "@braedonsaunders/appkit-ui";
import { LevelBadge } from "../../components/status";
import { SettingsRow } from "../../components/SettingsRow";
import { isPlainObject } from "../../lib/format";
import { plexDirectHealth, remoteHealth } from "../../lib/health";
import type { EtherConfig } from "../../lib/types";

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
