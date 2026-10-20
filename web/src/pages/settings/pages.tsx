import { toast } from "@braedonsaunders/appkit-ui";
import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import { useApp } from "../../app-context";
import { ConfigSection } from "../../components/ConfigSection";
import { ErrorPanel } from "../../components/feedback";
import { DomainPage } from "../../components/layout";
import { RemoteDiagnostics } from "../../components/RemoteDiagnostics";
import { api } from "../../lib/api";
import { humanizeKey, isPlainObject } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import { BackupsAndImport } from "./Backups";
import { ClusterOverview, useClustered } from "./Cluster";
import { ConnectedServices } from "./ConnectedServices";
import { inferField } from "./SchemaForm";
import * as S from "./schema";
import { PlexStatus, UpdatesStatus } from "./sections";

/**
 * config.json sections with a home somewhere in the UI. Any other object the
 * server reports appears on General so new options are never unreachable.
 */
const KNOWN_KEYS = new Set([
  "server",
  "plex",
  "usenet",
  "indexers",
  "debrid",
  "cache",
  "catalog",
  "resolver",
  "quality",
  "transcode",
  "trakt",
  "openSubtitles",
  "llm",
  "updates",
  "spike",
  "markers",
  // Node-local storage, chosen at startup; the admin API shows it redacted
  // and never edits it, so it gets no generic form on General.
  "database",
  "setupComplete",
]);

type Obj = Record<string, unknown>;

/** Server name, port, limits and logging, plus any config section this UI has no page for. */
export function GeneralPage() {
  const { reloadBoot } = useApp();
  const clustered = useClustered();
  const config = useAsync(() => api.getConfig(), []);
  const cfg = config.data as Obj | null | undefined;
  const extras = useMemo(
    () => Object.keys(cfg ?? {}).filter((k) => !KNOWN_KEYS.has(k) && isPlainObject(cfg?.[k])),
    [cfg],
  );
  return (
    <DomainPage
      title="General"
      description="Server name, port, stream limits and logging."
      base="/server"
    >
      <ConfigSection
        configKey="server"
        endpoint="server"
        groups={clustered ? S.clusterServerGroups() : S.SERVER}
        // Node weight only matters in a cluster.
        omit={clustered ? undefined : ["clusterWeight"]}
        label="General"
        onSaved={() => void reloadBoot()}
      />
      {config.error ? (
        <ErrorPanel error={config.error} onRetry={() => void config.reload()} />
      ) : null}
      {extras.map((key) => {
        const value = cfg?.[key] as Obj;
        return (
          <ConfigSection
            key={key}
            configKey={key}
            label={humanizeKey(key)}
            groups={[
              {
                title: humanizeKey(key),
                fields: Object.keys(value).map((k) => inferField(k, value[k])),
              },
            ]}
            onSaved={() => void config.reload()}
          />
        );
      })}
    </DomainPage>
  );
}

export function RemoteAccessPage() {
  const navigate = useNavigate();
  const { dashboard, reloadBoot } = useApp();
  const reopenSetup = async () => {
    try {
      await api.setupReopen();
      await reloadBoot();
      navigate("/setup");
    } catch (e) {
      toast.error("Couldn't reopen setup", { description: (e as Error).message });
    }
  };
  return (
    <DomainPage
      title="Plex & remote access"
      description="Your Plex account, remote access and secure connections."
      base="/remote-access"
    >
      <ConfigSection
        configKey="plex"
        endpoint="plex"
        groups={S.PLEX}
        label="Plex & remote access"
        before={(_, c) => (
          <PlexStatus cfg={c} remote={dashboard?.sections?.remote} onReopen={reopenSetup} />
        )}
        after={() => <RemoteDiagnostics />}
      />
    </DomainPage>
  );
}

export function ServicesPage() {
  return (
    <DomainPage
      title="Connected services"
      description="Trakt, a language model, OpenSubtitles, and where every other integration lives."
      base="/services"
    >
      <ConnectedServices />
    </DomainPage>
  );
}

export function BackupsPage() {
  return (
    <DomainPage
      title="Backups & import"
      description="Snapshots of your settings and data, and imports from Plex, Trakt and Letterboxd."
      base="/backups"
    >
      <BackupsAndImport />
    </DomainPage>
  );
}

export function UpdatesPage() {
  return (
    <DomainPage
      title="Updates"
      description="Release channel, maintenance window and automatic updates."
      base="/updates"
    >
      <ConfigSection
        configKey="updates"
        endpoint="updates"
        groups={S.UPDATES}
        label="Updates"
        before={() => <UpdatesStatus />}
      />
    </DomainPage>
  );
}

export function ClusterPage() {
  return (
    <DomainPage
      title="Cluster"
      description="Ether servers sharing one database and appearing in Plex as one server."
      base="/cluster"
    >
      <ClusterOverview />
    </DomainPage>
  );
}

export function TroubleshootingPage() {
  return (
    <DomainPage
      title="Troubleshooting"
      description="Tools for tracking down playback problems in a Plex app."
      base="/troubleshooting"
    >
      <ConfigSection configKey="spike" endpoint="spike" groups={S.SPIKE} label="Troubleshooting" />
    </DomainPage>
  );
}
