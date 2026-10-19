import { EditPanel } from "../components/EditPanel";
import {
  Button,
  Input,
  PageContainer,
  PageHeader,
  PagedTable,
  SubtabNav,
  toast,
} from "@braedonsaunders/appkit-ui";
import { useCallback, useEffect, useState } from "react";
import { api, apiBlob, apiFetch, API_BASE, errorMessage, getToken } from "../lib/api";
import { TitleSearch } from "../components/pickers";
import type { TitleCard } from "../lib/contract";
import type { EtherUser } from "../lib/types";

type Plan = {
  id: string;
  imported: boolean;
  items: { title: string; key: number; views: number; rating: number; offsetMs: number }[];
  lists: { name: string; kind: string; keys: number[] }[];
  warnings: string[];
};
type LocalFile = { path: string; key: number; size: number; error?: string };
type Libraries = { folders: string[]; files: LocalFile[] };
type Backups = {
  snapshots: { name: string; size: number; created: string }[];
  settings: { intervalHours: number; retain: number };
  warning: string;
};
const base = "/adoption";
const panel = "space-y-4 rounded-xl border border-border bg-surface p-5";
const selectClass = "rounded-lg border border-border bg-surface px-3 py-2 text-sm";
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

export default function Adoption() {
  const [tab, setTab] = useState<"migration" | "libraries" | "backups">("migration");
  return (
    <PageContainer className="space-y-6">
      <PageHeader
        title="Bring your library"
        description="Move your Plex history, use local media, and keep portable backups."
      />
      <div className="border-b border-border">
        <SubtabNav
          ariaLabel="Adoption tools"
          active={tab}
          onSelect={(key) => setTab(key as typeof tab)}
          tabs={[
            { key: "migration", label: "Migration" },
            { key: "libraries", label: "Local libraries" },
            { key: "backups", label: "Backups" },
          ]}
        />
      </div>
      {tab === "migration" ? (
        <Migration />
      ) : tab === "libraries" ? (
        <LocalLibraries />
      ) : (
        <BackupPanel />
      )}
    </PageContainer>
  );
}
function Migration() {
  const [tool, setTool] = useState("");
  const [url, setURL] = useState("");
  const [token, setToken] = useState("");
  const [users, setUsers] = useState<EtherUser[]>([]);
  const [viewer, setViewer] = useState("");
  const [plan, setPlan] = useState<Plan | null>(null);
  const [busy, setBusy] = useState(false);
  const [format, setFormat] = useState("letterboxd");
  const [kind, setKind] = useState("history");
  useEffect(() => {
    void api
      .users()
      .then((us) => {
        setUsers(us);
        if (us[0]) setViewer(String(us[0].id));
      })
      .catch((e) => toast.error(errorMessage(e)));
  }, []);
  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    if (plan)
      document
        .getElementById("import-review")
        ?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [plan]);
  const review = plan && (
    <section id="import-review" className={panel}>
      <h2 className="text-lg font-semibold">{plan.imported ? "Imported" : "Review import"}</h2>
      <p>
        {plan.items.length} matched state records · {plan.lists.length} playlists and collections
      </p>
      {plan.warnings.length > 0 && (
        <ul className="max-h-52 list-disc space-y-1 overflow-auto pl-5 text-sm text-fg-muted">
          {plan.warnings.map((warning, i) => (
            <li key={i}>{warning}</li>
          ))}
        </ul>
      )}
      <div className="max-h-64 overflow-auto text-sm">
        {plan.items.slice(0, 100).map((it, i) => (
          <p key={i}>
            {it.title} · {it.views} watches · rating {it.rating || "—"}
            {it.offsetMs > 0 ? ` · resume at ${Math.floor(it.offsetMs / 60000)} min` : ""}
          </p>
        ))}
      </div>
      <Button
        disabled={busy || plan.imported}
        onClick={() =>
          void act(async () => {
            setPlan(await post<Plan>("/migration/import", { id: plan.id }));
            toast.success("Import complete");
          })
        }
      >
        {plan.imported ? "Import complete" : "Import reviewed records"}
      </Button>
    </section>
  );
  return (
    <div className="space-y-5">
      <EditPanel
        open={tool === "plex"}
        onOpen={() => {
          setPlan(null);
          setTool("plex");
        }}
        onOpenChange={(open) => {
          if (!open) setTool("");
        }}
        size="xl"
        title="Import from Plex"
        description="Bring across watched history, ratings and lists. Preview before importing."
        action="Start Plex import"
      >
        <section className={panel}>
          <h2 className="text-lg font-semibold">Migrate from Plex</h2>
          <p className="text-sm text-fg-muted">
            Preview one viewer at a time using that viewer’s source-server access token. The source
            server is read only. Add friends through Plex sharing before choosing their Ether viewer
            here.
          </p>
          <label className="block space-y-1 text-sm">
            Ether viewer
            <select
              className={selectClass + " block w-full"}
              value={viewer}
              onChange={(e) => {
                setViewer(e.target.value);
                setPlan(null);
              }}
            >
              {users.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.title || u.username}
                </option>
              ))}
            </select>
          </label>
          <label className="block space-y-1 text-sm">
            Plex server URL
            <Input
              type="url"
              placeholder="http://plex-server:32400"
              value={url}
              onChange={(e) => {
                setURL(e.target.value);
                setPlan(null);
              }}
            />
          </label>
          <label className="block space-y-1 text-sm">
            Source viewer’s Plex token
            <Input
              type="password"
              autoComplete="off"
              value={token}
              onChange={(e) => {
                setToken(e.target.value);
                setPlan(null);
              }}
            />
          </label>
          <Button
            disabled={busy || !url || !token || !viewer}
            onClick={() =>
              void act(async () => {
                const p = await post<Plan>("/migration/preview", {
                  url,
                  token,
                  viewers: [
                    {
                      localUserId: Number(viewer),
                      name: users.find((u) => String(u.id) === viewer)?.username,
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
        </section>
        {review}
      </EditPanel>
      <EditPanel
        open={tool === "csv"}
        onOpen={() => {
          setPlan(null);
          setTool("csv");
        }}
        onOpenChange={(open) => {
          if (!open) setTool("");
        }}
        size="xl"
        title="CSV import & export"
        description="Transfer a Trakt or Letterboxd diary, ratings or watchlist."
        action="Open CSV tools"
      >
        <section className={panel}>
          <h2 className="text-lg font-semibold">Trakt and Letterboxd CSV</h2>
          <p className="text-sm text-fg-muted">
            Use an exported diary, rating, or watchlist CSV. Letterboxd title matches require the
            same name and year. Preview results before importing.
          </p>
          <label className="block text-sm">
            CSV viewer
            <select
              className={selectClass + " block w-full"}
              value={viewer}
              onChange={(e) => {
                setViewer(e.target.value);
                setPlan(null);
              }}
            >
              {users.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.title || u.username}
                </option>
              ))}
            </select>
          </label>
          <div className="flex flex-wrap gap-2">
            <select
              aria-label="CSV service"
              className={selectClass}
              value={format}
              onChange={(e) => setFormat(e.target.value)}
            >
              <option value="letterboxd">Letterboxd</option>
              <option value="trakt">Trakt</option>
            </select>
            <select
              aria-label="CSV contents"
              className={selectClass}
              value={kind}
              onChange={(e) => setKind(e.target.value)}
            >
              <option value="history">History</option>
              <option value="ratings">Ratings</option>
              <option value="watchlist">Watchlist</option>
            </select>
            <Button
              variant="outline"
              disabled={busy || !viewer}
              onClick={() =>
                void act(() =>
                  download(
                    `/migration/csv/export?viewer=${viewer}&format=${format}&kind=${kind}`,
                    `ether-${format}-${kind}.csv`,
                  ),
                )
              }
            >
              Export CSV
            </Button>
          </div>
          <label className="block text-sm">
            Preview CSV file
            <input
              className="mt-2 block w-full text-sm"
              type="file"
              accept=".csv,text/csv"
              disabled={busy || !viewer}
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file)
                  void act(async () => {
                    const p = await upload(
                      `/migration/csv/preview?viewer=${viewer}&format=${format}&kind=${kind}`,
                      file,
                      "text/csv",
                    );
                    setPlan(p as Plan);
                  });
                e.currentTarget.value = "";
              }}
            />
          </label>
        </section>
        {review}
      </EditPanel>
      {!tool && review}
    </div>
  );
}
function LocalLibraries() {
  const [data, setData] = useState<Libraries | null>(null);
  const [folder, setFolder] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(
    async () => setData(await apiFetch<Libraries>(base + "/libraries/")),
    [],
  );
  useEffect(() => {
    void load().catch((e) => toast.error(errorMessage(e)));
  }, [load]);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      await load();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className={panel}>
      <h2 className="text-lg font-semibold">Local libraries</h2>
      <p className="text-sm text-fg-muted">
        Matched local files play first. Usenet fills missing titles. Folder scans skip symbolic
        links; unmatched files remain available for review.
      </p>
      <EditPanel
        title="Add a media folder"
        description="Choose a folder on this server. Ether scans it for movies and episodes."
        action="Add folder"
      >
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void act(async () => {
              await post("/libraries/folders", { path: folder });
              setFolder("");
              await post("/libraries/scan");
            });
          }}
        >
          <Input
            aria-label="Local media folder"
            placeholder="/media/Movies"
            value={folder}
            onChange={(e) => setFolder(e.target.value)}
          />
          <Button disabled={busy || !folder}>Add folder</Button>
        </form>
      </EditPanel>
      <Button
        variant="outline"
        disabled={busy}
        onClick={() => void act(() => post("/libraries/scan"))}
      >
        {busy ? "Scanning…" : "Scan now"}
      </Button>
      {data?.folders.map((path) => (
        <div key={path} className="flex items-center justify-between gap-3 text-sm">
          <code className="break-all">{path}</code>
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() =>
              void act(() =>
                apiFetch(base + "/libraries/folders?path=" + encodeURIComponent(path), {
                  method: "DELETE",
                }),
              )
            }
          >
            Remove folder
          </Button>
        </div>
      ))}
      <PagedTable
        rows={data?.files ?? []}
        rowKey={(file) => file.path}
        pageSize={15}
        empty="No media files indexed. Add a folder and scan it."
        labels={{ searchLabel: "Search local files", searchPlaceholder: "Find a file…" }}
        columns={[
          {
            key: "path",
            header: "File",
            search: (file) => file.path,
            sortValue: (file) => file.path,
            cell: (file) => <span className="block max-w-lg break-all">{file.path}</span>,
          },
          {
            key: "status",
            header: "Match",
            search: (file) => (file.key ? "matched" : "needs match"),
            cell: (file) => (
              <span>
                {file.key ? "Matched" : "Needs match"}
                {file.error && <span className="block text-xs text-danger">{file.error}</span>}
              </span>
            ),
          },
          {
            key: "size",
            header: "Size",
            align: "right",
            sortValue: (file) => file.size,
            cell: (file) => (
              <span className="tabular-nums">{(file.size / 1048576).toFixed(1)} MB</span>
            ),
          },
          {
            key: "actions",
            header: "",
            cell: (file) => (
              <LocalMatch
                file={file}
                busy={busy}
                save={(body) => act(() => post("/libraries/match", body))}
              />
            ),
          },
        ]}
      />
    </section>
  );
}
function BackupPanel() {
  const [data, setData] = useState<Backups | null>(null);
  const [busy, setBusy] = useState(false);
  const [interval, setIntervalHours] = useState(24);
  const [retain, setRetain] = useState(7);
  const [restore, setRestore] = useState("");
  const load = useCallback(async () => {
    const d = await apiFetch<Backups>(base + "/backups/");
    setData(d);
    setIntervalHours(d.settings.intervalHours);
    setRetain(d.settings.retain);
  }, []);
  useEffect(() => {
    void load().catch((e) => toast.error(errorMessage(e)));
  }, [load]);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      await load();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className={panel}>
      <h2 className="text-lg font-semibold">Backups</h2>
      <p className="text-sm text-fg-muted">
        Snapshots include config and a consistent SQLite database. Bundles contain account
        credentials; keep downloads private.
      </p>
      {data?.warning && (
        <p role="status" className="text-sm text-warning">
          {data.warning}
        </p>
      )}
      <EditPanel
        title="Automatic backups"
        description="Choose how often to create snapshots and how many to retain."
        onOpen={() => {
          if (data) {
            setIntervalHours(data.settings.intervalHours);
            setRetain(data.settings.retain);
          }
        }}
        onDiscard={() => {
          if (data) {
            setIntervalHours(data.settings.intervalHours);
            setRetain(data.settings.retain);
          }
        }}
        action="Edit backup schedule"
        summary={
          data
            ? `${data.settings.intervalHours === 0 ? "Disabled" : `Every ${data.settings.intervalHours} hours`} · ${data.settings.retain} snapshots retained`
            : "Loading schedule…"
        }
      >
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            void act(() => post("/backups/settings", { intervalHours: interval, retain }));
          }}
        >
          <label className="text-sm">
            Every hours (0 disables)
            <Input
              type="number"
              min={0}
              max={8760}
              value={interval}
              onChange={(e) => setIntervalHours(Number(e.target.value))}
            />
          </label>
          <label className="text-sm">
            Keep snapshots
            <Input
              type="number"
              min={1}
              max={365}
              value={retain}
              onChange={(e) => setRetain(Number(e.target.value))}
            />
          </label>
          <Button variant="outline" disabled={busy}>
            Save schedule
          </Button>
        </form>
      </EditPanel>
      <Button disabled={busy} onClick={() => void act(() => post("/backups/"))}>
        {busy ? "Working…" : "Create snapshot"}
      </Button>
      <PagedTable
        rows={data?.snapshots ?? []}
        rowKey={(snapshot) => snapshot.name}
        pageSize={10}
        empty="No snapshots yet. Create your first backup."
        defaultSort={{ key: "created", dir: "desc" }}
        labels={{ searchLabel: "Search backups" }}
        columns={[
          {
            key: "created",
            header: "Created",
            search: (snapshot) => snapshot.name,
            sortValue: (snapshot) => snapshot.created,
            cell: (snapshot) => new Date(snapshot.created).toLocaleString(),
          },
          {
            key: "size",
            header: "Size",
            align: "right",
            sortValue: (snapshot) => snapshot.size,
            cell: (snapshot) => (snapshot.size / 1048576).toFixed(1) + " MB",
          },
          {
            key: "download",
            header: "",
            cell: (snapshot) => (
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() =>
                  void act(() =>
                    download("/backups/export/" + encodeURIComponent(snapshot.name), snapshot.name),
                  )
                }
              >
                Download
              </Button>
            ),
          },
        ]}
      />
      <EditPanel
        title="Restore a backup"
        description="Validate a bundle and prepare a separate data directory."
        action="Restore backup"
      >
        <div className="space-y-2 border-t border-border pt-4">
          <h3 className="font-medium">Restore on this machine</h3>
          <p className="text-sm text-fg-muted">
            Validates checksums and database integrity, then stages a new data directory. Stop Ether
            and use the returned directory to activate the restore.
          </p>
          <label className="block text-sm">
            Backup bundle
            <input
              className="mt-2 block text-sm"
              type="file"
              accept=".tar.gz,.gz,application/gzip"
              disabled={busy}
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file)
                  void act(async () => {
                    const result = (await upload("/backups/restore", file, "application/gzip")) as {
                      path?: string;
                    };
                    setRestore(result.path ?? "");
                    toast.success("Restore validated and staged");
                  });
                e.currentTarget.value = "";
              }}
            />
          </label>
          {restore && (
            <p role="status" className="break-all text-sm">
              Restored directory: <code>{restore}</code>
            </p>
          )}
        </div>
      </EditPanel>
    </section>
  );
}

function LocalMatch({
  file,
  busy,
  save,
}: {
  file: LocalFile;
  busy: boolean;
  save: (body: unknown) => Promise<void>;
}) {
  const [title, setTitle] = useState<TitleCard | null>(null);
  const [season, setSeason] = useState(1);
  const [episode, setEpisode] = useState(1);
  return (
    <EditPanel
      compact
      title="Match local file"
      description={file.path}
      action={file.key ? "Change match" : "Match file"}
    >
      <div className="space-y-2">
        <TitleSearch placeholder="Match a movie or show…" onPick={setTitle} />
        {title && (
          <>
            <p>
              {title.title}
              {title.year ? ` (${title.year})` : ""}
            </p>
            {title.type === "show" && (
              <div className="flex gap-3">
                <label>
                  Season
                  <Input
                    aria-label="Match season"
                    type="number"
                    min={0}
                    max={999}
                    value={season}
                    onChange={(e) => setSeason(Number(e.target.value))}
                  />
                </label>
                <label>
                  Episode
                  <Input
                    aria-label="Match episode"
                    type="number"
                    min={1}
                    max={999}
                    value={episode}
                    onChange={(e) => setEpisode(Number(e.target.value))}
                  />
                </label>
              </div>
            )}
            <Button
              variant="outline"
              disabled={busy || (title.type !== "movie" && title.type !== "show")}
              onClick={() =>
                void save(
                  title.type === "show"
                    ? { path: file.path, showKey: Number(title.key), season, episode }
                    : { path: file.path, key: Number(title.key) },
                )
              }
            >
              Match this file
            </Button>
          </>
        )}
      </div>
    </EditPanel>
  );
}
