import {
  Alert,
  AlertDescription,
  AlertTitle,
  Badge,
  Button,
  confirmDialog,
  EmptyState,
  Input,
  PagedTable,
  SettingsSection,
  Textarea,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Copy, KeyRound, Plus } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { Field, Segmented } from "../../components/fields";
import { ClusterSetup } from "./ClusterSetup";
import { Section } from "../../components/layout";
import { SettingsRow } from "../../components/SettingsRow";
import { api, errorMessage } from "../../lib/api";
import { formatDateTime, parseTime, plural, relativeTime } from "../../lib/format";
import { useAsync, useInterval, type AsyncState } from "../../lib/hooks";
import type {
  ClusterNode,
  ClusterSettings,
  ClusterStatus,
  CreatedJoinToken,
  JoinTokenInfo,
} from "../../lib/types";

export const CLUSTER_DOCS =
  "https://github.com/braedonsaunders/ether/blob/main/docs/install.md#running-a-cluster";

/** How often the page refreshes membership and tokens while it is visible. */
export const CLUSTER_POLL_MS = 10_000;

/** A node is live within 20 s of its last heartbeat; older means the database is unreachable. */
const HEARTBEAT_STALE_MS = 20_000;
/** Clock differences beyond this confuse liveness and token expiry. */
const SKEW_WARN_NS = 2e9;

export function isClustered(s: ClusterStatus | null | undefined): boolean {
  return s?.mode === "postgres";
}

/**
 * Whether this server is a cluster node, read once per page; false while
 * loading and when the server doesn't report it (standalone behaviour).
 */
export function useClustered(): boolean {
  const status = useAsync((signal) => api.clusterStatus(signal).catch(() => null), []);
  return isClustered(status.data);
}

function nodeName(n: ClusterNode): string {
  return n.name || n.id;
}

/** "videotoolbox", "Software" or "Unavailable" (no ffmpeg on the node). */
export function transcodeLabel(n: ClusterNode): string {
  if (!n.caps?.transcode) return "Unavailable";
  const hw = n.caps.hwAccel;
  return hw && hw !== "none" ? hw : "Software";
}

/** Server warnings plus conditions this node's own health reports. */
export function clusterWarnings(s: ClusterStatus, now: Date = new Date()): string[] {
  // Server warnings are log-style sentences; capitalise them for the banner.
  const out = (s.warnings ?? []).map((w) => w.charAt(0).toUpperCase() + w.slice(1));
  if (s.error) out.push(`Couldn't read the cluster's nodes from the database: ${s.error}`);
  const h = s.health;
  if (h) {
    const beat = parseTime(h.lastHeartbeat);
    if (!beat || now.getTime() - beat.getTime() > HEARTBEAT_STALE_MS)
      out.push(
        beat
          ? `This node hasn't reached the database since ${relativeTime(beat, now)}. Streams keep playing, but other nodes may treat it as offline.`
          : "This node hasn't reached the database yet.",
      );
    else if (!h.busConnected)
      out.push(
        "This node's event connection to the database is down, so changes made on other nodes may reach it late.",
      );
    if (Math.abs(h.clockSkewNs ?? 0) > SKEW_WARN_NS)
      out.push(
        `This node's clock is ${Math.round(Math.abs(h.clockSkewNs) / 1e9)} seconds off the database server's. Sync the clock (NTP) on every node.`,
      );
  }
  return out;
}

export type TokenState = "used" | "expired" | "unused";

export function tokenState(t: JoinTokenInfo, now: Date = new Date()): TokenState {
  if (parseTime(t.usedAt)) return "used";
  const exp = parseTime(t.expiresAt);
  if (exp && exp.getTime() <= now.getTime()) return "expired";
  return "unused";
}

/** Live nodes first, the leader first among them, then by name. */
function sortNodes(nodes: ClusterNode[]): ClusterNode[] {
  return [...nodes].sort(
    (a, b) =>
      Number(b.live) - Number(a.live) ||
      Number(b.leader) - Number(a.leader) ||
      nodeName(a).localeCompare(nodeName(b)),
  );
}

function copy(text: string, what: string) {
  void navigator.clipboard
    .writeText(text)
    .then(() => toast.success(`${what} copied`))
    .catch(() => toast.error(`Select and copy the ${what.toLowerCase()}`));
}

/**
 * Server › Cluster: on a standalone server, what clustering is and how to
 * start; on a cluster node, the nodes, their health and the join tokens.
 */
export function ClusterOverview() {
  const status = useAsync((signal) => api.clusterStatus(signal), []);
  const clustered = isClustered(status.data);
  const [addingNode, setAddingNode] = useState(false);
  const setup = useAsync((signal) => api.clusterSetupStatus(signal), []);
  const settings = useAsync(
    async (signal) => (clustered ? api.clusterSettings(signal) : null),
    [clustered],
  );
  useInterval(() => {
    void status.reload();
    if (clustered) void settings.reload();
  }, CLUSTER_POLL_MS);

  if (status.error && !status.data)
    return <ErrorPanel error={status.error} onRetry={() => void status.reload()} />;
  const s = status.data;
  if (!s) return <LoadingRows rows={3} />;
  if (!clustered)
    return (
      <Standalone
        status={s}
        available={setup.data?.available ?? false}
        message={setup.data?.state === "failed" ? setup.data.message : undefined}
        onComplete={() => {
          void status.reload();
          void setup.reload();
        }}
      />
    );

  const warnings = clusterWarnings(s);
  return (
    <>
      {warnings.length ? (
        <Alert variant="warning" role="status">
          <AlertTitle>Cluster needs attention</AlertTitle>
          <AlertDescription>
            <ul className="list-disc space-y-1 pl-5">
              {warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : null}
      <Nodes status={s} onChanged={() => void status.reload()} onAdd={() => setAddingNode(true)} />
      <SharedSettings status={s} settings={settings} />
      <JoinTokens settings={settings} />
      {addingNode ? (
        <JoinTokenDrawer
          onClose={() => setAddingNode(false)}
          onCreated={() => void settings.reload()}
        />
      ) : null}
    </>
  );
}

function Standalone({
  status,
  available,
  message,
  onComplete,
}: {
  status: ClusterStatus;
  available: boolean;
  message?: string;
  onComplete: () => void;
}) {
  return (
    <>
      {message ? (
        <Alert variant="destructive" role="alert">
          <AlertTitle>Cluster setup needs attention</AlertTitle>
          <AlertDescription>{message}</AlertDescription>
        </Alert>
      ) : null}
      <SettingsSection title="This server">
        <SettingsRow
          title="Role"
          description="Your data currently lives in this server’s SQLite database."
          control={<Badge variant="secondary">Standalone (SQLite)</Badge>}
        />
        {status.self?.version ? (
          <SettingsRow title="Version" description={status.self.version} />
        ) : null}
      </SettingsSection>
      <ClusterSetup available={available} onComplete={onComplete} />
      <a
        className="text-sm text-fg-muted underline"
        href={CLUSTER_DOCS}
        target="_blank"
        rel="noreferrer"
      >
        Read the cluster guide
      </a>
    </>
  );
}

export function managerURL(n: ClusterNode): string | null {
  for (const addr of n.addrs ?? []) {
    try {
      const u = new URL(addr);
      if ((u.protocol === "http:" || u.protocol === "https:") && !u.username && !u.password)
        return new URL("/ether/cluster", u).href;
    } catch {
      /* skip malformed connections */
    }
  }
  return null;
}

function Nodes({
  status,
  onChanged,
  onAdd,
}: {
  status: ClusterStatus;
  onChanged: () => void;
  onAdd: () => void;
}) {
  const [manage, setManage] = useState(false);
  const nodes = sortNodes(status.nodes ?? []);
  const live = nodes.filter((n) => n.live).length;
  return (
    <Section
      title="Nodes"
      description={`${live} of ${plural(nodes.length, "node")} online. Open a node’s manager to change its local settings.`}
      actions={
        <div className="flex gap-2">
          <Button size="sm" onClick={onAdd}>
            <Plus size={14} /> Add node
          </Button>
          <Button size="sm" variant="outline" onClick={() => setManage(true)}>
            Manage this node
          </Button>
        </div>
      }
    >
      <PagedTable<ClusterNode>
        rows={nodes}
        rowKey={(n) => n.id}
        pageSize={25}
        searchable={nodes.length > 8}
        empty={
          <EmptyState
            title="No nodes reported"
            description="The database couldn't be read. Nodes appear here once it can."
          />
        }
        columns={[
          {
            key: "name",
            header: "Node",
            search: (n) => `${nodeName(n)} ${n.version}`,
            cell: (n) => (
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="font-medium text-fg">{nodeName(n)}</span>
                  {n.leader ? <Badge variant="info">Leader</Badge> : null}
                  {n.id === status.self?.id ? <Badge variant="outline">This node</Badge> : null}
                </div>
                <span className="block text-xs text-fg-muted">
                  Version {n.version || "unknown"}
                </span>
              </div>
            ),
          },
          {
            key: "status",
            header: "Status",
            cell: (n) =>
              n.live && n.draining ? (
                <Badge variant="warning">Paused</Badge>
              ) : n.live ? (
                <Badge variant="success">Online</Badge>
              ) : (
                <span className="flex flex-col items-start gap-0.5">
                  <Badge variant="secondary">Offline</Badge>
                  <span className="text-xs text-fg-muted" title={formatDateTime(n.lastSeen)}>
                    Last seen {relativeTime(n.lastSeen)}
                  </span>
                </span>
              ),
          },
          {
            key: "load",
            header: "Streams",
            align: "right",
            cell: (n) => (
              <span className="tabular-nums" aria-label={`${nodeName(n)} streams`}>
                {n.live ? n.load : "—"}
              </span>
            ),
          },
          {
            key: "transcode",
            header: "Transcoding",
            cell: (n) => <span className="text-fg-muted">{transcodeLabel(n)}</span>,
          },
          {
            key: "weight",
            header: "Weight",
            align: "right",
            cell: (n) => (
              <span className="tabular-nums" aria-label={`${nodeName(n)} weight`}>
                {n.caps?.weight || 1}
              </span>
            ),
          },
          {
            key: "manage",
            header: "",
            cell: (n) =>
              n.id === status.self.id ? null : managerURL(n) ? (
                <a
                  className="text-sm text-primary underline"
                  href={managerURL(n)!}
                  target="_blank"
                  rel="noreferrer"
                >
                  Open manager<span className="sr-only"> for {nodeName(n)}</span>
                </a>
              ) : (
                <span className="text-xs text-fg-muted">No address reported</span>
              ),
          },
          {
            key: "ip",
            header: "Public IP",
            cell: (n) => <code className="text-xs text-fg-muted">{n.caps?.egressIp || "—"}</code>,
          },
        ]}
      />
      {manage ? (
        <NodeDrawer status={status} onClose={() => setManage(false)} onChanged={onChanged} />
      ) : null}
    </Section>
  );
}

function NodeDrawer({
  status,
  onClose,
  onChanged,
}: {
  status: ClusterStatus;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [weight, setWeight] = useState(status.self.caps.weight || 1);
  const [draining, setDraining] = useState(status.self.draining ?? false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const save = async (body: { weight?: number; draining?: boolean }) => {
    setBusy(true);
    setError("");
    try {
      const result = await api.updateClusterNode(body);
      setDraining(result.draining);
      onChanged();
      toast.success("Node updated");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Drawer
      open
      onClose={onClose}
      title="Manage this node"
      description={nodeName(status.self)}
      footer={
        <Button variant="outline" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="space-y-6">
        {error ? (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        ) : null}
        <Field
          label="Node weight"
          htmlFor="cluster-weight"
          hint="Relative share of account-wide provider connections and request budgets. A weight of 2 gets twice the share of a node with weight 1."
        >
          <Input
            id="cluster-weight"
            type="number"
            min={1}
            max={100}
            value={weight}
            onChange={(e) => setWeight(e.target.valueAsNumber)}
          />
        </Field>
        <Button
          disabled={busy || !Number.isInteger(weight) || weight < 1 || weight > 100}
          onClick={() => void save({ weight })}
        >
          Save weight
        </Button>
        <SettingsSection title="New streams">
          <SettingsRow
            title={draining ? "Paused" : "Accepting streams"}
            description="Pausing keeps current streams playing, hands scheduled work to another node and removes this node from published connections. It resumes automatically after a server restart."
          />
        </SettingsSection>
        <Alert variant="warning">
          <AlertDescription>
            Pausing the only available node prevents new playback until you resume it.
          </AlertDescription>
        </Alert>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => void save({ draining: !draining })}
        >
          {draining ? "Resume new streams" : "Pause new streams"}
        </Button>
        <p className="text-sm text-fg-muted">
          Change this node’s port, cache and transcoding hardware from{" "}
          <Link className="underline" to="/server">
            General settings
          </Link>
          . Use{" "}
          <Link className="underline" to="/updates">
            Updates
          </Link>{" "}
          for rolling cluster updates.
        </p>
      </div>
    </Drawer>
  );
}

type SettingsState = AsyncState<ClusterSettings | null>;

function SharedSettings({ status, settings }: { status: ClusterStatus; settings: SettingsState }) {
  const st = settings.data;
  const self = status.self;
  return (
    <SettingsSection title="Shared settings">
      <SettingsRow
        title="This node"
        description={
          self?.id === status.leader
            ? "Leads the cluster: it publishes the server to plex.tv and runs scheduled work."
            : "A cluster member. Scheduled work runs on the leader."
        }
        control={<Badge variant="info">{nodeName(self)}</Badge>}
      />
      <SettingsRow
        title="Settings version"
        description="Settings are stored in the database. A change on any node reaches the others within 30 seconds."
        control={
          st?.sharedVersion !== undefined ? (
            <span className="tabular-nums text-sm text-fg">v{st.sharedVersion}</span>
          ) : null
        }
      />
      <SettingsRow
        title="Credential key"
        description="Fingerprint of the key that encrypts stored credentials. It's the same on every node."
        control={st?.keyFingerprint ? <code className="text-xs">{st.keyFingerprint}</code> : null}
      />
      <SettingsRow
        title="Settings each node keeps"
        description={
          <>
            Port, listen address, log level, cache, transcoding hardware, the public port for its
            port forward and the node's weight are set on each node, on that node's own{" "}
            <Link to="/server" className="underline">
              General
            </Link>{" "}
            page.
          </>
        }
      />
      {settings.error ? (
        <div className="px-5 pb-4">
          <ErrorPanel error={settings.error} onRetry={() => void settings.reload()} />
        </div>
      ) : null}
    </SettingsSection>
  );
}

const TOKEN_STATE: Record<
  TokenState,
  { label: string; variant: "success" | "secondary" | "warning" }
> = {
  unused: { label: "Unused", variant: "success" },
  used: { label: "Used", variant: "secondary" },
  expired: { label: "Expired", variant: "warning" },
};

function JoinTokens({ settings }: { settings: SettingsState }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const tokens = settings.data?.joinTokens ?? [];

  const revoke = async (t: JoinTokenInfo) => {
    const ok = await confirmDialog({
      title: "Revoke join token?",
      message: `Token ${t.id} stops working right away. Nodes that already joined are not affected.`,
      confirmLabel: "Revoke",
      tone: "danger",
    });
    if (!ok) return;
    setBusy(t.id);
    try {
      await api.revokeJoinToken(t.id);
      await settings.reload();
      toast.success("Join token revoked");
    } catch (e) {
      toast.error("Couldn't revoke the token", { description: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  };

  return (
    <Section
      title="Join tokens"
      description="A new node joins with a one-time token created here. Each token works once and expires within the hour."
      actions={
        <Button size="sm" onClick={() => setOpen(true)}>
          <Plus size={14} /> Create join token
        </Button>
      }
    >
      {!settings.data && !settings.error ? (
        <LoadingRows rows={2} />
      ) : (
        <PagedTable<JoinTokenInfo>
          rows={tokens}
          rowKey={(t) => t.id}
          pageSize={10}
          searchable={false}
          empty={
            <EmptyState
              icon={<KeyRound />}
              title="No join tokens"
              description="Create one when you're ready to add a node."
            />
          }
          columns={[
            {
              key: "id",
              header: "Token",
              cell: (t) => <code className="text-xs">{t.id}</code>,
            },
            {
              key: "created",
              header: "Created",
              cell: (t) => (
                <span className="text-fg-muted" title={formatDateTime(t.createdAt)}>
                  {relativeTime(t.createdAt)}
                </span>
              ),
            },
            {
              key: "expires",
              header: "Expires",
              cell: (t) => (
                <span className="text-fg-muted" title={formatDateTime(t.expiresAt)}>
                  {tokenState(t) === "used" ? "—" : relativeTime(t.expiresAt)}
                </span>
              ),
            },
            {
              key: "state",
              header: "Status",
              cell: (t) => {
                const state = tokenState(t);
                return (
                  <span className="flex flex-col items-start gap-0.5">
                    <Badge variant={TOKEN_STATE[state].variant}>{TOKEN_STATE[state].label}</Badge>
                    {state === "used" ? (
                      <span className="text-xs text-fg-muted" title={formatDateTime(t.usedAt)}>
                        {t.usedBy ? `by ${t.usedBy}, ` : ""}
                        {relativeTime(t.usedAt)}
                      </span>
                    ) : null}
                  </span>
                );
              },
            },
            {
              key: "actions",
              header: "",
              align: "right",
              cell: (t) =>
                tokenState(t) === "used" ? null : (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={busy === t.id}
                    aria-label={`Revoke token ${t.id}`}
                    onClick={() => void revoke(t)}
                  >
                    Revoke
                  </Button>
                ),
            },
          ]}
        />
      )}
      {open ? (
        <JoinTokenDrawer onClose={() => setOpen(false)} onCreated={() => void settings.reload()} />
      ) : null}
    </Section>
  );
}

const TTL_OPTIONS = [
  { value: "15", label: "15 minutes" },
  { value: "30", label: "30 minutes" },
  { value: "60", label: "1 hour" },
];

function JoinTokenDrawer({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [ttl, setTtl] = useState("60");
  const [busy, setBusy] = useState(false);
  // Held only while the drawer is open: the server never returns it again.
  const [created, setCreated] = useState<CreatedJoinToken | null>(null);

  const create = async () => {
    setBusy(true);
    try {
      setCreated(await api.createJoinToken(Number(ttl)));
      onCreated();
    } catch (e) {
      toast.error("Couldn't create a join token", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer
      open
      onClose={onClose}
      title="Create join token"
      description="Lets one new machine join this cluster."
      size="lg"
      footer={
        created ? (
          <Button variant="outline" onClick={onClose}>
            Done
          </Button>
        ) : (
          <Button disabled={busy} onClick={() => void create()}>
            {busy ? "Creating…" : "Create join token"}
          </Button>
        )
      }
    >
      <div className="space-y-5">
        <Field
          label="Valid for"
          hint="Use the token before it expires; you can always create another."
        >
          {created ? (
            <p className="text-sm text-fg">Expires {formatDateTime(created.expiresAt)}</p>
          ) : (
            <Segmented ariaLabel="Valid for" options={TTL_OPTIONS} value={ttl} onChange={setTtl} />
          )}
        </Field>
        {created ? (
          <>
            <Alert variant="warning" role="alert">
              <AlertTitle>Keep this token secret</AlertTitle>
              <AlertDescription>
                {created.warning ||
                  "It contains the database password and the key that decrypts every stored credential."}{" "}
                It's shown only once: after you close this drawer it can't be shown again.
              </AlertDescription>
            </Alert>
            <Field label="Join token">
              <Textarea
                aria-label="Join token"
                readOnly
                rows={4}
                value={created.token}
                className="font-mono text-xs break-all"
              />
            </Field>
            <Button variant="outline" size="sm" onClick={() => copy(created.token, "Token")}>
              <Copy size={14} /> Copy token
            </Button>
            <Field
              label="On the new machine"
              hint="Open its Server Manager, choose Cluster → Join cluster and paste this token. Ether checks the connection, backs up its data and restarts automatically."
            >
              <p className="text-sm text-fg-muted">
                Install and open Ether on the new machine. You can join before completing its
                standalone setup.
              </p>
            </Field>
          </>
        ) : (
          <p className="text-sm text-fg-muted">
            Paste the token into Cluster → Join cluster on the new machine. It is shown once and
            contains the database password and the credential key, so treat it like a password.
          </p>
        )}
      </div>
    </Drawer>
  );
}
