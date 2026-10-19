import { SettingsRow } from "../../components/SettingsRow";
import { Drawer } from "../../components/Drawer";
import { useViewers } from "../../components/pickers";
import { EditPanel } from "../../components/EditPanel";
import {
  Avatar,
  Badge,
  Button,
  EmptyState,
  Input,
  PagedTable,
  SearchSelect,
  SettingsSection,
  Switch,
  toast,
} from "@braedonsaunders/appkit-ui";
import { ArrowDown, ArrowUp, Plus, Trash2, Users } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Meter } from "../../components/charts";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import {
  ChipGroup,
  ListInput,
  NumberInput,
  SecretInput,
  TestButton,
  type TestState,
} from "../../components/fields";
import { LevelBadge } from "../../components/status";
import { UserLimitsForm } from "../../components/UserLimits";
import { api, newId } from "../../lib/api";
import { formatBytes, isPlainObject, relativeTime } from "../../lib/format";
import { indexersHealth, plexDirectHealth, remoteHealth, usenetHealth } from "../../lib/health";
import { useAsync } from "../../lib/hooks";
import type {
  DiskInfo,
  EtherConfig,
  EtherUser,
  Indexer,
  QualityProfile,
  QualityRule,
  UpdateStatus,
  UsenetProvider,
} from "../../lib/types";
import { QUALITY_AUDIO, QUALITY_CODECS, QUALITY_SOURCES } from "./schema";

export type Errors = Record<string, string>;
export type RunTest = (
  key: string,
  fn: () => Promise<{ ok: boolean; error?: string; durationMs: number }>,
) => void;

function swap<T>(list: T[], i: number, j: number): T[] {
  if (j < 0 || j >= list.length) return list;
  const next = [...list];
  [next[i], next[j]] = [next[j] as T, next[i] as T];
  return next;
}

function TextRow({
  label,
  value,
  onChange,
  error,
  placeholder,
  help,
  type,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  placeholder?: string;
  help?: ReactNode;
  type?: string;
}) {
  return (
    <SettingsRow
      title={label}
      description={
        <>
          {help}
          {error ? (
            <span className="block text-xs text-danger" role="alert">
              {error}
            </span>
          ) : null}
        </>
      }
      control={
        <Input
          aria-label={label}
          aria-invalid={error ? true : undefined}
          type={type}
          value={value}
          placeholder={placeholder}
          onChange={(e) => onChange(e.currentTarget.value)}
          className="sm:w-72"
          spellCheck={false}
        />
      }
    />
  );
}
function NumRow({
  label,
  value,
  onChange,
  error,
  help,
  min,
  max,
  unit,
}: {
  label: string;
  value: number;
  onChange: (n: number) => void;
  error?: string;
  help?: ReactNode;
  min?: number;
  max?: number;
  unit?: string;
}) {
  return (
    <SettingsRow
      title={label}
      description={
        <>
          {help}
          {error ? (
            <span className="block text-xs text-danger" role="alert">
              {error}
            </span>
          ) : null}
        </>
      }
      control={
        <span className="flex items-center gap-2">
          <NumberInput
            aria-label={label}
            value={value}
            min={min}
            max={max}
            step={1}
            className="w-28"
            onChange={(n) => onChange(Math.round(n))}
          />
          {unit ? <span className="text-sm text-fg-muted">{unit}</span> : null}
        </span>
      }
    />
  );
}
function BoolRow({
  label,
  value,
  onChange,
  help,
}: {
  label: string;
  value: boolean;
  onChange: (v: boolean) => void;
  help?: ReactNode;
}) {
  return (
    <SettingsRow
      title={label}
      description={help}
      control={
        <Switch
          checked={value}
          onChange={(e) => onChange(e.currentTarget.checked)}
          aria-label={label}
        />
      }
    />
  );
}

function OrderButtons({
  label,
  i,
  n,
  onMove,
}: {
  label: string;
  i: number;
  n: number;
  onMove: (d: -1 | 1) => void;
}) {
  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        className="size-8"
        onClick={() => onMove(-1)}
        disabled={i === 0}
        aria-label={`Move ${label} up`}
      >
        <ArrowUp size={14} />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        className="size-8"
        onClick={() => onMove(1)}
        disabled={i === n - 1}
        aria-label={`Move ${label} down`}
      >
        <ArrowDown size={14} />
      </Button>
    </>
  );
}

// ---- Usenet -------------------------------------------------------------------------

export function UsenetSection({
  value,
  onChange,
  errors,
  tests,
  runTest,
  status,
  footer,
  onDiscard,
  onCommit,
}: {
  value: EtherConfig["usenet"];
  onChange: (v: EtherConfig["usenet"]) => void;
  errors: Errors;
  tests: Record<string, TestState>;
  runTest: RunTest;
  status: unknown;
  footer: ReactNode;
  onDiscard?: () => void;
  onCommit: (value: EtherConfig["usenet"]) => Promise<void>;
}) {
  const providers = value.providers ?? [];
  const live = Array.isArray(status) ? status.filter(isPlainObject) : [];
  const set = (list: UsenetProvider[]) =>
    onChange({ ...value, providers: list.map((p, i) => ({ ...p, priority: i })) });
  const health = usenetHealth(status);
  return (
    <>
      <SettingsSection title="Status">
        <SettingsRow
          title="Providers"
          description={health.summary}
          control={<LevelBadge level={health.level} />}
        />
      </SettingsSection>
      {providers.length === 0 ? (
        <EmptyState
          title="No providers"
          description="Add your Usenet provider. Block accounts can be added as backfill."
        />
      ) : null}
      {providers.map((p, i) => {
        const f = `usenet.providers[${i}]`;
        const stat = live.find((s) => s.id === p.id);
        const name = p.name || p.host || `Provider ${i + 1}`;
        return (
          <EditPanel
            onDiscard={onDiscard}
            key={p.id}
            initialOpen={!p.host}
            title={name}
            description={
              i === 0
                ? "Primary provider"
                : p.backfill
                  ? "Backfill provider"
                  : "Additional provider"
            }
            action="Edit provider"
            footer={<div className="flex justify-end gap-2">{footer}</div>}
          >
            <SettingsSection
              key={p.id || i}
              title={name}
              description={
                i === 0
                  ? "Tried first"
                  : p.backfill
                    ? "Backfill: only for articles missing elsewhere"
                    : `Priority ${i + 1}`
              }
              footer={
                <>
                  <TestButton
                    state={tests[p.id] ?? { status: "idle" }}
                    onTest={() => runTest(p.id, () => api.testUsenet(p))}
                    disabled={!p.host}
                  />
                  <span className="flex-1" />
                  <OrderButtons
                    label={name}
                    i={i}
                    n={providers.length}
                    onMove={(d) => set(swap(providers, i, i + d))}
                  />
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-danger"
                    onClick={() =>
                      void onCommit({
                        ...value,
                        providers: providers
                          .filter((_, j) => j !== i)
                          .map((p, priority) => ({ ...p, priority })),
                      })
                    }
                  >
                    <Trash2 size={14} /> Remove
                  </Button>
                </>
              }
            >
              {stat ? (
                <SettingsRow
                  title="Live"
                  description={`${Number(stat.activeConnections ?? 0)} of ${Number(stat.configuredConnections ?? p.connections)} connections busy${stat.bytes ? ` · ${formatBytes(stat.bytes)} fetched` : ""}`}
                  control={
                    <Badge variant={stat.state === "ok" ? "success" : "warning"}>
                      {String(stat.state ?? "unknown")}
                    </Badge>
                  }
                />
              ) : null}
              <TextRow
                label="Name"
                value={p.name}
                onChange={(v) => set(providers.map((x, j) => (j === i ? { ...x, name: v } : x)))}
              />
              <TextRow
                label="Host"
                value={p.host}
                placeholder="news.example.com"
                error={errors[`${f}.host`]}
                onChange={(v) => set(providers.map((x, j) => (j === i ? { ...x, host: v } : x)))}
              />
              <NumRow
                label="Port"
                value={p.port}
                min={1}
                max={65535}
                error={errors[`${f}.port`]}
                onChange={(n) => set(providers.map((x, j) => (j === i ? { ...x, port: n } : x)))}
              />
              <BoolRow
                label="SSL / TLS"
                value={p.tls}
                onChange={(v) =>
                  set(
                    providers.map((x, j) =>
                      j === i
                        ? {
                            ...x,
                            tls: v,
                            port: v && x.port === 119 ? 563 : !v && x.port === 563 ? 119 : x.port,
                          }
                        : x,
                    ),
                  )
                }
              />
              <TextRow
                label="Username"
                value={p.username}
                onChange={(v) =>
                  set(providers.map((x, j) => (j === i ? { ...x, username: v } : x)))
                }
              />
              <SettingsRow stacked title="Password">
                <SecretInput
                  ariaLabel={`${name} password`}
                  value={p.password}
                  onChange={(v) =>
                    set(providers.map((x, j) => (j === i ? { ...x, password: v } : x)))
                  }
                />
              </SettingsRow>
              <NumRow
                label="Connections"
                value={p.connections}
                min={0}
                max={500}
                error={errors[`${f}.connections`]}
                help="Your plan's connection limit."
                onChange={(n) =>
                  set(providers.map((x, j) => (j === i ? { ...x, connections: n } : x)))
                }
              />
              <BoolRow
                label="Backfill only"
                value={p.backfill}
                help="Use for block accounts: only consulted when an article is missing elsewhere."
                onChange={(v) =>
                  set(providers.map((x, j) => (j === i ? { ...x, backfill: v } : x)))
                }
              />
              <BoolRow
                label="Enabled"
                value={p.enabled}
                onChange={(v) => set(providers.map((x, j) => (j === i ? { ...x, enabled: v } : x)))}
              />
            </SettingsSection>
          </EditPanel>
        );
      })}
      <div className="flex items-center justify-between gap-2">
        <Button
          variant="outline"
          onClick={() =>
            set([
              ...providers,
              {
                id: newId("prov"),
                name: "",
                host: "",
                port: 563,
                tls: true,
                username: "",
                password: "",
                connections: 20,
                priority: providers.length,
                backfill: false,
                enabled: true,
              },
            ])
          }
        >
          <Plus size={15} /> Add provider
        </Button>
      </div>
    </>
  );
}

// ---- Indexers --------------------------------------------------------------------------

export function IndexersSection({
  value,
  onChange,
  errors,
  tests,
  runTest,
  status,
  footer,
  onDiscard,
  onCommit,
}: {
  value: Indexer[];
  onChange: (v: Indexer[]) => void;
  errors: Errors;
  tests: Record<string, TestState>;
  runTest: RunTest;
  status: unknown;
  footer: ReactNode;
  onDiscard?: () => void;
  onCommit: (value: EtherConfig["indexers"]) => Promise<void>;
}) {
  const live = Array.isArray(status) ? status.filter(isPlainObject) : [];
  const set = (list: Indexer[]) => onChange(list.map((x, i) => ({ ...x, priority: i })));
  const health = indexersHealth(status);
  return (
    <>
      <SettingsSection title="Today">
        <SettingsRow
          title="Budgets"
          description={health.summary}
          control={<LevelBadge level={health.level} />}
        />
      </SettingsSection>
      {value.length === 0 ? (
        <EmptyState
          title="No indexers"
          description="Add a Newznab indexer so Ether can find releases."
        />
      ) : null}
      {value.map((ix, i) => {
        const f = `indexers[${i}]`;
        const name = ix.name || ix.url || `Indexer ${i + 1}`;
        const stat = live.find((s) => s.indexer === ix.name || s.id === ix.id);
        const used = Number(stat?.hitsUsed ?? 0);
        return (
          <EditPanel
            onDiscard={onDiscard}
            key={ix.id}
            initialOpen={!ix.url}
            title={name}
            description={"Newznab indexer"}
            action="Edit indexer"
            footer={<div className="flex justify-end gap-2">{footer}</div>}
          >
            <SettingsSection
              key={ix.id || i}
              title={name}
              description={i === 0 ? "Searched first" : `Priority ${i + 1}`}
              footer={
                <>
                  <TestButton
                    state={tests[ix.id] ?? { status: "idle" }}
                    onTest={() => runTest(ix.id, () => api.testIndexer(ix))}
                    disabled={!ix.url}
                  />
                  <span className="flex-1" />
                  <OrderButtons
                    label={name}
                    i={i}
                    n={value.length}
                    onMove={(d) => set(swap(value, i, i + d))}
                  />
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-danger"
                    onClick={() =>
                      void onCommit(
                        value
                          .filter((_, j) => j !== i)
                          .map((ix, priority) => ({ ...ix, priority })),
                      )
                    }
                  >
                    <Trash2 size={14} /> Remove
                  </Button>
                </>
              }
            >
              {stat && ix.dailyLimit > 0 ? (
                <SettingsRow stacked title="Searches today">
                  <Meter
                    value={used / ix.dailyLimit}
                    label={`${used} used`}
                    valueLabel={`${Number(stat.hitsRemaining ?? 0)} left of ${ix.dailyLimit}`}
                    tone={
                      used >= ix.dailyLimit
                        ? "danger"
                        : used / ix.dailyLimit > 0.8
                          ? "warning"
                          : "primary"
                    }
                  />
                </SettingsRow>
              ) : null}
              <TextRow
                label="Name"
                value={ix.name}
                onChange={(v) => set(value.map((x, j) => (j === i ? { ...x, name: v } : x)))}
              />
              <TextRow
                label="URL"
                type="url"
                value={ix.url}
                placeholder="https://indexer.example.com"
                help="/api is added automatically."
                error={errors[`${f}.url`]}
                onChange={(v) => set(value.map((x, j) => (j === i ? { ...x, url: v } : x)))}
              />
              <SettingsRow stacked title="API key">
                <SecretInput
                  ariaLabel={`${name} API key`}
                  value={ix.apiKey}
                  onChange={(v) => set(value.map((x, j) => (j === i ? { ...x, apiKey: v } : x)))}
                />
              </SettingsRow>
              <NumRow
                label="Daily searches"
                value={ix.dailyLimit}
                min={0}
                help="Your plan's API limit. 0 = unlimited."
                error={errors[`${f}.dailyLimit`]}
                onChange={(n) => set(value.map((x, j) => (j === i ? { ...x, dailyLimit: n } : x)))}
              />
              <NumRow
                label="Daily downloads"
                value={ix.dailyGrabs}
                min={0}
                help="NZB downloads allowed per day. 0 = unlimited."
                error={errors[`${f}.dailyGrabs`]}
                onChange={(n) => set(value.map((x, j) => (j === i ? { ...x, dailyGrabs: n } : x)))}
              />
              <SettingsRow
                stacked
                title="Movie categories"
                description="Newznab category ids. Empty uses the standard movie categories."
              >
                <ListInput
                  numeric
                  ariaLabel={`${name} movie categories`}
                  value={ix.movieCategories}
                  onChange={(v) =>
                    set(
                      value.map((x, j) =>
                        j === i ? { ...x, movieCategories: v.length ? (v as number[]) : null } : x,
                      ),
                    )
                  }
                  placeholder="2000, 2040"
                />
              </SettingsRow>
              <SettingsRow stacked title="TV categories">
                <ListInput
                  numeric
                  ariaLabel={`${name} TV categories`}
                  value={ix.tvCategories}
                  onChange={(v) =>
                    set(
                      value.map((x, j) =>
                        j === i ? { ...x, tvCategories: v.length ? (v as number[]) : null } : x,
                      ),
                    )
                  }
                  placeholder="5000, 5040"
                />
              </SettingsRow>
              <BoolRow
                label="Enabled"
                value={ix.enabled}
                onChange={(v) => set(value.map((x, j) => (j === i ? { ...x, enabled: v } : x)))}
              />
            </SettingsSection>
          </EditPanel>
        );
      })}
      <div className="flex items-center justify-between gap-2">
        <Button
          variant="outline"
          onClick={() =>
            set([
              ...value,
              {
                id: newId("idx"),
                name: "",
                url: "",
                apiKey: "",
                dailyLimit: 0,
                dailyGrabs: 0,
                priority: value.length,
                enabled: true,
                movieCategories: null,
                tvCategories: null,
              },
            ])
          }
        >
          <Plus size={15} /> Add indexer
        </Button>
      </div>
    </>
  );
}

// ---- Quality ----------------------------------------------------------------------------

function blankProfile(n: number): QualityProfile {
  return {
    name: `Profile ${n}`,
    maxHeight: 1080,
    minHeight: 720,
    allowHdr: false,
    allowDolbyVision: false,
    preferredSources: ["webdl", "bluray"],
    preferredCodecs: ["h264", "hevc"],
    preferredAudio: ["eac3", "ac3", "aac"],
    minSizeMbPerMin: 5,
    maxSizeMbPerMin: 150,
    preferredLanguages: null,
    allowCam: false,
  };
}

const HEIGHTS = [
  { value: "0", label: "Any" },
  { value: "480", label: "480p" },
  { value: "720", label: "720p" },
  { value: "1080", label: "1080p" },
  { value: "2160", label: "4K (2160p)" },
];

/** Ordered multi-pick: chips toggle membership; the order picked is the preference order. */
function PreferenceList({
  label,
  all,
  value,
  onChange,
}: {
  label: string;
  all: string[];
  value: string[] | null;
  onChange: (v: string[]) => void;
}) {
  const v = value ?? [];
  return (
    <div className="space-y-1.5">
      <ChipGroup
        ariaLabel={label}
        size="sm"
        value={v}
        onChange={(next) =>
          onChange(next.filter((x) => v.includes(x)).concat(next.filter((x) => !v.includes(x))))
        }
        options={[...all, ...v.filter((x) => !all.includes(x))].map((x) => ({
          value: x,
          label: x,
        }))}
      />
      <p className="text-xs text-fg-muted">
        {v.length ? `Preference: ${v.join(" › ")}` : "No preference"}
      </p>
    </div>
  );
}

export function QualitySection({
  value,
  onChange,
  errors,
  footer,
  onDiscard,
  onCommit,
  view = "profiles",
}: {
  value: EtherConfig["quality"];
  onChange: (v: EtherConfig["quality"]) => void;
  errors: Errors;
  footer: ReactNode;
  onDiscard?: () => void;
  onCommit: (value: EtherConfig["quality"]) => Promise<void>;
  view?: "profiles" | "rules" | "defaults";
}) {
  const { viewers } = useViewers();
  const profiles = value.profiles ?? [];
  const rules = value.rules ?? [];
  const names = profiles.map((p) => ({ value: p.name, label: p.name }));
  const setProfiles = (list: QualityProfile[]) => onChange({ ...value, profiles: list });
  const setRules = (list: QualityRule[]) => onChange({ ...value, rules: list });
  const patch = (i: number, p: Partial<QualityProfile>) => {
    const old = profiles[i];
    const next = profiles.map((x, j) => (j === i ? { ...x, ...p } : x));
    // Keep references valid when a profile is renamed.
    if (old && p.name !== undefined && p.name !== old.name) {
      onChange({
        ...value,
        profiles: next,
        default: value.default === old.name ? p.name : value.default,
        remote: value.remote === old.name ? p.name : value.remote,
        rules: rules.map((r) => (r.profile === old.name ? { ...r, profile: p.name! } : r)),
      });
    } else setProfiles(next);
  };
  return (
    <>
      {view === "defaults" && (
        <EditPanel
          onDiscard={onDiscard}
          title="Default quality"
          description="The profile used on your network and when watching remotely."
          action="Edit defaults"
          summary={`${value.default || "Not selected"} locally · ${value.remote || value.default || "Not selected"} remotely`}
          footer={footer}
        >
          <SettingsSection
            title="Defaults"
            description="Which profile a client gets when no rule matches."
          >
            <SettingsRow
              title="On your network"
              description={errors["quality.default"]}
              control={
                <div className="w-48">
                  <SearchSelect
                    value={value.default}
                    onChange={(v) => onChange({ ...value, default: v })}
                    options={names}
                    ariaLabel="Default profile"
                    searchable={false}
                  />
                </div>
              }
            />
            <SettingsRow
              title="Remote"
              description={errors["quality.remote"]}
              control={
                <div className="w-48">
                  <SearchSelect
                    value={value.remote}
                    onChange={(v) => onChange({ ...value, remote: v })}
                    options={names}
                    ariaLabel="Remote profile"
                    searchable={false}
                  />
                </div>
              }
            />
          </SettingsSection>
        </EditPanel>
      )}

      {view === "rules" && (
        <EditPanel
          onDiscard={onDiscard}
          title="Client rules"
          description="The first matching rule chooses a quality profile. Match a device, platform or named viewer."
          action="Edit client rules"
          summary={
            <div className="space-y-1">
              {rules.length
                ? rules.map((r, i) => (
                    <p key={i}>
                      {i + 1}. {r.product || "Any device"}
                      {r.platform ? ` · ${r.platform}` : ""}
                      {r.userId
                        ? ` · ${viewers?.find((v) => v.id === r.userId)?.title || viewers?.find((v) => v.id === r.userId)?.username || "Unavailable viewer"}`
                        : ""}{" "}
                      → {r.profile}
                    </p>
                  ))
                : "No rules. All devices follow the defaults."}
            </div>
          }
          footer={footer}
        >
          <SettingsSection
            title="Rules"
            description="The first matching rule picks the profile. Match on the Plex product (e.g. “Plex for Apple TV”), platform, or a viewer."
          >
            {rules.length === 0 ? (
              <SettingsRow title="No rules" description="Every client uses the defaults above." />
            ) : null}
            {rules.map((r, i) => (
              <div
                key={i}
                className="flex flex-wrap items-end gap-2 px-5 py-3"
                role="group"
                aria-label={`Quality rule ${i + 1}`}
              >
                <div className="min-w-48 flex-1 space-y-2 text-xs text-fg-muted">
                  Device
                  <SearchSelect
                    value={
                      ![
                        "",
                        "Plex for Apple TV",
                        "Plex Web",
                        "Plex for iOS",
                        "Plex for Android",
                        "Roku",
                        "Fire TV",
                        "LG",
                        "Samsung",
                        "Xbox",
                        "PlayStation",
                      ].includes(r.product ?? "")
                        ? "__custom__"
                        : (r.product ?? "")
                    }
                    onChange={(v) =>
                      setRules(
                        rules.map((x, j) =>
                          j === i
                            ? {
                                ...x,
                                product: v === "__custom__" ? "Custom device" : v || undefined,
                              }
                            : x,
                        ),
                      )
                    }
                    options={[
                      { value: "", label: "Any device" },
                      ...[
                        "Plex for Apple TV",
                        "Plex Web",
                        "Plex for iOS",
                        "Plex for Android",
                        "Roku",
                        "Fire TV",
                        "LG",
                        "Samsung",
                        "Xbox",
                        "PlayStation",
                      ].map((v) => ({ value: v, label: v })),
                      { value: "__custom__", label: "Custom product name…" },
                    ]}
                    ariaLabel={`Rule ${i + 1} device`}
                  />
                  {r.product &&
                    ![
                      "Plex for Apple TV",
                      "Plex Web",
                      "Plex for iOS",
                      "Plex for Android",
                      "Roku",
                      "Fire TV",
                      "LG",
                      "Samsung",
                      "Xbox",
                      "PlayStation",
                    ].includes(r.product) && (
                      <Input
                        value={r.product}
                        onChange={(e) =>
                          setRules(
                            rules.map((x, j) =>
                              j === i ? { ...x, product: e.currentTarget.value } : x,
                            ),
                          )
                        }
                        aria-label={`Rule ${i + 1} custom product`}
                        placeholder="Product name contains…"
                      />
                    )}
                </div>
                <label className="min-w-28 flex-1 text-xs text-fg-muted">
                  Platform
                  <Input
                    value={r.platform ?? ""}
                    placeholder="Any"
                    onChange={(e) =>
                      setRules(
                        rules.map((x, j) =>
                          j === i ? { ...x, platform: e.currentTarget.value || undefined } : x,
                        ),
                      )
                    }
                    aria-label={`Rule ${i + 1} platform`}
                    className="mt-1"
                  />
                </label>
                <div className="min-w-40 flex-1 text-xs text-fg-muted">
                  Viewer
                  <div className="mt-1">
                    <SearchSelect
                      value={String(r.userId ?? 0)}
                      onChange={(v) =>
                        setRules(
                          rules.map((x, j) =>
                            j === i ? { ...x, userId: Number(v) || undefined } : x,
                          ),
                        )
                      }
                      options={[
                        { value: "0", label: "Everyone" },
                        ...(viewers ?? []).map((v) => ({
                          value: String(v.id),
                          label: v.title || v.username,
                        })),
                      ]}
                      ariaLabel={`Rule ${i + 1} viewer`}
                    />
                  </div>
                </div>
                <div className="w-40 text-xs text-fg-muted">
                  Profile
                  <div className="mt-1">
                    <SearchSelect
                      value={r.profile}
                      onChange={(v) =>
                        setRules(rules.map((x, j) => (j === i ? { ...x, profile: v } : x)))
                      }
                      options={names}
                      ariaLabel={`Rule ${i + 1} profile`}
                      searchable={false}
                    />
                  </div>
                </div>
                <OrderButtons
                  label={`rule ${i + 1}`}
                  i={i}
                  n={rules.length}
                  onMove={(d) => setRules(swap(rules, i, i + d))}
                />
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-8 text-danger"
                  onClick={() => setRules(rules.filter((_, j) => j !== i))}
                  aria-label={`Remove rule ${i + 1}`}
                >
                  <Trash2 size={14} />
                </Button>
                {errors[`quality.rules[${i}].profile`] ? (
                  <p className="w-full text-xs text-danger">
                    {errors[`quality.rules[${i}].profile`]}
                  </p>
                ) : null}
              </div>
            ))}
            <div className="px-5 py-3">
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  setRules([...rules, { profile: value.default || profiles[0]?.name || "" }])
                }
                disabled={!profiles.length}
              >
                <Plus size={14} /> Add rule
              </Button>
            </div>
          </SettingsSection>
        </EditPanel>
      )}

      {view === "profiles" &&
        profiles.map((p, i) => {
          const f = `quality.profiles[${i}]`;
          return (
            <EditPanel
              onDiscard={onDiscard}
              key={i}
              initialOpen={p.name.startsWith("Profile ")}
              title={p.name || `Profile ${i + 1}`}
              description="Resolution, HDR, source and language preferences for matching clients."
              action="Edit profile"
              summary={`${p.minHeight || "Any"}–${p.maxHeight || "Any"}p · ${p.allowHdr ? "HDR allowed" : "SDR"}`}
              footer={footer}
            >
              <SettingsSection
                key={i}
                title={p.name || `Profile ${i + 1}`}
                description="How releases are ranked for clients using this profile."
                footer={
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-danger"
                    onClick={() =>
                      void onCommit({ ...value, profiles: profiles.filter((_, j) => j !== i) })
                    }
                    disabled={profiles.length <= 1}
                  >
                    <Trash2 size={14} /> Remove profile
                  </Button>
                }
              >
                <TextRow
                  label="Name"
                  value={p.name}
                  error={errors[`${f}.name`]}
                  onChange={(v) => patch(i, { name: v })}
                />
                <SettingsRow
                  title="Resolution"
                  description={
                    errors[`${f}.minHeight`] ? (
                      <span className="text-danger">{errors[`${f}.minHeight`]}</span>
                    ) : (
                      "Lowest and highest acceptable."
                    )
                  }
                  control={
                    <span className="flex items-center gap-2">
                      <span className="w-32">
                        <SearchSelect
                          value={String(p.minHeight)}
                          onChange={(v) => patch(i, { minHeight: Number(v) })}
                          options={HEIGHTS}
                          ariaLabel={`${p.name} minimum resolution`}
                          searchable={false}
                        />
                      </span>
                      <span className="text-sm text-fg-muted">to</span>
                      <span className="w-32">
                        <SearchSelect
                          value={String(p.maxHeight)}
                          onChange={(v) => patch(i, { maxHeight: Number(v) })}
                          options={HEIGHTS}
                          ariaLabel={`${p.name} maximum resolution`}
                          searchable={false}
                        />
                      </span>
                    </span>
                  }
                />
                <BoolRow
                  label="Allow HDR"
                  value={p.allowHdr}
                  help="Only used when the client supports it."
                  onChange={(v) => patch(i, { allowHdr: v })}
                />
                <BoolRow
                  label="Allow Dolby Vision"
                  value={p.allowDolbyVision}
                  onChange={(v) => patch(i, { allowDolbyVision: v })}
                />
                <BoolRow
                  label="Allow CAM releases"
                  value={p.allowCam}
                  onChange={(v) => patch(i, { allowCam: v })}
                />
                <SettingsRow stacked title="Sources">
                  <PreferenceList
                    label={`${p.name} sources`}
                    all={QUALITY_SOURCES}
                    value={p.preferredSources}
                    onChange={(v) => patch(i, { preferredSources: v })}
                  />
                </SettingsRow>
                <SettingsRow stacked title="Video codecs">
                  <PreferenceList
                    label={`${p.name} codecs`}
                    all={QUALITY_CODECS}
                    value={p.preferredCodecs}
                    onChange={(v) => patch(i, { preferredCodecs: v })}
                  />
                </SettingsRow>
                <SettingsRow stacked title="Audio">
                  <PreferenceList
                    label={`${p.name} audio`}
                    all={QUALITY_AUDIO}
                    value={p.preferredAudio}
                    onChange={(v) => patch(i, { preferredAudio: v })}
                  />
                </SettingsRow>
                <SettingsRow
                  stacked
                  title="Languages"
                  description="ISO codes in preference order. Empty accepts any."
                >
                  <ListInput
                    ariaLabel={`${p.name} languages`}
                    value={p.preferredLanguages}
                    onChange={(v) =>
                      patch(i, { preferredLanguages: v.length ? v.map(String) : null })
                    }
                    placeholder="en, fr"
                  />
                </SettingsRow>
                <SettingsRow
                  title="Size per minute"
                  description={
                    errors[`${f}.minSizeMbPerMin`] ? (
                      <span className="text-danger">{errors[`${f}.minSizeMbPerMin`]}</span>
                    ) : (
                      "Sanity bounds in MB per minute of runtime; filters fakes and bloated files."
                    )
                  }
                  control={
                    <span className="flex items-center gap-2">
                      <NumberInput
                        aria-label={`${p.name} minimum MB per minute`}
                        value={p.minSizeMbPerMin}
                        min={0}
                        className="w-20"
                        onChange={(n) => patch(i, { minSizeMbPerMin: n })}
                      />
                      <span className="text-sm text-fg-muted">to</span>
                      <NumberInput
                        aria-label={`${p.name} maximum MB per minute`}
                        value={p.maxSizeMbPerMin}
                        min={0}
                        className="w-20"
                        onChange={(n) => patch(i, { maxSizeMbPerMin: n })}
                      />
                      <span className="text-sm text-fg-muted">MB</span>
                    </span>
                  }
                />
              </SettingsSection>
            </EditPanel>
          );
        })}
      <div className="flex items-center justify-between gap-2">
        {view === "profiles" && (
          <Button
            variant="outline"
            onClick={() => setProfiles([...profiles, blankProfile(profiles.length + 1)])}
          >
            <Plus size={15} /> Add profile
          </Button>
        )}
      </div>
    </>
  );
}

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

export function CacheStatus({
  dir,
  budget,
  status,
}: {
  dir: string;
  budget: number;
  status: unknown;
}) {
  const disk = useAsync<DiskInfo>(() => api.disk(dir), [dir]);
  const used = isPlainObject(status) ? Number(status.used ?? 0) : 0;
  return (
    <SettingsSection title="Usage">
      <SettingsRow
        stacked
        title="Cache"
        description={`${formatBytes(used)} of ${formatBytes(budget)} budget in use.`}
      >
        <Meter value={budget > 0 ? used / budget : 0} />
      </SettingsRow>
      <SettingsRow
        stacked
        title="Disk"
        description={
          disk.error
            ? "Couldn't read free space for that folder."
            : disk.data
              ? `${formatBytes(disk.data.freeBytes)} free of ${formatBytes(disk.data.totalBytes)} on ${disk.data.probedPath || dir}.`
              : "Checking free space…"
        }
      >
        {disk.data ? (
          <Meter
            value={disk.data.totalBytes > 0 ? budget / disk.data.totalBytes : 0}
            label="Budget as a share of the disk"
            tone={disk.data.freeBytes < budget - used ? "warning" : "primary"}
          />
        ) : null}
        {disk.data && disk.data.freeBytes < budget - used ? (
          <p className="mt-1 text-xs text-warning">
            The budget is larger than the free space left on this disk.
          </p>
        ) : null}
      </SettingsRow>
    </SettingsSection>
  );
}

export function TranscodeStatus({ status }: { status: unknown }) {
  const s = isPlainObject(status) ? status : {};
  const hw = isPlainObject(s.hwaccel) ? s.hwaccel : {};
  const encoders = Array.isArray(hw.encoders) ? hw.encoders.map(String) : [];
  return (
    <SettingsSection title="Detected">
      {typeof s.error === "string" ? (
        <SettingsRow
          title="ffmpeg"
          description={s.error}
          control={<LevelBadge level="error" label="Not found" />}
        />
      ) : (
        <>
          <SettingsRow
            title="ffmpeg"
            description={s.ffmpeg ? `Version ${String(s.ffmpeg)}` : "Not reported"}
            control={
              <LevelBadge
                level={s.ffmpeg ? "ok" : "unknown"}
                label={s.ffmpeg ? "Ready" : "Unknown"}
              />
            }
          />
          <SettingsRow
            title="Hardware acceleration"
            description={encoders.length ? encoders.join(", ") : "Software encoding"}
            control={
              <Badge variant={hw.name && hw.name !== "none" ? "success" : "secondary"}>
                {String(hw.name ?? "none")}
              </Badge>
            }
          />
        </>
      )}
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

// ---- Users -------------------------------------------------------------------------------

export function UsersSection({ defaultStreamLimit }: { defaultStreamLimit: number }) {
  const users = useAsync(() => api.users(), []);
  const [open, setOpen] = useState<EtherUser | null>(null);
  useEffect(() => {
    if (open && users.data) setOpen(users.data.find((u) => u.id === open.id) ?? null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [users.data]);
  if (users.error)
    return (
      <ErrorPanel
        error={users.error}
        feature="User administration"
        onRetry={() => void users.reload()}
      />
    );
  if (!users.data) return <LoadingRows rows={3} />;
  return (
    <>
      <PagedTable<EtherUser>
        rows={users.data}
        rowKey={(u) => String(u.id)}
        searchable
        pageSize={10}
        onRowClick={setOpen}
        empty={
          <EmptyState
            icon={<Users />}
            title="No users yet"
            description="Share this server from plex.tv to add people."
          />
        }
        columns={[
          {
            key: "name",
            header: "User",
            search: (u) => `${u.title} ${u.username} ${u.email ?? ""}`,
            sortValue: (u) => (u.title || u.username).toLowerCase(),
            cell: (u) => (
              <span className="flex items-center gap-3">
                <Avatar src={u.thumb} name={u.title || u.username} size={30} />
                <span>
                  <span className="block font-medium text-fg">
                    {u.title || u.username} {u.isOwner ? <Badge>Owner</Badge> : null}
                  </span>
                  <span className="block text-xs text-fg-muted">{u.email || u.username}</span>
                </span>
              </span>
            ),
          },
          {
            key: "streams",
            header: "Streams",
            align: "right",
            sortValue: (u) => u.streamLimit,
            cell: (u) => (
              <span className="tabular-nums">
                {u.streamLimit || `Default (${defaultStreamLimit})`}
              </span>
            ),
          },
          {
            key: "rating",
            header: "Rating cap",
            sortValue: (u) => u.maxContentRating,
            cell: (u) =>
              u.maxContentRating ? (
                <Badge variant="warning">{u.maxContentRating}</Badge>
              ) : (
                <span className="text-fg-muted">None</span>
              ),
          },
        ]}
      />
      <Drawer
        open={open !== null}
        onClose={() => setOpen(null)}
        title={open ? open.title || open.username : ""}
        description={open?.email}
        size="lg"
      >
        {open ? (
          <UserLimitsForm
            user={open}
            defaultStreamLimit={defaultStreamLimit}
            onSaved={(saved) =>
              users.setData((list) => (list ?? []).map((u) => (u.id === saved.id ? saved : u)))
            }
          />
        ) : null}
      </Drawer>
    </>
  );
}
