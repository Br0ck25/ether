import {
  SettingsSection,
  SettingsShell,
  Spinner,
  toast,
  type SettingsNavGroup,
} from "@braedonsaunders/appkit-ui";
import { ArchiveRestore, ArrowDownToLine, LifeBuoy, Plug, Puzzle, Server, Tv } from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { Link, Navigate, useNavigate, useParams } from "react-router-dom";
import { useApp } from "../../app-context";
import { ConfigSection } from "../../components/ConfigSection";
import { ErrorPanel } from "../../components/feedback";
import { RemoteDiagnostics } from "../../components/RemoteDiagnostics";
import { SettingsRow } from "../../components/SettingsRow";
import { api } from "../../lib/api";
import { humanizeKey, isPlainObject } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import { BackupsAndImport } from "./Backups";
import { ConnectedServices } from "./ConnectedServices";
import { inferField } from "./SchemaForm";
import * as S from "./schema";
import { PlexStatus } from "./sections";

interface SectionDef {
  id: string;
  label: string;
  description: string;
  icon: ReactNode;
}

const SECTIONS: SectionDef[] = [
  {
    id: "server",
    label: "General",
    description: "Server name, port, stream limits and logging.",
    icon: <Server />,
  },
  {
    id: "plex",
    label: "Plex & remote access",
    description: "Your Plex account, remote access and secure connections.",
    icon: <Tv />,
  },
  {
    id: "services",
    label: "Connected services",
    description: "Trakt, a language model, OpenSubtitles, and where every other integration lives.",
    icon: <Plug />,
  },
  {
    id: "backups",
    label: "Backups & import",
    description:
      "Snapshots of your settings and data, and imports from Plex, Trakt and Letterboxd.",
    icon: <ArchiveRestore />,
  },
  {
    id: "updates",
    label: "Updates",
    description: "Release channel and automatic updates.",
    icon: <ArrowDownToLine />,
  },
  {
    id: "troubleshooting",
    label: "Troubleshooting",
    description: "Tools for tracking down playback problems in a Plex app.",
    icon: <LifeBuoy />,
  },
];

const GROUPS: { label: string; ids: string[] }[] = [
  { label: "Server", ids: ["server", "plex"] },
  { label: "Integrations", ids: ["services"] },
  { label: "Maintenance", ids: ["backups", "updates", "troubleshooting"] },
];

/**
 * config.json sections with a home somewhere in the UI. Any other object the
 * server reports appears under "More" so new options are never unreachable.
 */
const KNOWN_KEYS = new Set([
  "server",
  "plex",
  "usenet",
  "indexers",
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
  "setupComplete",
]);

type Obj = Record<string, unknown>;

export default function Settings() {
  const { section: param } = useParams();
  const navigate = useNavigate();
  const { boot, dashboard, reloadBoot } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const cfg = config.data as Obj | null | undefined;

  const extras = useMemo<(SectionDef & { key: string })[]>(
    () =>
      Object.keys(cfg ?? {})
        .filter((k) => !KNOWN_KEYS.has(k) && isPlainObject(cfg?.[k]))
        .map((k) => ({
          id: `x-${k}`,
          key: k,
          label: humanizeKey(k),
          description: `${humanizeKey(k)} options this server supports.`,
          icon: <Puzzle />,
        })),
    [cfg],
  );

  const nav: SettingsNavGroup[] = [
    ...GROUPS.map((g) => ({
      label: g.label,
      items: g.ids
        .map((id) => SECTIONS.find((s) => s.id === id)!)
        .map((s) => ({ key: s.id, label: s.label, icon: s.icon })),
    })),
    ...(extras.length
      ? [{ label: "More", items: extras.map((s) => ({ key: s.id, label: s.label, icon: s.icon })) }]
      : []),
  ];

  const known = SECTIONS.find((s) => s.id === param);
  const extra = extras.find((s) => s.id === param);
  const section = known ?? extra;

  const reopenSetup = async () => {
    try {
      await api.setupReopen();
      await reloadBoot();
      navigate("/setup");
    } catch (e) {
      toast.error("Couldn't reopen setup", { description: (e as Error).message });
    }
  };

  let body: ReactNode;
  if (!section) {
    // Extra sections are only known once the config has loaded.
    if (param?.startsWith("x-") && !cfg && !config.error)
      body = (
        <div className="py-10 text-center">
          <Spinner size={20} label="Loading settings" />
        </div>
      );
    else if (param?.startsWith("x-") && config.error)
      body = <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
    else return <Navigate to="/settings/server" replace />;
  } else if (extra) {
    const value = cfg?.[extra.key] as Obj;
    body = (
      <ConfigSection
        key={extra.key}
        configKey={extra.key}
        label={extra.label}
        groups={[
          { title: "Options", fields: Object.keys(value).map((k) => inferField(k, value[k])) },
        ]}
        onSaved={() => void config.reload()}
      />
    );
  } else {
    switch (section.id) {
      case "server":
        body = (
          <ConfigSection
            configKey="server"
            endpoint="server"
            groups={S.SERVER}
            label="General"
            onSaved={() => void reloadBoot()}
          />
        );
        break;
      case "plex":
        body = (
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
        );
        break;
      case "services":
        body = <ConnectedServices />;
        break;
      case "backups":
        body = <BackupsAndImport />;
        break;
      case "updates":
        body = (
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
        );
        break;
      case "troubleshooting":
        body = (
          <ConfigSection
            configKey="spike"
            endpoint="spike"
            groups={S.SPIKE}
            label="Troubleshooting"
          />
        );
        break;
    }
  }

  const active = section ?? { label: "", description: "", id: param ?? "" };
  return (
    <div className="h-full">
      <SettingsShell
        title="Settings"
        description="How Ether runs. Viewers never see any of this."
        nav={nav}
        activeKey={active.id}
        onSelect={(k) => navigate(`/settings/${k}`)}
        contentWidth={active.id === "backups" ? "wide" : "narrow"}
        linkRender={({ href, children, className }) => (
          <Link to={href} className={className}>
            {children}
          </Link>
        )}
      >
        {section ? (
          <div>
            <h2 className="text-lg font-semibold text-fg">{section.label}</h2>
            <p className="text-sm text-fg-muted">{section.description}</p>
          </div>
        ) : null}
        {body}
      </SettingsShell>
    </div>
  );
}
