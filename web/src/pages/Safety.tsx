import { ListInput } from "../components/fields";
import { Drawer } from "../components/Drawer";
import { EditPanel } from "../components/EditPanel";
import {
  Button,
  Input,
  PageContainer,
  PageHeader,
  SubtabNav,
  Spinner,
  toast,
  Switch,
} from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { api, apiFetch, errorMessage } from "../lib/api";
import { RemoteDiagnostics } from "../components/RemoteDiagnostics";
import type { EtherUser } from "../lib/types";

type Policy = {
  maxContentRating: string;
  blockedGenres: string[];
  blockedKeywords: string[];
  remoteDisabled: boolean;
  maxBitrateKbps: number;
  streamLimit: number;
  allowedHours: number[];
  timezone: string;
};
type Invite = {
  id: number;
  label: string;
  expires: string;
  maxUses: number;
  uses: number;
  revoked: boolean;
};
type Clean = {
  enabled: boolean;
  error: string;
  lastApplied: string;
  sources: { key: string; value: string }[];
  scope: string;
};
const empty: Policy = {
  maxContentRating: "",
  blockedGenres: [],
  blockedKeywords: [],
  remoteDisabled: false,
  maxBitrateKbps: 0,
  streamLimit: 0,
  allowedHours: [],
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
};
const panel = "space-y-4 rounded-xl border border-border bg-surface p-5";
const select = "block w-full rounded-md border border-border bg-surface px-3 py-2 text-sm";
export default function Safety() {
  const [policyReady, setPolicyReady] = useState(false);
  const [savedPolicy, setSavedPolicy] = useState<Policy>(empty);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [policyTab, setPolicyTab] = useState("content");
  const [tab, setTab] = useState("viewers");
  const [users, setUsers] = useState<EtherUser[]>([]);
  const [viewer, setViewer] = useState(0);
  const [policy, setPolicy] = useState<Policy>(empty);
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [clean, setClean] = useState<Clean | null>(null);
  const [invites, setInvites] = useState<Invite[]>([]);
  const [label, setLabel] = useState("");
  const [days, setDays] = useState(7);
  const [uses, setUses] = useState(1);
  const [link, setLink] = useState("");
  const perform = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const reloadInvites = async () => setInvites(await apiFetch<Invite[]>("/safety/invites/"));
  useEffect(() => {
    void Promise.all([
      api.users().then((v) => {
        setUsers(v);
        setViewer(v[0]?.id ?? 0);
      }),
      apiFetch<Clean>("/safety/clean-home/").then(setClean),
      reloadInvites(),
    ])
      .catch((e) => toast.error(errorMessage(e)))
      .finally(() => setLoaded(true));
  }, []);
  useEffect(() => {
    setPolicyReady(false);
    if (!viewer) return;
    const controller = new AbortController();
    void apiFetch<Policy>(`/safety/policies/${viewer}`, { signal: controller.signal })
      .then((p) => {
        if (!controller.signal.aborted) {
          const next = { ...empty, ...p };
          setPolicy(next);
          setSavedPolicy(next);
          setPolicyReady(true);
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) toast.error(errorMessage(e));
      });
    return () => controller.abort();
  }, [viewer]);
  const patch = (p: Partial<Policy>) => setPolicy((v) => ({ ...v, ...p }));
  return (
    <PageContainer className="space-y-6">
      <PageHeader
        title="Safety & sharing"
        description="Protect each viewer, invite friends, and keep Plex Home focused on your server."
      />
      <div className="border-b border-border">
        <SubtabNav
          ariaLabel="Safety tools"
          active={tab}
          onSelect={(key) => setTab(key as typeof tab)}
          tabs={[
            { key: "viewers", label: "Viewer policies" },
            { key: "invites", label: "Invitations" },
            { key: "home", label: "Clean Home" },
            { key: "diagnostics", label: "Remote & security" },
          ]}
        />
      </div>
      {!loaded ? (
        <Spinner label="Loading policies" />
      ) : tab === "diagnostics" ? (
        <RemoteDiagnostics />
      ) : tab === "viewers" ? (
        <>
          {" "}
          <label className="block text-sm">
            Viewer
            <select
              className={select}
              value={viewer}
              onChange={(e) => setViewer(Number(e.target.value))}
            >
              {users.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.title || u.username}
                </option>
              ))}
            </select>
          </label>
          <EditPanel
            onOpen={() => setPolicy(savedPolicy)}
            onDiscard={() => setPolicy(savedPolicy)}
            title="Viewer policy"
            description="Choose content restrictions and playback limits for one person."
            action="Edit viewer policy"
            summary={
              users.find((u) => u.id === viewer)?.title ||
              users.find((u) => u.id === viewer)?.username ||
              "Select a viewer"
            }
          >
            <section className={panel}>
              {!policyReady && <p role="status">Loading viewer policy…</p>}
              <SubtabNav
                ariaLabel="Policy settings"
                active={policyTab}
                onSelect={setPolicyTab}
                tabs={[
                  { key: "content", label: "Content" },
                  { key: "playback", label: "Playback limits" },
                  { key: "schedule", label: "Allowed times" },
                ]}
              />
              {policyTab === "content" && (
                <>
                  <div className="flex flex-wrap gap-2">
                    {[
                      ["preschool", "Young children"],
                      ["family", "Family"],
                      ["teens", "Teens"],
                    ].map(([age, name]) => (
                      <Button
                        variant="outline"
                        key={age}
                        onClick={() =>
                          void perform(async () =>
                            setPolicy({
                              ...empty,
                              ...(await apiFetch<Policy>(
                                `/safety/policies/presets/kids?age=${age}`,
                              )),
                            }),
                          )
                        }
                      >
                        {name}
                      </Button>
                    ))}
                  </div>
                  <p className="text-sm text-fg-muted">
                    Adult titles are always excluded. Unrated titles are blocked when a rating limit
                    applies. Existing library access and stricter viewer settings continue to apply.
                  </p>
                  <label className="block text-sm">
                    Maximum content rating
                    <select
                      className={select}
                      value={policy.maxContentRating}
                      onChange={(e) => patch({ maxContentRating: e.target.value })}
                    >
                      {["", "TV-Y", "TV-Y7", "G", "PG", "PG-13", "TV-14", "R", "TV-MA"].map((r) => (
                        <option key={r} value={r}>
                          {r || "Unrestricted"}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="block text-sm">
                    Blocked genres, comma separated
                    <ListInput
                      value={policy.blockedGenres}
                      onChange={(v) => patch({ blockedGenres: v.map(String) })}
                    />
                  </label>
                  <label className="block text-sm">
                    Blocked keywords, comma separated
                    <ListInput
                      value={policy.blockedKeywords}
                      onChange={(v) => patch({ blockedKeywords: v.map(String) })}
                    />
                  </label>
                </>
              )}{" "}
              {policyTab === "playback" && (
                <>
                  <label className="flex items-center gap-2 text-sm">
                    <Switch
                      type="checkbox"
                      checked={policy.remoteDisabled}
                      onChange={(e) => patch({ remoteDisabled: e.target.checked })}
                    />
                    Allow playback only on the local network
                  </label>
                  <div className="grid gap-4 md:grid-cols-2">
                    <label className="block text-sm">
                      Maximum bitrate (kbps, 0 uses server default)
                      <Input
                        type="number"
                        min={0}
                        value={policy.maxBitrateKbps}
                        onChange={(e) => patch({ maxBitrateKbps: Number(e.target.value) })}
                      />
                    </label>
                    <label className="block text-sm">
                      Concurrent streams (0 uses server default)
                      <Input
                        type="number"
                        min={0}
                        value={policy.streamLimit}
                        onChange={(e) => patch({ streamLimit: Number(e.target.value) })}
                      />
                    </label>
                  </div>
                </>
              )}{" "}
              {policyTab === "schedule" && (
                <>
                  <label className="block text-sm">
                    Playback timezone
                    <Input
                      value={policy.timezone}
                      onChange={(e) => patch({ timezone: e.target.value })}
                    />
                  </label>
                  <p className="text-sm text-fg-muted">
                    Allowed hours: leave all clear for any time.
                  </p>
                  <div className="flex flex-wrap gap-2">
                    {Array.from({ length: 24 }, (_, h) => (
                      <Button
                        key={h}
                        size="sm"
                        variant={policy.allowedHours?.includes(h) ? "default" : "outline"}
                        aria-pressed={policy.allowedHours?.includes(h)}
                        onClick={() =>
                          patch({
                            allowedHours: policy.allowedHours?.includes(h)
                              ? policy.allowedHours.filter((x) => x !== h)
                              : [...(policy.allowedHours ?? []), h],
                          })
                        }
                      >
                        {String(h).padStart(2, "0")}:00
                      </Button>
                    ))}
                  </div>
                </>
              )}{" "}
              <Button
                disabled={busy || !viewer || !policyReady}
                onClick={() =>
                  void perform(async () => {
                    await apiFetch(`/safety/policies/${viewer}`, {
                      method: "PUT",
                      body: JSON.stringify(policy),
                    });
                    setSavedPolicy(policy);
                    toast.success("Viewer policy saved");
                  })
                }
              >
                Save policy
              </Button>
            </section>
          </EditPanel>
        </>
      ) : tab === "invites" ? (
        <section className={panel}>
          <div className="flex items-center justify-between gap-3">
            <h2 className="font-semibold">Invitations</h2>
            <Button
              onClick={() => {
                setLink("");
                setLabel("");
                setInviteOpen(true);
              }}
            >
              Invite a friend
            </Button>
          </div>
          <Drawer
            open={inviteOpen}
            onClose={() => setInviteOpen(false)}
            title="Invite a friend"
            size="lg"
          >
            {" "}
            <h2 className="text-lg font-semibold">Invite a friend</h2>
            <p className="text-sm text-fg-muted">
              Choose whose policy to copy into this invitation. Plex account entitlements continue
              to apply. New friends receive access to Movies and TV Shows.
            </p>
            <label className="block text-sm">
              Copy policy from
              <select
                className={select}
                value={viewer}
                onChange={(e) => setViewer(Number(e.target.value))}
              >
                {users.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.title || u.username}
                  </option>
                ))}
              </select>
            </label>
            <label className="block text-sm">
              Invitation label
              <Input value={label} onChange={(e) => setLabel(e.target.value)} />
            </label>
            <div className="grid gap-4 md:grid-cols-2">
              <label className="text-sm">
                Valid for days
                <Input
                  type="number"
                  min={1}
                  max={90}
                  value={days}
                  onChange={(e) => setDays(Number(e.target.value))}
                />
              </label>
              <label className="text-sm">
                Maximum people
                <Input
                  type="number"
                  min={1}
                  max={1000}
                  value={uses}
                  onChange={(e) => setUses(Number(e.target.value))}
                />
              </label>
            </div>
            <Button
              disabled={busy || !policyReady}
              onClick={() =>
                void perform(async () => {
                  const result = await apiFetch<{ path: string }>("/safety/invites/", {
                    method: "POST",
                    body: JSON.stringify({
                      label,
                      expires: new Date(Date.now() + days * 86400000).toISOString(),
                      maxUses: uses,
                      sections: [1, 2],
                      policy: savedPolicy,
                      streamLimit: savedPolicy.streamLimit,
                    }),
                  });
                  setLink(location.origin + result.path);
                  await reloadInvites();
                })
              }
            >
              Create invitation
            </Button>
            {link && (
              <div className="space-y-2">
                <Input aria-label="Invitation link" value={link} readOnly />
                <Button
                  variant="outline"
                  onClick={() =>
                    void navigator.clipboard
                      .writeText(link)
                      .then(() => toast.success("Invitation copied"))
                      .catch(() => toast.error("Select and copy the invitation link"))
                  }
                >
                  Copy link
                </Button>
                <p className="text-xs text-fg-muted">
                  Use an address your friend can reach. Invitation links are displayed once.
                </p>
              </div>
            )}
          </Drawer>{" "}
          {invites.map((invite) => (
            <div
              key={invite.id}
              className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3"
            >
              <div>
                <p>{invite.label || "Invitation"}</p>
                <p className="text-xs text-fg-muted">
                  {invite.uses}/{invite.maxUses} joined · expires{" "}
                  {new Date(invite.expires).toLocaleDateString()}
                  {invite.revoked ? " · revoked" : ""}
                </p>
              </div>
              <Button
                variant="outline"
                disabled={busy || invite.revoked}
                onClick={() =>
                  void perform(async () => {
                    await apiFetch(`/safety/invites/${invite.id}`, { method: "DELETE" });
                    await reloadInvites();
                  })
                }
              >
                Revoke
              </Button>
            </div>
          ))}
        </section>
      ) : (
        <section className={panel}>
          <h2 className="text-lg font-semibold">Automatic Clean Home</h2>
          <p className="text-sm text-fg-muted">
            Ether automatically disables Plex-owned online sources for the owner and managed Home
            accounts. Your own channels and libraries remain available. Shared friends’ account
            settings require their account authorization.
          </p>
          <p>
            {clean?.enabled ? "Enabled" : "Disabled"}
            {clean?.lastApplied && !clean.lastApplied.startsWith("0001")
              ? ` · last checked ${new Date(clean.lastApplied).toLocaleString()}`
              : ""}
          </p>
          {clean?.error && (
            <p className="text-sm text-danger" role="alert">
              {clean.error}
            </p>
          )}
          <Button
            variant="outline"
            disabled={busy}
            onClick={() =>
              void perform(async () =>
                setClean(
                  await apiFetch<Clean>("/safety/clean-home/", { method: "POST", body: "{}" }),
                ),
              )
            }
          >
            Apply now
          </Button>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() =>
              void perform(async () => {
                const c = await apiFetch<{ value: Record<string, unknown> }>("/config/plex");
                await apiFetch("/config/plex", {
                  method: "PUT",
                  body: JSON.stringify({ ...c.value, disableCleanHome: clean?.enabled }),
                });
                setClean((v) => (v ? { ...v, enabled: !v.enabled } : v));
              })
            }
          >
            {clean?.enabled ? "Turn off automatic cleanup" : "Turn on automatic cleanup"}
          </Button>
        </section>
      )}
    </PageContainer>
  );
}
