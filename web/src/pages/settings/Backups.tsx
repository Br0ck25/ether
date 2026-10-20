import {
  Alert,
  AlertDescription,
  Button,
  Input,
  PagedTable,
  SearchSelect,
  SettingsSection,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Download, FileUp, Plus } from "lucide-react";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { useClustered } from "./Cluster";
import { NumberInput } from "../../components/fields";
import { Section } from "../../components/layout";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { api, apiBlob, apiFetch, API_BASE, errorMessage, getToken } from "../../lib/api";
import { formatBytes } from "../../lib/format";
import type { EtherUser } from "../../lib/types";

type Plan = {
  id: string;
  imported: boolean;
  items: { title: string; key: number; views: number; rating: number; offsetMs: number }[];
  lists: { name: string; kind: string; keys: number[] }[];
  warnings: string[];
};
type Schedule = { intervalHours: number; retain: number; sharedDir?: string };
type Snapshot = {
  name: string;
  size: number;
  created: string;
  /** Clustered servers: the node that wrote (and, unless shared, holds) the bundle. */
  node?: string;
  nodeId?: string;
  /** In the shared backup directory every node reaches. */
  shared?: boolean;
  /** On another node's disk: download it from that node. */
  remote?: boolean;
};
type Backups = {
  snapshots: Snapshot[] | null;
  settings: Schedule;
  warning: string;
};

/** Where a clustered snapshot lives, for the Location column. */
function snapshotLocation(s: Snapshot): string {
  if (s.shared) return "Shared folder";
  const node = s.node || s.nodeId || "";
  if (!node) return "";
  return s.remote ? node : `${node} (this node)`;
}

const base = "/adoption";
const post = <T,>(path: string, body: unknown = {}) =>
  apiFetch<T>(base + path, { method: "POST", body: JSON.stringify(body) });

async function download(path: string, name: string) {
  const blob = await apiBlob(API_BASE + base + path);
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = name;
  anchor.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

async function upload(path: string, file: File, contentType: string) {
  const res = await fetch(API_BASE + base + path, {
    method: "POST",
    body: file,
    headers: { "X-Plex-Token": getToken(), "X-Ether-Request": "1", "Content-Type": contentType },
  });
  const data = (await res.json()) as { error?: { message?: string } };
  if (!res.ok) throw new Error(data.error?.message ?? `Request failed (${res.status})`);
  return data;
}

/** Runs one action at a time, reporting failures as toasts. */
function useBusy() {
  const [busy, setBusy] = useState(false);
  const run = useCallback(async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      return true;
    } catch (e) {
      toast.error(errorMessage(e));
      return false;
    } finally {
      setBusy(false);
    }
  }, []);
  return { busy, run };
}

function useImportViewers() {
  const [users, setUsers] = useState<EtherUser[] | null>(null);
  useEffect(() => {
    let live = true;
    api.users().then(
      (us) => {
        if (live) setUsers(us);
      },
      (e) => toast.error(errorMessage(e)),
    );
    return () => {
      live = false;
    };
  }, []);
  return users;
}

/** A native file picker styled as a button. */
function FilePick({
  label,
  accept,
  disabled,
  onFile,
}: {
  label: string;
  accept: string;
  disabled?: boolean;
  onFile: (file: File) => void;
}) {
  return (
    <label
      className={`relative inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-md border border-border bg-surface px-3 text-sm font-medium text-fg shadow-sm hover:bg-surface-hover focus-within:ring-2 focus-within:ring-ring/50 ${disabled ? "pointer-events-none opacity-50" : ""}`}
    >
      <FileUp size={14} /> {label}
      <input
        className="sr-only"
        type="file"
        accept={accept}
        aria-label={label}
        disabled={disabled}
        onChange={(e) => {
          const file = e.currentTarget.files?.[0];
          if (file) onFile(file);
          e.currentTarget.value = "";
        }}
      />
    </label>
  );
}

/**
 * Backups & import: the automatic backup schedule edited in place, snapshots
 * and restore on one tab; one-off imports from Plex, Trakt and Letterboxd on
 * the other. Both stay mounted so a half-finished import survives a tab switch.
 */
export function BackupsAndImport({ tab = "backups" }: { tab?: "backups" | "import" }) {
  const users = useImportViewers();
  const clustered = useClustered();
  const [data, setData] = useState<Backups | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [restored, setRestored] = useState("");
  const [saveError, setSaveError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const { busy, run } = useBusy();
  const { draft, setDraft, dirty, reset } = useDraft<Schedule>(data?.settings);

  const load = useCallback(async () => {
    try {
      setData(await apiFetch<Backups>(base + "/backups/"));
      setLoadError(null);
    } catch (e) {
      setLoadError(e);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const saveSchedule = async () => {
    if (!draft) return;
    setSaving(true);
    setSaveError(null);
    try {
      await post("/backups/settings", {
        intervalHours: draft.intervalHours,
        retain: draft.retain,
        ...(clustered ? { sharedDir: (draft.sharedDir ?? "").trim() } : {}),
      });
      toast.success("Settings saved", { description: "Automatic backups" });
      await load();
    } catch (e) {
      setSaveError(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <div hidden={tab !== "backups"} className="space-y-6">
        {loadError && !data ? (
          <ErrorPanel error={loadError} onRetry={() => void load()} />
        ) : (
          <>
            <SettingsSection
              title="Automatic backups"
              description="Snapshots hold your settings and a consistent copy of the database."
            >
              {draft ? (
                <>
                  <SettingsRow
                    title={<label htmlFor="backup-interval">Back up every</label>}
                    description="0 turns automatic backups off."
                    control={
                      <span className="flex items-center gap-2">
                        <NumberInput
                          id="backup-interval"
                          aria-label="Back up every"
                          className="w-24"
                          min={0}
                          max={8760}
                          step={1}
                          value={draft.intervalHours}
                          onChange={(n) => setDraft({ ...draft, intervalHours: n })}
                        />
                        <span className="text-sm text-fg-muted">hours</span>
                      </span>
                    }
                  />
                  <SettingsRow
                    title={<label htmlFor="backup-retain">Snapshots to keep</label>}
                    description="The oldest snapshot is removed when a new one would exceed this."
                    control={
                      <NumberInput
                        id="backup-retain"
                        aria-label="Snapshots to keep"
                        className="w-24"
                        min={1}
                        max={365}
                        step={1}
                        value={draft.retain}
                        onChange={(n) => setDraft({ ...draft, retain: n })}
                      />
                    }
                  />
                  {clustered ? (
                    <SettingsRow
                      stacked
                      title={<label htmlFor="backup-shared-dir">Shared backup folder</label>}
                      description="A folder every node reaches at the same path, such as a network share. Leave empty to keep each backup on the node that made it."
                    >
                      <Input
                        id="backup-shared-dir"
                        placeholder="/mnt/backups/ether"
                        value={draft.sharedDir ?? ""}
                        onChange={(e) => setDraft({ ...draft, sharedDir: e.currentTarget.value })}
                      />
                    </SettingsRow>
                  ) : null}
                </>
              ) : (
                <div className="px-5 py-4">
                  <LoadingRows rows={2} />
                </div>
              )}
            </SettingsSection>

            <Section
              title="Snapshots"
              description="Downloads include account credentials. Keep them private."
              actions={
                <Button
                  size="sm"
                  disabled={busy}
                  onClick={() => void run(() => post("/backups/").then(load))}
                >
                  <Plus size={14} /> {busy ? "Working…" : "Create snapshot"}
                </Button>
              }
            >
              {data?.warning ? (
                <Alert variant="warning" role="status">
                  <AlertDescription>{data.warning}</AlertDescription>
                </Alert>
              ) : null}
              {data ? (
                <PagedTable
                  rows={data.snapshots ?? []}
                  rowKey={(s) => s.name}
                  pageSize={10}
                  empty="No snapshots yet. Create your first backup."
                  defaultSort={{ key: "created", dir: "desc" }}
                  labels={{ searchLabel: "Search backups" }}
                  columns={[
                    {
                      key: "created",
                      header: "Created",
                      search: (s) => s.name,
                      sortValue: (s) => s.created,
                      cell: (s) => new Date(s.created).toLocaleString(),
                    },
                    {
                      key: "size",
                      header: "Size",
                      align: "right",
                      sortValue: (s) => s.size,
                      cell: (s) => <span className="tabular-nums">{formatBytes(s.size)}</span>,
                    },
                    ...((data.snapshots ?? []).some((s) => snapshotLocation(s))
                      ? [
                          {
                            key: "location",
                            header: "Location",
                            search: snapshotLocation,
                            sortValue: snapshotLocation,
                            cell: (s: Snapshot) => (
                              <span className="text-fg-muted">{snapshotLocation(s) || "—"}</span>
                            ),
                          },
                        ]
                      : []),
                    {
                      key: "download",
                      header: "",
                      align: "right",
                      cell: (s) => (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={busy || s.remote}
                          title={
                            s.remote
                              ? `Stored on ${s.node || "another node"}: download it from that node's admin page`
                              : undefined
                          }
                          aria-label={`Download ${s.name}`}
                          onClick={() =>
                            void run(() =>
                              download("/backups/export/" + encodeURIComponent(s.name), s.name),
                            )
                          }
                        >
                          <Download size={14} /> Download
                        </Button>
                      ),
                    },
                  ]}
                />
              ) : (
                <LoadingRows rows={3} />
              )}
            </Section>

            <Section
              title="Restore a backup"
              description="Ether checks the bundle's checksums and database, then prepares it in a separate data folder. Nothing changes until you stop Ether and start it from that folder."
            >
              <FilePick
                label="Choose backup bundle"
                accept=".tar.gz,.gz,application/gzip"
                disabled={busy}
                onFile={(file) =>
                  void run(async () => {
                    const result = (await upload("/backups/restore", file, "application/gzip")) as {
                      path?: string;
                    };
                    setRestored(result.path ?? "");
                    toast.success("Restore validated and staged");
                  })
                }
              />
              {restored ? (
                <Alert variant="success" role="status">
                  <AlertDescription>
                    Ready to restore. Stop Ether and start it with the data folder{" "}
                    <code className="break-all">{restored}</code>.
                  </AlertDescription>
                </Alert>
              ) : null}
            </Section>
          </>
        )}
      </div>
      <div hidden={tab !== "import"} className="space-y-6">
        <PlexImport users={users} />
        <CsvTools users={users} />
      </div>

      <SaveBar
        dirty={dirty}
        saving={saving}
        error={saveError}
        onSave={saveSchedule}
        onDiscard={() => {
          reset();
          setSaveError(null);
        }}
      />
    </>
  );
}

// ---- Imports ---------------------------------------------------------------------------

function ViewerPick({
  label,
  users,
  value,
  onChange,
}: {
  label: string;
  users: EtherUser[] | null;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="w-full sm:w-64">
      <SearchSelect
        ariaLabel={label}
        placeholder={users ? "Choose a viewer" : "Loading viewers…"}
        value={value}
        onChange={onChange}
        options={(users ?? []).map((u) => ({ value: String(u.id), label: u.title || u.username }))}
      />
    </div>
  );
}

/** Step two of every import: what will change, and the explicit import action. */
function ImportReview({
  id,
  plan,
  busy,
  onImport,
}: {
  id: string;
  plan: Plan;
  busy: boolean;
  onImport: () => void;
}) {
  return (
    <Section
      id={id}
      title={plan.imported ? "Imported" : "Review import"}
      description={`${plan.items.length} watch records · ${plan.lists.length} playlists and collections`}
      footer={
        <Button size="sm" disabled={busy || plan.imported} onClick={onImport}>
          {plan.imported ? "Import complete" : "Import reviewed records"}
        </Button>
      }
    >
      {plan.warnings.length > 0 ? (
        <ul className="max-h-52 list-disc space-y-1 overflow-auto pl-5 text-sm text-warning">
          {plan.warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      ) : null}
      {plan.items.length ? (
        <ul className="max-h-64 space-y-0.5 overflow-auto text-sm">
          {plan.items.slice(0, 100).map((it, i) => (
            <li key={i}>
              {it.title} · {it.views} watches · rating {it.rating || "—"}
              {it.offsetMs > 0 ? ` · resume at ${Math.floor(it.offsetMs / 60000)} min` : ""}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-fg-muted">Nothing matched.</p>
      )}
    </Section>
  );
}

function useImportPlan(reviewId: string) {
  const [plan, setPlan] = useState<Plan | null>(null);
  const { busy, run } = useBusy();
  useEffect(() => {
    if (plan)
      document.getElementById(reviewId)?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [plan, reviewId]);
  const review: ReactNode = plan ? (
    <ImportReview
      id={reviewId}
      plan={plan}
      busy={busy}
      onImport={() =>
        void run(async () => {
          setPlan(await post<Plan>("/migration/import", { id: plan.id }));
          toast.success("Import complete");
        })
      }
    />
  ) : null;
  return { setPlan, busy, run, review };
}

/** The viewer imports apply to, defaulting to the first one. */
function useViewerChoice(users: EtherUser[] | null) {
  const [viewer, setViewer] = useState("");
  useEffect(() => {
    if (!viewer && users?.[0]) setViewer(String(users[0].id));
  }, [users, viewer]);
  return [viewer, setViewer] as const;
}

function PlexImport({ users }: { users: EtherUser[] | null }) {
  const [viewer, setViewer] = useViewerChoice(users);
  const [url, setURL] = useState("");
  const [token, setToken] = useState("");
  const { setPlan, busy, run, review } = useImportPlan("plex-import-review");
  const edit = (fn: (v: string) => void) => (v: string) => {
    fn(v);
    setPlan(null);
  };
  return (
    <>
      <SettingsSection
        title="Import from Plex"
        description="Bring one viewer's watch history, ratings and playlists across from another Plex server. That server is only read, and you review everything before it's imported."
        footer={
          <Button
            size="sm"
            variant="outline"
            disabled={busy || !url || !token || !viewer}
            onClick={() =>
              void run(async () => {
                const p = await post<Plan>("/migration/preview", {
                  url,
                  token,
                  viewers: [
                    {
                      localUserId: Number(viewer),
                      name: users?.find((u) => String(u.id) === viewer)?.username,
                    },
                  ],
                });
                setPlan(p);
                setToken("");
              })
            }
          >
            {busy ? "Working…" : "Preview import"}
          </Button>
        }
      >
        <SettingsRow
          title="Ether viewer"
          description="Who the history belongs to here. To add friends, share your library with them in Plex first."
          control={
            <ViewerPick
              label="Ether viewer"
              users={users}
              value={viewer}
              onChange={edit(setViewer)}
            />
          }
        />
        <SettingsRow stacked title={<label htmlFor="plex-import-url">Plex server URL</label>}>
          <Input
            id="plex-import-url"
            type="url"
            placeholder="http://plex-server:32400"
            value={url}
            onChange={(e) => edit(setURL)(e.currentTarget.value)}
          />
        </SettingsRow>
        <SettingsRow
          stacked
          title={<label htmlFor="plex-import-token">Source viewer’s Plex token</label>}
          description="That viewer's access token on the source server. It's used for the preview and not stored."
        >
          <Input
            id="plex-import-token"
            type="password"
            autoComplete="off"
            value={token}
            onChange={(e) => edit(setToken)(e.currentTarget.value)}
          />
        </SettingsRow>
      </SettingsSection>
      {review}
    </>
  );
}

const CSV_SERVICES = [
  { value: "letterboxd", label: "Letterboxd" },
  { value: "trakt", label: "Trakt" },
];
const CSV_KINDS = [
  { value: "history", label: "History" },
  { value: "ratings", label: "Ratings" },
  { value: "watchlist", label: "Watchlist" },
];

function CsvTools({ users }: { users: EtherUser[] | null }) {
  const [viewer, setViewer] = useViewerChoice(users);
  const [format, setFormat] = useState("letterboxd");
  const [kind, setKind] = useState("history");
  const { setPlan, busy, run, review } = useImportPlan("csv-import-review");
  const query = `viewer=${viewer}&format=${format}&kind=${kind}`;
  const edit = (fn: (v: string) => void) => (v: string) => {
    fn(v);
    setPlan(null);
  };
  return (
    <>
      <SettingsSection
        title="Trakt and Letterboxd files"
        description="Export or import a diary, ratings or watchlist as CSV. Letterboxd matches need the same title and year."
        footer={
          <>
            <Button
              size="sm"
              variant="outline"
              disabled={busy || !viewer}
              onClick={() =>
                void run(() =>
                  download(`/migration/csv/export?${query}`, `ether-${format}-${kind}.csv`),
                )
              }
            >
              <Download size={14} /> Export CSV
            </Button>
            <FilePick
              label="Preview a CSV import"
              accept=".csv,text/csv"
              disabled={busy || !viewer}
              onFile={(file) =>
                void run(async () => {
                  setPlan(
                    (await upload(`/migration/csv/preview?${query}`, file, "text/csv")) as Plan,
                  );
                })
              }
            />
          </>
        }
      >
        <SettingsRow
          title="Viewer"
          control={
            <ViewerPick
              label="CSV viewer"
              users={users}
              value={viewer}
              onChange={edit(setViewer)}
            />
          }
        />
        <SettingsRow
          title="Service"
          control={
            <div className="w-48">
              <SearchSelect
                ariaLabel="CSV service"
                value={format}
                onChange={edit(setFormat)}
                options={CSV_SERVICES}
              />
            </div>
          }
        />
        <SettingsRow
          title="Contents"
          control={
            <div className="w-48">
              <SearchSelect
                ariaLabel="CSV contents"
                value={kind}
                onChange={edit(setKind)}
                options={CSV_KINDS}
              />
            </div>
          }
        />
      </SettingsSection>
      {review}
    </>
  );
}
