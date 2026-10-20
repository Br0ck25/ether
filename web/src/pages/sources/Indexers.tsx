import {
  Alert,
  AlertDescription,
  Badge,
  EmptyState,
  SettingsSection,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Globe, Link2, Plus, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useApp } from "../../app-context";
import { ActionMenu } from "../../components/ActionMenu";
import { Meter } from "../../components/charts";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import {
  ListInput,
  SecretInput,
  Segmented,
  TestButton,
  type TestState,
} from "../../components/fields";
import { Section } from "../../components/layout";
import { SettingsRow } from "../../components/SettingsRow";
import { api, errorMessage, newId } from "../../lib/api";
import { isPlainObject } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import type { Indexer, IndexerProtocol, SiteDefinition, SiteTestResult } from "../../lib/types";
import { hasUsableDebrid } from "./Debrid";
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
import {
  BuiltinSites,
  MissingDefinition,
  SitePicker,
  SiteSettingsFields,
  blankSiteIndexer,
  isBuiltin,
  siteProtocolLabel,
} from "./IndexerSites";

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
    protocol: "usenet",
  };
}

const PROTOCOLS: { value: IndexerProtocol; label: string }[] = [
  { value: "usenet", label: "Usenet (Newznab)" },
  { value: "torrent", label: "Torrent (Torznab)" },
];

export function isTorrent(ix: { protocol?: string }) {
  return ix.protocol === "torrent";
}

/** The indexer API name for a protocol, as shown on badges. */
export function protocolLabel(ix: { protocol?: string }) {
  return isTorrent(ix) ? "Torznab" : "Newznab";
}

function indexerName(ix: Indexer, i: number) {
  return ix.name || ix.url || `Indexer ${i + 1}`;
}

/** A built-in site's test: result count and a few sample titles, or the error. */
function SiteTestResults({ result }: { result: SiteTestResult }) {
  if (!result.ok) return null;
  const titles = result.titles ?? [];
  return (
    <Alert variant="success" role="status">
      <AlertDescription>
        <p>
          Signed in and searched: {result.results} {result.results === 1 ? "result" : "results"}
          {titles.length ? ", for example:" : "."}
        </p>
        {titles.length ? (
          <ul className="mt-1 list-disc pl-5 text-xs">
            {titles.map((t) => (
              <li key={t}>{t}</li>
            ))}
          </ul>
        ) : null}
      </AlertDescription>
    </Alert>
  );
}

/**
 * Indexers in search order: built-in sites and Newznab/Torznab URLs, each
 * edited in a drawer, followed by the built-in sites' definitions and
 * settings edited in place. `version` changes when indexers were added
 * elsewhere (Prowlarr import).
 */
export function IndexerList({ version = 0 }: { version?: number }) {
  const { dashboard, refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), [version]);
  const [sitesVersion, setSitesVersion] = useState(0);
  const sites = useAsync(() => api.sites.definitions(), [sitesVersion]);
  const statuses = useAsync(() => api.sites.status().catch(() => []), [version, sitesVersion]);
  const [editing, setEditing] = useState<{ index: number; draft: Indexer } | null>(null);
  const [picking, setPicking] = useState(false);
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const { tests, runTest, reset } = useTests();
  const [siteTest, setSiteTest] = useState<{ state: TestState; result?: SiteTestResult }>({
    state: { status: "idle" },
  });

  const cfg = config.data;
  const indexers = cfg?.indexers ?? [];
  const definitions = sites.data ?? null;
  const defFor = (ix: Indexer): SiteDefinition | undefined =>
    definitions?.find((d) => d.id === ix.definition);
  const statusFor = (ix: Indexer) => statuses.data?.find((s) => s.indexerId === ix.id);
  const inUse = useMemo(
    () => new Set(indexers.filter(isBuiltin).map((ix) => ix.definition ?? "")),
    [indexers],
  );
  const status = dashboard?.sections?.indexers;
  const live = Array.isArray(status) ? status.filter(isPlainObject) : [];
  const mixed = indexers.some(isTorrent) && indexers.some((ix) => !isTorrent(ix));
  const statFor = (ix: Indexer) => live.find((s) => s.indexer === ix.name || s.id === ix.id);

  async function commit(list: Indexer[], message: string) {
    await api.putSection(
      "indexers",
      list.map((ix, priority) => ({ ...ix, priority })),
    );
    toast.success(message);
    await config.reload();
    void statuses.reload();
    void refreshDashboard();
  }

  const open = (index: number, draft: Indexer) => {
    setErrors({});
    setError(null);
    reset(draft.id);
    setSiteTest({ state: { status: "idle" } });
    setPicking(false);
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

  async function testSite(draft: Indexer) {
    setSiteTest({ state: { status: "running" } });
    try {
      const r = await api.sites.test(draft);
      setSiteTest({
        state: r.ok
          ? { status: "ok", durationMs: r.durationMs }
          : { status: "fail", message: r.error ?? "failed" },
        result: r,
      });
      void statuses.reload();
    } catch (e) {
      setSiteTest({ state: { status: "fail", message: errorMessage(e) } });
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
  const needsDebrid = indexers.some((ix) => isTorrent(ix) && ix.enabled) && !hasUsableDebrid(cfg);
  const builtin = draft ? isBuiltin(draft) : false;
  const draftDef = draft && builtin ? defFor(draft) : undefined;

  return (
    <>
      {needsDebrid ? (
        <Alert variant="info" role="status">
          <AlertDescription>
            Torrent indexers need a debrid account to stream from.{" "}
            <Link to="/sources/debrid" className="font-medium underline underline-offset-2">
              Add a debrid provider
            </Link>
          </AlertDescription>
        </Alert>
      ) : null}
      <Section
        title="Indexers"
        description="Searched in order. Daily budgets keep Ether inside each site's limits."
        actions={
          <ActionMenu
            label="Add indexer"
            variant="default"
            size="sm"
            items={[
              {
                key: "site",
                label: "Built-in site…",
                icon: Globe,
                onSelect: () => {
                  setEditing(null);
                  setPicking(true);
                },
              },
              {
                key: "newznab",
                label: "Newznab or Torznab URL…",
                icon: Link2,
                onSelect: () => open(indexers.length, blankIndexer(indexers.length)),
              },
            ]}
          >
            <Plus size={14} /> Add indexer
          </ActionMenu>
        }
        bodyClassName=""
      >
        {indexers.length === 0 ? (
          <EmptyState
            icon={<Search />}
            title="No indexers"
            description="Add a built-in site or a Newznab/Torznab indexer so Ether can find releases."
          />
        ) : (
          <ul className="divide-y divide-border">
            {indexers.map((ix, i) => {
              const s = statFor(ix);
              const label = indexerName(ix, i);
              const hits = Number(s?.hitsUsed ?? 0);
              const site = isBuiltin(ix);
              const def = site ? defFor(ix) : undefined;
              const tested = site ? statusFor(ix) : undefined;
              return (
                <RecordItem
                  key={ix.id}
                  title={label}
                  description={[
                    i === 0 ? "Searched first" : `Priority ${i + 1}`,
                    site ? `Built-in site: ${def?.name ?? ix.definition ?? "unknown"}` : "",
                    ix.dailyLimit > 0
                      ? s
                        ? `${hits} of ${ix.dailyLimit} searches used today`
                        : `${ix.dailyLimit} searches a day`
                      : "Unlimited searches",
                    ix.dailyGrabs > 0 ? `${ix.dailyGrabs} downloads a day` : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                  meta={
                    <>
                      {site ? (
                        <>
                          <Badge variant="info">Built-in</Badge>
                          <Badge variant="outline">{siteProtocolLabel(ix.protocol)}</Badge>
                          {definitions && !def ? (
                            <Badge variant="destructive">Definition missing</Badge>
                          ) : null}
                          {tested ? (
                            <Badge
                              variant={tested.ok ? "success" : "destructive"}
                              title={tested.message}
                            >
                              {tested.ok ? "Test passed" : "Test failed"}
                            </Badge>
                          ) : null}
                        </>
                      ) : isTorrent(ix) || mixed ? (
                        <Badge variant="outline">{protocolLabel(ix)}</Badge>
                      ) : null}
                      {!ix.enabled ? <Badge variant="secondary">Off</Badge> : null}
                      {s && ix.dailyLimit > 0 && hits >= ix.dailyLimit ? (
                        <Badge variant="destructive">Out of searches</Badge>
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

      <BuiltinSites
        cfg={cfg}
        definitions={definitions}
        definitionsError={sites.error}
        inUse={inUse}
        onConfigSaved={() => config.reload()}
        onDefinitionsChanged={() => setSitesVersion((v) => v + 1)}
      />

      <Drawer
        open={picking}
        onClose={() => setPicking(false)}
        title="Add a built-in site"
        description="Choose the site's definition. Its settings come next."
        size="lg"
      >
        {sites.error ? (
          <ErrorPanel error={sites.error} onRetry={() => void sites.reload()} />
        ) : definitions === null ? (
          <LoadingRows rows={3} />
        ) : (
          <SitePicker
            definitions={definitions}
            onInstalled={() => setSitesVersion((v) => v + 1)}
            onPick={(def) => open(indexers.length, blankSiteIndexer(def, indexers.length))}
          />
        )}
      </Drawer>

      <RecordDrawer
        open={editing !== null}
        onClose={close}
        title={
          isNew
            ? builtin
              ? (draftDef?.name ?? "New built-in site")
              : "New indexer"
            : draft && editing
              ? indexerName(draft, editing.index)
              : ""
        }
        description={
          draft
            ? builtin
              ? `Built-in site · ${siteProtocolLabel(draft.protocol)}`
              : isTorrent(draft)
                ? "Torznab indexer"
                : "Newznab indexer"
            : undefined
        }
        onSave={() => void save()}
        saving={saving}
        saveLabel="Save indexer"
        onRemove={isNew ? undefined : () => void remove()}
        removeLabel="Remove indexer"
        error={error}
        tools={
          draft ? (
            builtin ? (
              <TestButton
                state={siteTest.state}
                onTest={() => void testSite(draft)}
                disabled={!draftDef}
              />
            ) : (
              <TestButton
                state={tests[draft.id] ?? { status: "idle" }}
                onTest={() => runTest(draft.id, () => api.testIndexer(draft))}
                disabled={!draft.url}
              />
            )
          ) : null
        }
      >
        {draft ? (
          <>
            {stat && draft.dailyLimit > 0 ? (
              <Meter
                value={used / draft.dailyLimit}
                label={`${used} searches used today`}
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
            {builtin && definitions && !draftDef ? (
              <MissingDefinition id={draft.definition} />
            ) : null}
            {builtin && siteTest.result ? <SiteTestResults result={siteTest.result} /> : null}
            {builtin ? (
              <SettingsSection title="Site">
                <TextRow label="Name" value={draft.name} onChange={(v) => set({ name: v })} />
                <TextRow
                  label="Site address"
                  type="url"
                  value={draft.url}
                  placeholder={draftDef?.links[0]}
                  help={
                    draftDef && draftDef.links.length > 1
                      ? `Also: ${draftDef.links.slice(1).join(", ")}`
                      : undefined
                  }
                  error={errors.url}
                  onChange={(v) => set({ url: v })}
                />
                <SettingsRow
                  title="Protocol"
                  description={
                    isTorrent(draft)
                      ? "Torrent releases stream through your debrid account."
                      : "Usenet releases stream from your Usenet providers."
                  }
                  control={<Badge variant="outline">{siteProtocolLabel(draft.protocol)}</Badge>}
                />
                <BoolRow
                  label="Enabled"
                  value={draft.enabled}
                  onChange={(v) => set({ enabled: v })}
                />
              </SettingsSection>
            ) : (
              <SettingsSection title="Connection">
                <SettingsRow
                  title="Protocol"
                  description={
                    isTorrent(draft)
                      ? "Torrent releases stream through your debrid account."
                      : "Usenet releases stream from your Usenet providers."
                  }
                  control={
                    <Segmented
                      ariaLabel="Protocol"
                      size="sm"
                      options={PROTOCOLS}
                      value={isTorrent(draft) ? "torrent" : "usenet"}
                      onChange={(v) => set({ protocol: v })}
                    />
                  }
                />
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
            )}
            {builtin && draftDef ? (
              <SiteSettingsFields def={draftDef} draft={draft} onChange={set} errors={errors} />
            ) : null}
            <SettingsSection title="Daily budget">
              <NumRow
                label="Daily searches"
                value={draft.dailyLimit}
                min={0}
                help={
                  builtin
                    ? "Searches Ether may run on this site per day. 0 = unlimited."
                    : "Your plan's API limit. 0 = unlimited."
                }
                error={errors.dailyLimit}
                onChange={(n) => set({ dailyLimit: n })}
              />
              <NumRow
                label="Daily downloads"
                value={draft.dailyGrabs}
                min={0}
                help={`${isTorrent(draft) ? "Torrent" : "NZB"} downloads allowed per day. 0 = unlimited.`}
                error={errors.dailyGrabs}
                onChange={(n) => set({ dailyGrabs: n })}
              />
            </SettingsSection>
            {builtin ? null : (
              <SettingsSection title="Categories">
                <SettingsRow
                  stacked
                  title="Movie categories"
                  description={`${protocolLabel(draft)} category ids. Empty uses the standard movie categories.`}
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
            )}
          </>
        ) : null}
      </RecordDrawer>
    </>
  );
}
