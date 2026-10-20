import { Badge, buttonVariants, SettingsSection, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useApp } from "../../app-context";
import { ErrorPanel } from "../../components/feedback";
import { TestButton, type TestState } from "../../components/fields";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { LevelBadge } from "../../components/status";
import { api, apiFetch, ApiError, type TestFn } from "../../lib/api";
import { useAsync } from "../../lib/hooks";
import type { EtherConfig } from "../../lib/types";
import { LanguageFeatures } from "./LanguageFeatures";
import { asObj, GenericFields, SchemaField, type FieldGroup } from "./SchemaForm";
import * as S from "./schema";

type Obj = Record<string, unknown>;

interface Service {
  configKey: "trakt" | "llm" | "openSubtitles";
  endpoint: string;
  label: string;
  groups: FieldGroup[];
  testLabel: string;
  test: (value: Obj) => ReturnType<TestFn>;
}

const SERVICES: Service[] = [
  {
    configKey: "trakt",
    endpoint: "trakt",
    label: "Trakt",
    groups: S.TRAKT,
    testLabel: "Test Trakt",
    test: (v) => api.testTrakt(v as EtherConfig["trakt"]),
  },
  {
    configKey: "llm",
    endpoint: "llm",
    label: "Language model",
    groups: S.LLM,
    testLabel: "Test endpoint",
    test: (v) => api.testLLM(v as EtherConfig["llm"]),
  },
  {
    configKey: "openSubtitles",
    endpoint: "opensubtitles",
    label: "OpenSubtitles",
    groups: S.OPENSUBTITLES,
    testLabel: "Test OpenSubtitles",
    test: (v) => api.testOpenSubtitles(v as EtherConfig["openSubtitles"]),
  },
];

/**
 * Connected services: the credentials Ether owns (Trakt, language
 * model, OpenSubtitles) as one in-place form with one save bar, plus a directory
 * of the integrations configured on their own pages.
 */
export function ConnectedServices() {
  const { refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const cfg = config.data;
  const stored: Record<string, Obj> | null = cfg
    ? Object.fromEntries(
        SERVICES.map((s) => [s.configKey, asObj((cfg as unknown as Obj)[s.configKey])]),
      )
    : null;
  const { draft, setDraft, dirty, reset } = useDraft<Record<string, Obj>>(stored);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [tests, setTests] = useState<Record<string, TestState>>({});

  const runTest = useCallback((s: Service, value: Obj) => {
    setTests((t) => ({ ...t, [s.configKey]: { status: "running" } }));
    s.test(value).then(
      (r) =>
        setTests((t) => ({
          ...t,
          [s.configKey]: r.ok
            ? { status: "ok", durationMs: r.durationMs }
            : { status: "fail", message: r.error ?? "failed" },
        })),
      (e) =>
        setTests((t) => ({
          ...t,
          [s.configKey]: { status: "fail", message: (e as Error).message },
        })),
    );
  }, []);

  const save = async () => {
    if (!draft || !stored) return;
    const changed = SERVICES.filter(
      (s) => JSON.stringify(draft[s.configKey]) !== JSON.stringify(stored[s.configKey]),
    );
    setSaving(true);
    setError(null);
    setErrors({});
    try {
      // Each service saves through its own endpoint so the server validates it alone.
      for (const s of changed) await api.putSection(s.endpoint, draft[s.configKey]);
      toast.success("Settings saved", { description: changed.map((s) => s.label).join(", ") });
      await config.reload();
      void refreshDashboard();
    } catch (e) {
      setError(e);
      if (e instanceof ApiError && e.fields)
        setErrors(Object.fromEntries(e.fields.map((f) => [f.field, f.message])));
      // Sections saved before the failure stay in the draft; saving again is harmless.
    } finally {
      setSaving(false);
    }
  };

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!cfg || !draft)
    return (
      <div className="py-10 text-center">
        <Spinner size={20} label="Loading settings" />
      </div>
    );

  return (
    <>
      {SERVICES.map((s) => {
        const value = draft[s.configKey] ?? {};
        const set = (next: Obj) => setDraft({ ...draft, [s.configKey]: next });
        const known = new Set(s.groups.flatMap((g) => g.fields.map((f) => f.key)));
        const extra = Object.keys(value).filter((k) => !known.has(k));
        return (
          <div key={s.configKey} className="space-y-4">
            {s.groups.map((g, i) => {
              const last = i === s.groups.length - 1;
              return (
                <SettingsSection
                  key={g.title ?? i}
                  title={g.title ?? s.label}
                  description={g.description}
                  footer={
                    last ? (
                      <TestButton
                        label={s.testLabel}
                        state={tests[s.configKey] ?? { status: "idle" }}
                        onTest={() => runTest(s, value)}
                      />
                    ) : undefined
                  }
                >
                  {g.fields.map((f) => (
                    <SchemaField
                      key={f.key}
                      def={f}
                      value={value[f.key]}
                      path={`${s.configKey}.${f.key}`}
                      error={errors[`${s.configKey}.${f.key}`]}
                      onChange={(v) => set({ ...value, [f.key]: v })}
                    />
                  ))}
                  {last && extra.length ? (
                    <GenericFields
                      value={value}
                      keys={extra}
                      onChange={set}
                      errors={errors}
                      prefix={s.configKey}
                    />
                  ) : null}
                </SettingsSection>
              );
            })}
            {s.configKey === "llm" ? <LanguageFeatures /> : null}
          </div>
        );
      })}
      <OtherIntegrations cfg={cfg} />
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

const linkClass = buttonVariants({ variant: "outline", size: "sm" });

/** Where every other integration is configured, with its live status. */
function OtherIntegrations({ cfg }: { cfg: EtherConfig }) {
  const { health } = useApp();
  const [ratings, setRatings] = useState<{ enabled?: boolean; provider?: string } | null>(null);
  useEffect(() => {
    let live = true;
    apiFetch<{ enabled?: boolean; provider?: string }>("/discovery/ratings/settings").then(
      (r) => {
        if (live) setRatings(r);
      },
      () => {
        /* status is optional here; the Ratings page reports errors */
      },
    );
    return () => {
      live = false;
    };
  }, []);
  const catalog = health.find((h) => h.id === "catalog");
  const remote = health.find((h) => h.id === "remote");
  const markers = asObj((cfg as unknown as Obj).markers);
  const debridAccounts = (cfg.debrid?.providers ?? []).filter((p) => p.enabled && p.apiKey).length;
  const providerName = ratings?.provider === "mdblist" ? "MDBList" : "OMDb";

  const rows: {
    name: string;
    description: string;
    status: ReactNode;
    href: string;
    where: string;
  }[] = [
    {
      name: "TMDB",
      description: "Titles, artwork and details for the whole catalogue.",
      status: catalog ? <LevelBadge level={catalog.level} label={catalog.summary} /> : null,
      href: "/metadata/catalogue",
      where: "Metadata › Catalogue",
    },
    {
      name: "OMDb and MDBList",
      description: "IMDb, Rotten Tomatoes and other rating badges in Plex.",
      status: ratings ? (
        ratings.enabled ? (
          <Badge variant="success">Connected · {providerName}</Badge>
        ) : (
          <Badge variant="secondary">Not connected</Badge>
        )
      ) : null,
      href: "/metadata/ratings",
      where: "Metadata › Ratings",
    },
    {
      name: "TheIntroDB",
      description: "Intro and credits markers so viewers can skip them.",
      status:
        "theIntroDb" in markers ? (
          markers.theIntroDb ? (
            <Badge variant="success">On</Badge>
          ) : (
            <Badge variant="secondary">Off</Badge>
          )
        ) : null,
      href: "/playback/markers",
      where: "Playback › Markers",
    },
    {
      name: "Prowlarr",
      description: "Import the Usenet and torrent indexers you already manage.",
      status: null,
      href: "/sources/indexers",
      where: "Sources › Indexers",
    },
    {
      name: "Debrid",
      description: "Stream torrent releases through your own debrid account.",
      status: debridAccounts ? (
        <Badge variant="success">
          {debridAccounts === 1 ? "1 account" : `${debridAccounts} accounts`}
        </Badge>
      ) : (
        <Badge variant="secondary">Not connected</Badge>
      ),
      href: "/sources/debrid",
      where: "Sources › Debrid",
    },
    {
      name: "Plex",
      description: "Your Plex account, sign-in and remote access.",
      status:
        cfg.plex.claimed && cfg.plex.token ? (
          remote ? (
            <LevelBadge level={remote.level} label={remote.summary} />
          ) : (
            <Badge variant="success">Signed in</Badge>
          )
        ) : (
          <Badge variant="warning">Not signed in</Badge>
        ),
      href: "/remote-access",
      where: "Plex & remote access",
    },
  ];
  return (
    <SettingsSection title="Other integrations" description="Configured where they're used.">
      {rows.map((r) => (
        <SettingsRow
          key={r.name}
          title={r.name}
          description={r.description}
          control={
            <span className="flex flex-wrap items-center justify-end gap-2">
              {r.status}
              <Link to={r.href} className={linkClass}>
                {r.where}
              </Link>
            </span>
          }
        />
      ))}
    </SettingsSection>
  );
}
