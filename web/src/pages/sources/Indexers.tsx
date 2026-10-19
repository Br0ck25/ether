import {
  Badge,
  Button,
  EmptyState,
  SettingsSection,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Plus, Search } from "lucide-react";
import { useState } from "react";
import { useApp } from "../../app-context";
import { Meter } from "../../components/charts";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { ListInput, SecretInput, TestButton } from "../../components/fields";
import { Section } from "../../components/layout";
import { SettingsRow } from "../../components/SettingsRow";
import { api, newId } from "../../lib/api";
import { isPlainObject } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import type { Indexer } from "../../lib/types";
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

function blankIndexer(priority: number): Indexer {
  return {
    id: newId("idx"),
    name: "",
    url: "",
    apiKey: "",
    dailyLimit: 0,
    dailyGrabs: 0,
    priority,
    enabled: true,
    movieCategories: null,
    tvCategories: null,
  };
}

function indexerName(ix: Indexer, i: number) {
  return ix.name || ix.url || `Indexer ${i + 1}`;
}

/**
 * Newznab indexers in search order; each indexer is edited in a drawer.
 * `version` changes when indexers were added elsewhere (Prowlarr import).
 */
export function IndexerList({ version = 0 }: { version?: number }) {
  const { dashboard, refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), [version]);
  const [editing, setEditing] = useState<{ index: number; draft: Indexer } | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const { tests, runTest, reset } = useTests();

  const cfg = config.data;
  const indexers = cfg?.indexers ?? [];
  const status = dashboard?.sections?.indexers;
  const live = Array.isArray(status) ? status.filter(isPlainObject) : [];
  const statFor = (ix: Indexer) => live.find((s) => s.indexer === ix.name || s.id === ix.id);

  async function commit(list: Indexer[], message: string) {
    await api.putSection(
      "indexers",
      list.map((ix, priority) => ({ ...ix, priority })),
    );
    toast.success(message);
    await config.reload();
    void refreshDashboard();
  }

  const open = (index: number, draft: Indexer) => {
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
      index < indexers.length
        ? indexers.map((ix, j) => (j === index ? draft : ix))
        : [...indexers, draft];
    setSaving(true);
    setErrors({});
    setError(null);
    try {
      await commit(list, index < indexers.length ? "Indexer saved" : "Indexer added");
      setEditing(null);
    } catch (e) {
      setError(e);
      setErrors(recordErrors(e, `indexers[${index}].`));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!editing) return;
    const { index, draft } = editing;
    const ok = await confirmDialog({
      title: "Remove indexer?",
      message: `${indexerName(draft, index)} will no longer be searched.`,
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    setSaving(true);
    setError(null);
    try {
      await commit(
        indexers.filter((_, j) => j !== index),
        "Indexer removed",
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
      await commit(swap(indexers, i, i + d), "Indexer order saved");
    } catch (e) {
      toastError("Couldn't reorder indexers", e);
    }
  }

  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!cfg) return <LoadingRows rows={3} />;

  const draft = editing?.draft;
  const set = (patch: Partial<Indexer>) =>
    setEditing((e) => (e ? { ...e, draft: { ...e.draft, ...patch } } : e));
  const isNew = editing ? editing.index >= indexers.length : false;
  const stat = draft ? statFor(draft) : undefined;
  const used = Number(stat?.hitsUsed ?? 0);

  return (
    <>
      <Section
        title="Indexers"
        description="Searched in order. Ether handles service limits automatically."
        actions={
          <Button size="sm" onClick={() => open(indexers.length, blankIndexer(indexers.length))}>
            <Plus size={14} /> Add indexer
          </Button>
        }
        bodyClassName=""
      >
        {indexers.length === 0 ? (
          <EmptyState
            icon={<Search />}
            title="No indexers"
            description="Add a Newznab indexer so Ether can find releases."
          />
        ) : (
          <ul className="divide-y divide-border">
            {indexers.map((ix, i) => {
              const s = statFor(ix);
              const label = indexerName(ix, i);
              const hits = Number(s?.hitsUsed ?? 0);
              return (
                <RecordItem
                  key={ix.id}
                  title={label}
                  description={[
                    i === 0 ? "Searched first" : `Priority ${i + 1}`,
                    ix.dailyLimit > 0
                      ? s
                        ? `${hits} of ${ix.dailyLimit} API requests used today`
                        : `${ix.dailyLimit} API requests a day`
                      : "Automatic limits",
                    ix.dailyGrabs > 0 ? `${ix.dailyGrabs} NZB downloads a day` : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                  meta={
                    <>
                      {!ix.enabled ? <Badge variant="secondary">Off</Badge> : null}
                      {s && Number(s.retryAt ?? 0) > Date.now() / 1000 ? (
                        <Badge variant="warning">Waiting for service · retries automatically</Badge>
                      ) : s &&
                        ((ix.dailyLimit > 0 && hits >= ix.dailyLimit) ||
                          (ix.dailyGrabs > 0 && Number(s.grabsUsed ?? 0) >= ix.dailyGrabs)) ? (
                        <Badge variant="warning">Personal cap reached</Badge>
                      ) : null}
                    </>
                  }
                  order={
                    <OrderButtons
                      label={label}
                      i={i}
                      n={indexers.length}
                      onMove={(d) => void move(i, d)}
                    />
                  }
                  editLabel={`Edit ${label}`}
                  onEdit={() => open(i, ix)}
                />
              );
            })}
          </ul>
        )}
      </Section>

      <RecordDrawer
        open={editing !== null}
        onClose={close}
        title={isNew ? "New indexer" : draft && editing ? indexerName(draft, editing.index) : ""}
        description="Newznab indexer"
        onSave={() => void save()}
        saving={saving}
        saveLabel="Save indexer"
        onRemove={isNew ? undefined : () => void remove()}
        removeLabel="Remove indexer"
        error={error}
        tools={
          draft ? (
            <TestButton
              state={tests[draft.id] ?? { status: "idle" }}
              onTest={() => runTest(draft.id, () => api.testIndexer(draft))}
              disabled={!draft.url}
            />
          ) : null
        }
      >
        {draft ? (
          <>
            {stat && draft.dailyLimit > 0 ? (
              <Meter
                value={used / draft.dailyLimit}
                label={`${used} API requests used today`}
                valueLabel={`${Number(stat.hitsRemaining ?? 0)} left of ${draft.dailyLimit}`}
                tone={
                  used >= draft.dailyLimit
                    ? "danger"
                    : used / draft.dailyLimit > 0.8
                      ? "warning"
                      : "primary"
                }
              />
            ) : null}
            <SettingsSection title="Connection">
              <TextRow label="Name" value={draft.name} onChange={(v) => set({ name: v })} />
              <TextRow
                label="URL"
                type="url"
                value={draft.url}
                placeholder="https://indexer.example.com"
                help="/api is added automatically."
                error={errors.url}
                onChange={(v) => set({ url: v })}
              />
              <SettingsRow stacked title="API key">
                <SecretInput
                  ariaLabel="API key"
                  value={draft.apiKey}
                  onChange={(v) => set({ apiKey: v })}
                />
              </SettingsRow>
              <BoolRow
                label="Enabled"
                value={draft.enabled}
                onChange={(v) => set({ enabled: v })}
              />
            </SettingsSection>
            <details className="space-y-3">
              <summary className="cursor-pointer text-sm font-medium">
                Advanced: personal caps
              </summary>
              <SettingsSection
                title="Optional daily caps"
                description="Ether follows service limit responses automatically. Add a lower personal cap only if you want one."
              >
                <NumRow
                  label="API requests per day"
                  value={draft.dailyLimit}
                  min={0}
                  help="0 = automatic. Includes searches and capability checks."
                  error={errors.dailyLimit}
                  onChange={(n) => set({ dailyLimit: n })}
                />
                <NumRow
                  label="NZB downloads per day"
                  value={draft.dailyGrabs}
                  min={0}
                  help="0 = automatic. Counts NZB files, not movie data."
                  error={errors.dailyGrabs}
                  onChange={(n) => set({ dailyGrabs: n })}
                />
              </SettingsSection>
            </details>
            <SettingsSection title="Categories">
              <SettingsRow
                stacked
                title="Movie categories"
                description="Newznab category ids. Empty uses the standard movie categories."
              >
                <ListInput
                  numeric
                  ariaLabel="Movie categories"
                  value={draft.movieCategories}
                  onChange={(v) => set({ movieCategories: v.length ? (v as number[]) : null })}
                  placeholder="2000, 2040"
                />
              </SettingsRow>
              <SettingsRow
                stacked
                title="TV categories"
                description="Empty uses the standard TV categories."
              >
                <ListInput
                  numeric
                  ariaLabel="TV categories"
                  value={draft.tvCategories}
                  onChange={(v) => set({ tvCategories: v.length ? (v as number[]) : null })}
                  placeholder="5000, 5040"
                />
              </SettingsRow>
            </SettingsSection>
          </>
        ) : null}
      </RecordDrawer>
    </>
  );
}
