import {
  Badge,
  Button,
  EmptyState,
  SettingsSection,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Network, Plus } from "lucide-react";
import { useState } from "react";
import { useApp } from "../../app-context";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { SecretInput, TestButton } from "../../components/fields";
import { Section } from "../../components/layout";
import { SettingsRow } from "../../components/SettingsRow";
import { api, newId } from "../../lib/api";
import { formatBytes, isPlainObject } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import type { EtherConfig, UsenetProvider } from "../../lib/types";
import {
  BoolRow,
  NumRow,
  OrderButtons,
  RecordDrawer,
  RecordItem,
  TextRow,
  recordErrors,
  swap,
  toastError,
  useTests,
  type Errors,
} from "./form";

function blankProvider(priority: number): UsenetProvider {
  return {
    id: newId("prov"),
    name: "",
    host: "",
    port: 563,
    tls: true,
    username: "",
    password: "",
    connections: 20,
    priority,
    backfill: false,
    enabled: true,
  };
}

function providerName(p: UsenetProvider, i: number) {
  return p.name || p.host || `Provider ${i + 1}`;
}

function role(p: UsenetProvider, i: number) {
  return i === 0 ? "Tried first" : p.backfill ? "Backfill" : `Priority ${i + 1}`;
}

/**
 * Connection use for a provider row. In a cluster the account's connections
 * are divided among the nodes (nodeShare), so busy counts are out of this
 * node's share.
 */
export function connectionText(stat: Record<string, unknown> | undefined, configured: number) {
  if (!stat) return [`${configured} connections`];
  const total = Number(stat.configuredConnections ?? configured);
  const share = Number(stat.nodeShare ?? 0);
  const active = Number(stat.activeConnections ?? 0);
  if (share > 0)
    return [
      `${active} of ${share} connections busy`,
      `this node: ${share} of ${total} connections`,
    ];
  return [`${active} of ${total} connections busy`];
}

/** Usenet providers in priority order; each provider is edited in a drawer. */
export function UsenetProviders() {
  const { dashboard, refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const [repairDraft, setRepairDraft] = useState<NonNullable<
    EtherConfig["usenet"]["repair"]
  > | null>(null);
  const [editing, setEditing] = useState<{ index: number; draft: UsenetProvider } | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const { tests, runTest, reset } = useTests();

  const cfg = config.data;
  const providers = cfg?.usenet.providers ?? [];
  const status = dashboard?.sections?.providers;
  const live = Array.isArray(status) ? status.filter(isPlainObject) : [];

  async function commit(list: UsenetProvider[], message: string) {
    if (!cfg) return;
    const usenet: EtherConfig["usenet"] = {
      ...cfg.usenet,
      providers: list.map((p, priority) => ({ ...p, priority })),
    };
    await api.putSection("usenet", usenet);
    toast.success(message);
    await config.reload();
    void refreshDashboard();
  }

  async function saveRepair() {
    if (!cfg || !repairDraft) return;
    setSaving(true);
    setError(null);
    try {
      await api.putSection("usenet", { ...cfg.usenet, repair: repairDraft });
      toast.success("Repair settings saved");
      setRepairDraft(null);
      await config.reload();
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }
  const open = (index: number, draft: UsenetProvider) => {
    setErrors({});
    setError(null);
    reset(draft.id);
    setEditing({ index, draft: structuredClone(draft) });
  };
  const close = () => {
    if (!saving) setEditing(null);
  };

  async function save() {
    if (!editing) return;
    const { index, draft } = editing;
    const list =
      index < providers.length
        ? providers.map((p, j) => (j === index ? draft : p))
        : [...providers, draft];
    setSaving(true);
    setErrors({});
    setError(null);
    try {
      await commit(list, index < providers.length ? "Provider saved" : "Provider added");
      setEditing(null);
    } catch (e) {
      setError(e);
      setErrors(recordErrors(e, `usenet.providers[${index}].`));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!editing) return;
    const { index, draft } = editing;
    const ok = await confirmDialog({
      title: "Remove provider?",
      message: `${providerName(draft, index)} will no longer be used for streaming.`,
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    setSaving(true);
    setError(null);
    try {
      await commit(
        providers.filter((_, j) => j !== index),
        "Provider removed",
      );
      setEditing(null);
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }

  async function move(i: number, d: -1 | 1) {
    try {
      await commit(swap(providers, i, i + d), "Provider order saved");
    } catch (e) {
      toastError("Couldn't reorder providers", e);
    }
  }

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!cfg) return <LoadingRows rows={3} />;

  const draft = editing?.draft;
  const set = (patch: Partial<UsenetProvider>) =>
    setEditing((e) => (e ? { ...e, draft: { ...e.draft, ...patch } } : e));
  const isNew = editing ? editing.index >= providers.length : false;
  const name = draft && editing ? providerName(draft, editing.index) : "";

  return (
    <>
      <Section
        title="Providers"
        description="Tried in order. Backfill providers are only used for articles missing everywhere else."
        actions={
          <Button size="sm" onClick={() => open(providers.length, blankProvider(providers.length))}>
            <Plus size={14} /> Add provider
          </Button>
        }
        bodyClassName=""
      >
        {providers.length === 0 ? (
          <EmptyState
            icon={<Network />}
            title="No providers"
            description="Add your Usenet provider. Block accounts can be added as backfill."
          />
        ) : (
          <ul className="divide-y divide-border">
            {providers.map((p, i) => {
              const stat = live.find((s) => s.id === p.id);
              const label = providerName(p, i);
              return (
                <RecordItem
                  key={p.id}
                  title={label}
                  description={[
                    role(p, i),
                    p.host ? `${p.host}:${p.port}` : "No host",
                    ...connectionText(stat, p.connections),
                    stat?.bytes ? `${formatBytes(stat.bytes)} fetched` : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                  meta={
                    <>
                      {!p.enabled ? <Badge variant="secondary">Off</Badge> : null}
                      {stat ? (
                        <Badge variant={stat.state === "ok" ? "success" : "warning"}>
                          {stat.state === "ok" ? "Connected" : String(stat.state ?? "Unknown")}
                        </Badge>
                      ) : null}
                    </>
                  }
                  order={
                    <OrderButtons
                      label={label}
                      i={i}
                      n={providers.length}
                      onMove={(d) => void move(i, d)}
                    />
                  }
                  editLabel={`Edit ${label}`}
                  onEdit={() => open(i, p)}
                />
              );
            })}
          </ul>
        )}
      </Section>

      <Section
        title="Repair Ahead"
        description="Restore missing source data during readahead when it can finish within your limits."
      >
        <SettingsRow
          title={cfg?.usenet.repair?.enabled ? "Repair enabled" : "Repair disabled"}
          description="Repair uses additional download bandwidth. Playback can still switch sources when repair cannot finish in time."
          control={
            <Button
              variant="outline"
              onClick={() => {
                setError(null);
                setRepairDraft(
                  cfg?.usenet.repair ?? {
                    enabled: false,
                    summaryRows: 4,
                    adaptiveSummaries: true,
                    maxDownloadBytes: 512 * 1024 * 1024,
                    liveWait: "2s",
                    aheadWait: "1m30s",
                  },
                );
              }}
            >
              Configure repair
            </Button>
          }
        />
      </Section>
      <RecordDrawer
        open={repairDraft !== null}
        onClose={() => {
          if (!saving) setRepairDraft(null);
        }}
        title="Repair Ahead"
        onSave={() => void saveRepair()}
        saving={saving}
        saveLabel="Save repair settings"
        error={error}
      >
        {repairDraft ? (
          <SettingsSection title="Recovery">
            <BoolRow
              label="Enable repair"
              value={repairDraft.enabled}
              onChange={(enabled) => setRepairDraft({ ...repairDraft, enabled })}
            />
            <NumRow
              label="Extra download budget"
              value={Math.round((repairDraft.maxDownloadBytes || 512 * 1024 * 1024) / 1024 / 1024)}
              min={32}
              max={4096}
              unit="MiB"
              help="Additional transfer budget per repair attempt; requests already in flight can finish."
              onChange={(n) =>
                setRepairDraft({
                  ...repairDraft,
                  maxDownloadBytes: Math.min(4096, Math.max(32, n)) * 1024 * 1024,
                })
              }
            />
            <BoolRow
              label="Remember downloaded data"
              value={repairDraft.summaryRows > 0}
              help="After damage is detected, retain repair information when its expected benefit justifies the memory and CPU cost."
              onChange={(enabled) =>
                setRepairDraft({
                  ...repairDraft,
                  summaryRows: enabled ? 4 : 0,
                  adaptiveSummaries: true,
                })
              }
            />
          </SettingsSection>
        ) : null}
      </RecordDrawer>

      <RecordDrawer
        open={editing !== null}
        onClose={close}
        title={isNew ? "New provider" : name}
        description={editing && !isNew ? role(editing.draft, editing.index) : undefined}
        onSave={() => void save()}
        saving={saving}
        saveLabel="Save provider"
        onRemove={isNew ? undefined : () => void remove()}
        removeLabel="Remove provider"
        error={error}
        tools={
          draft ? (
            <TestButton
              state={tests[draft.id] ?? { status: "idle" }}
              onTest={() => runTest(draft.id, () => api.testUsenet(draft))}
              disabled={!draft.host}
            />
          ) : null
        }
      >
        {draft ? (
          <SettingsSection title="Connection">
            <TextRow label="Name" value={draft.name} onChange={(v) => set({ name: v })} />
            <TextRow
              label="Host"
              value={draft.host}
              placeholder="news.example.com"
              error={errors.host}
              onChange={(v) => set({ host: v })}
            />
            <NumRow
              label="Port"
              value={draft.port}
              min={1}
              max={65535}
              error={errors.port}
              onChange={(n) => set({ port: n })}
            />
            <BoolRow
              label="SSL / TLS"
              value={draft.tls}
              onChange={(v) =>
                set({
                  tls: v,
                  port: v && draft.port === 119 ? 563 : !v && draft.port === 563 ? 119 : draft.port,
                })
              }
            />
            <TextRow
              label="Username"
              value={draft.username}
              error={errors.username}
              onChange={(v) => set({ username: v })}
            />
            <SettingsRow stacked title="Password">
              <SecretInput
                ariaLabel="Password"
                value={draft.password}
                onChange={(v) => set({ password: v })}
              />
            </SettingsRow>
            <NumRow
              label="Connections"
              value={draft.connections}
              min={0}
              max={500}
              error={errors.connections}
              help="Your plan's connection limit."
              onChange={(n) => set({ connections: n })}
            />
            <BoolRow
              label="Backfill only"
              value={draft.backfill}
              help="Use for block accounts: only consulted when an article is missing elsewhere."
              onChange={(v) => set({ backfill: v })}
            />
            <BoolRow label="Enabled" value={draft.enabled} onChange={(v) => set({ enabled: v })} />
          </SettingsSection>
        ) : null}
      </RecordDrawer>
    </>
  );
}
