import { SettingsSection, toast } from "@braedonsaunders/appkit-ui";
import { useCallback, useMemo } from "react";
import { useNavigate } from "react-router-dom";
import { useApp } from "../../app-context";
import { ConfigSection } from "../../components/ConfigSection";
import { ErrorPanel } from "../../components/feedback";
import { DomainPage, useRouteTab } from "../../components/layout";
import { RemoteDiagnostics } from "../../components/RemoteDiagnostics";
import { SettingsRow } from "../../components/SettingsRow";
import { api } from "../../lib/api";
import { humanizeKey, isPlainObject } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import { BackupsAndImport } from "./Backups";
import { ConnectedServices } from "./ConnectedServices";
import { inferField, type FieldGroup } from "./SchemaForm";
import * as S from "./schema";
import { PlexStatus } from "./sections";

/**
 * config.json sections with a home somewhere in the UI. Any other object the
 * server reports gets its own tab on General so new options are never unreachable.
 */
const KNOWN_KEYS = new Set([
  "server",
  "plex",
  "usenet",
  "indexers",
  "indexerSites",
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
  "downloads",
  "personalize",
  "extras",
  "setupComplete",
]);

type Obj = Record<string, unknown>;

/** Reveals the tab holding the first field the server rejected. */
function useRevealErrors(base: string, tabOf: (key: string) => string | undefined) {
  const navigate = useNavigate();
  return useCallback(
    (keys: string[]) => {
      const tab = keys.map(tabOf).find(Boolean);
      if (tab) navigate(`${base}/${tab}`);
    },
    [base, navigate, tabOf],
  );
}

const serverTab = () => "server";

/** General sections other than the server itself, each on its own tab. */
const GENERAL_SECTIONS = [
  { key: "downloads", label: "Downloads", groups: S.DOWNLOADS },
  { key: "personalize", label: "Personalization", groups: S.PERSONALIZE },
  { key: "extras", label: "Trailers & extras", groups: S.EXTRAS },
] as const;

/**
 * Server name, port, limits and logging, then downloads, personalization and
 * trailers, plus any config section this UI has no page for. Every form stays
 * mounted across tabs, so edits on several tabs save together.
 */
export function GeneralPage() {
  const { reloadBoot } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const cfg = config.data as Obj | null | undefined;
  const extras = useMemo(
    () => Object.keys(cfg ?? {}).filter((k) => !KNOWN_KEYS.has(k) && isPlainObject(cfg?.[k])),
    [cfg],
  );
  const tabs = [
    { key: "server", label: "Server" },
    ...GENERAL_SECTIONS.map((x) => ({ key: x.key, label: x.label })),
    ...extras.map((k) => ({ key: k, label: humanizeKey(k) })),
  ];
  const tab = useRouteTab(tabs);
  const toServer = useRevealErrors("/server", serverTab);
  return (
    <DomainPage
      title="General"
      description="Server name, port and limits, downloads, personalization and trailers."
      base="/server"
      tabs={tabs}
      active={tab}
    >
      <ConfigSection
        configKey="server"
        endpoint="server"
        groups={S.SERVER}
        label="General"
        hidden={tab !== "server"}
        onFieldErrors={toServer}
        onSaved={() => void reloadBoot()}
      />
      {GENERAL_SECTIONS.map((x) => (
        <GeneralSection key={x.key} section={x} hidden={tab !== x.key} />
      ))}
      {config.error ? (
        <ErrorPanel error={config.error} onRetry={() => void config.reload()} />
      ) : null}
      {extras.map((key) => {
        const value = cfg?.[key] as Obj;
        return (
          <GeneralSection
            key={key}
            section={{
              key,
              label: humanizeKey(key),
              groups: [
                {
                  title: humanizeKey(key),
                  fields: Object.keys(value).map((k) => inferField(k, value[k])),
                },
              ],
            }}
            hidden={tab !== key}
            onSaved={() => void config.reload()}
          />
        );
      })}
    </DomainPage>
  );
}

function GeneralSection({
  section,
  hidden,
  onSaved,
}: {
  section: { key: string; label: string; groups: readonly FieldGroup[] };
  hidden: boolean;
  onSaved?: () => void;
}) {
  const tabOf = useCallback(() => section.key, [section.key]);
  const reveal = useRevealErrors("/server", tabOf);
  return (
    <ConfigSection
      configKey={section.key}
      groups={[...section.groups]}
      label={section.label}
      hidden={hidden}
      onFieldErrors={reveal}
      onSaved={onSaved}
    />
  );
}

const PLEX_TABS = [
  { key: "status", label: "Status" },
  { key: "remote", label: "Remote access", group: "Remote access" },
  { key: "secure", label: "Secure connections", group: "Secure connections" },
  { key: "account", label: "Account", group: "Account" },
  { key: "compatibility", label: "Compatibility", group: "Compatibility" },
] as const;

/** Which Plex tab a field lives on; options this UI doesn't know sit under Account. */
function plexTabOf(key: string) {
  const group = S.PLEX.find((g) => g.fields.some((f) => f.key === key))?.title;
  return PLEX_TABS.find((t) => "group" in t && t.group === group)?.key ?? "account";
}

export function RemoteAccessPage() {
  const navigate = useNavigate();
  const { dashboard, reloadBoot } = useApp();
  const tab = useRouteTab(PLEX_TABS);
  const reveal = useRevealErrors("/remote-access", plexTabOf);
  const reopenSetup = async () => {
    try {
      await api.setupReopen();
      await reloadBoot();
      navigate("/setup");
    } catch (e) {
      toast.error("Couldn't reopen setup", { description: (e as Error).message });
    }
  };
  const current = PLEX_TABS.find((t) => t.key === tab)!;
  return (
    <DomainPage
      title="Plex & remote access"
      description="Your Plex account, remote access and secure connections."
      base="/remote-access"
      tabs={PLEX_TABS}
      active={tab}
    >
      <ConfigSection
        configKey="plex"
        endpoint="plex"
        groups={S.PLEX}
        label="Plex & remote access"
        show={(g) => "group" in current && g.title === current.group}
        showAdvanced={tab === "account"}
        onFieldErrors={reveal}
        before={(_, c) =>
          tab === "status" ? (
            <PlexStatus cfg={c} remote={dashboard?.sections?.remote} onReopen={reopenSetup} />
          ) : null
        }
        after={() => (tab === "status" ? <RemoteDiagnostics /> : null)}
      />
    </DomainPage>
  );
}

const SERVICE_TABS = [
  { key: "overview", label: "Overview" },
  { key: "trakt", label: "Trakt" },
  { key: "llm", label: "Language model" },
  { key: "opensubtitles", label: "OpenSubtitles" },
] as const;

export function ServicesPage() {
  const tab = useRouteTab(SERVICE_TABS);
  return (
    <DomainPage
      title="Connected services"
      description="Every integration Ether uses and how it's doing. Trakt, a language model and OpenSubtitles are set up here."
      base="/services"
      tabs={SERVICE_TABS}
      active={tab}
    >
      <ConnectedServices tab={tab} />
    </DomainPage>
  );
}

const BACKUP_TABS = [
  { key: "backups", label: "Backups" },
  { key: "import", label: "Import & export" },
] as const;

export function BackupsPage() {
  const tab = useRouteTab(BACKUP_TABS);
  return (
    <DomainPage
      title="Backups & import"
      description="Snapshots of your settings and data, and imports from Plex, Trakt and Letterboxd."
      base="/backups"
      tabs={BACKUP_TABS}
      active={tab}
    >
      <BackupsAndImport tab={tab} />
    </DomainPage>
  );
}

export function UpdatesPage() {
  const { boot } = useApp();
  return (
    <DomainPage
      title="Updates"
      description="Release channel and automatic updates."
      base="/updates"
    >
      <ConfigSection
        configKey="updates"
        endpoint="updates"
        groups={S.UPDATES}
        label="Updates"
        before={() => (
          <SettingsSection title="This server">
            <SettingsRow
              title="Installed version"
              control={
                <code className="rounded bg-bg-subtle px-2 py-1 text-xs text-fg-muted">
                  {boot.version || "—"}
                </code>
              }
            />
          </SettingsSection>
        )}
      />
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
