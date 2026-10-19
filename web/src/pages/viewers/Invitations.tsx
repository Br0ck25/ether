import {
  Badge,
  Button,
  confirmDialog,
  EmptyState,
  Input,
  PagedTable,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Copy, MailPlus, UserPlus } from "lucide-react";
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Drawer } from "../../components/Drawer";
import { Field, NumberInput, Segmented } from "../../components/fields";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { useViewers, viewerName } from "../../components/pickers";
import { apiFetch, errorMessage } from "../../lib/api";
import { formatDateTime } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import { emptyPolicy, KID_PRESETS, normalizePolicy, type SafetyPolicy } from "./shared";

type Invite = {
  id: number;
  label: string;
  expires: string;
  maxUses: number;
  uses: number;
  revoked: boolean;
};

function inviteStatus(i: Invite): { label: string; variant: "success" | "secondary" | "warning" } {
  if (i.revoked) return { label: "Revoked", variant: "secondary" };
  if (new Date(i.expires).getTime() < Date.now()) return { label: "Expired", variant: "warning" };
  if (i.maxUses > 0 && i.uses >= i.maxUses) return { label: "Used up", variant: "secondary" };
  return { label: "Active", variant: "success" };
}

/** Invitation links for friends, each carrying the parental controls new people start with. */
export function Invitations() {
  const [params, setParams] = useSearchParams();
  const copyFrom = Number(params.get("copy")) || 0;
  const invites = useAsync(
    async (signal) => (await apiFetch<Invite[] | null>("/safety/invites/", { signal })) ?? [],
    [],
  );
  const [open, setOpen] = useState(copyFrom > 0);
  const [busy, setBusy] = useState<number | null>(null);
  useEffect(() => {
    if (copyFrom > 0) setOpen(true);
  }, [copyFrom]);

  const close = () => {
    setOpen(false);
    if (params.has("copy")) {
      const next = new URLSearchParams(params);
      next.delete("copy");
      setParams(next, { replace: true });
    }
  };

  const revoke = async (invite: Invite) => {
    const ok = await confirmDialog({
      title: "Revoke invitation?",
      message: `${invite.label || "This invitation"} stops working right away. People who already joined keep their access.`,
      confirmLabel: "Revoke",
      tone: "danger",
    });
    if (!ok) return;
    setBusy(invite.id);
    try {
      await apiFetch(`/safety/invites/${invite.id}`, { method: "DELETE" });
      await invites.reload();
      toast.success("Invitation revoked");
    } catch (e) {
      toast.error("Couldn't revoke", { description: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-2xl text-sm text-fg-muted">
          Share a link to give a friend access to Movies and TV Shows. Plex account entitlements
          still apply.
        </p>
        <Button onClick={() => setOpen(true)}>
          <UserPlus size={14} /> Invite a friend
        </Button>
      </div>
      {invites.error ? (
        <ErrorPanel error={invites.error} onRetry={() => void invites.reload()} />
      ) : !invites.data ? (
        <LoadingRows rows={3} />
      ) : (
        <PagedTable<Invite>
          rows={invites.data}
          rowKey={(i) => String(i.id)}
          pageSize={15}
          empty={
            <EmptyState
              icon={<MailPlus />}
              title="No invitations yet"
              description="Invite a friend to create a link you can send them."
            />
          }
          columns={[
            {
              key: "label",
              header: "Invitation",
              sortValue: (i) => (i.label || "Invitation").toLowerCase(),
              cell: (i) => <span className="font-medium text-fg">{i.label || "Invitation"}</span>,
            },
            {
              key: "joined",
              header: "Joined",
              sortValue: (i) => i.uses,
              cell: (i) => (
                <span className="tabular-nums text-fg-muted">
                  {i.uses} of {i.maxUses}
                </span>
              ),
            },
            {
              key: "expires",
              header: "Expires",
              sortValue: (i) => new Date(i.expires).getTime(),
              cell: (i) => (
                <span className="text-fg-muted" title={formatDateTime(i.expires)}>
                  {new Date(i.expires).toLocaleDateString()}
                </span>
              ),
            },
            {
              key: "status",
              header: "Status",
              cell: (i) => {
                const s = inviteStatus(i);
                return <Badge variant={s.variant}>{s.label}</Badge>;
              },
            },
            {
              key: "actions",
              header: "",
              align: "right",
              cell: (i) => (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={i.revoked || busy === i.id}
                  onClick={() => void revoke(i)}
                  aria-label={`Revoke ${i.label || "invitation"}`}
                >
                  Revoke
                </Button>
              ),
            },
          ]}
        />
      )}
      {open ? (
        <InviteDrawer copyFrom={copyFrom} onClose={close} onCreated={() => void invites.reload()} />
      ) : null}
    </div>
  );
}

type Start = "none" | "copy" | (typeof KID_PRESETS)[number]["value"];

function InviteDrawer({
  copyFrom,
  onClose,
  onCreated,
}: {
  /** Viewer whose saved parental controls the invitation starts from (0 for none). */
  copyFrom: number;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { viewers } = useViewers();
  const source = copyFrom ? viewers?.find((v) => v.id === copyFrom) : undefined;
  const [label, setLabel] = useState("");
  const [days, setDays] = useState(7);
  const [uses, setUses] = useState(1);
  const [start, setStart] = useState<Start>(copyFrom ? "copy" : "none");
  const [policy, setPolicy] = useState<SafetyPolicy | null>(copyFrom ? null : emptyPolicy());
  const [busy, setBusy] = useState(false);
  const [link, setLink] = useState("");

  const choose = async (next: Start) => {
    setStart(next);
    if (next === "none") {
      setPolicy(emptyPolicy());
      return;
    }
    setPolicy(null);
    try {
      const path =
        next === "copy"
          ? `/safety/policies/${copyFrom}`
          : `/safety/policies/presets/kids?age=${next}`;
      setPolicy(normalizePolicy(await apiFetch<SafetyPolicy>(path)));
    } catch (e) {
      toast.error("Couldn't load parental controls", { description: errorMessage(e) });
    }
  };
  useEffect(() => {
    if (copyFrom) void choose("copy");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [copyFrom]);

  const create = async () => {
    if (!policy) return;
    setBusy(true);
    try {
      const result = await apiFetch<{ path: string }>("/safety/invites/", {
        method: "POST",
        body: JSON.stringify({
          label,
          expires: new Date(Date.now() + days * 86400000).toISOString(),
          maxUses: uses,
          sections: [1, 2],
          policy,
          streamLimit: policy.streamLimit,
        }),
      });
      setLink(location.origin + result.path);
      onCreated();
    } catch (e) {
      toast.error("Couldn't create the invitation", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };

  const options: { value: Start; label: string }[] = [
    { value: "none", label: "No restrictions" },
    ...KID_PRESETS.map((p) => ({ value: p.value as Start, label: p.label })),
    ...(copyFrom
      ? [
          {
            value: "copy" as Start,
            label: source ? `Same as ${viewerName(source)}` : "Same as viewer",
          },
        ]
      : []),
  ];

  return (
    <Drawer
      open
      onClose={onClose}
      title="Invite a friend"
      description="New friends get access to Movies and TV Shows."
      size="lg"
      footer={
        link ? (
          <Button variant="outline" onClick={onClose}>
            Done
          </Button>
        ) : (
          <Button disabled={busy || !policy} onClick={() => void create()}>
            {busy ? "Creating…" : "Create invitation"}
          </Button>
        )
      }
    >
      <div className="space-y-5">
        <Field label="Label" hint="Only you see this, e.g. who the link is for.">
          <Input
            aria-label="Invitation label"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            disabled={!!link}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Valid for days">
            <NumberInput
              aria-label="Valid for days"
              min={1}
              max={90}
              value={days}
              onChange={(n) => setDays(Math.min(90, Math.max(1, Math.round(n))))}
              disabled={!!link}
            />
          </Field>
          <Field label="Maximum people">
            <NumberInput
              aria-label="Maximum people"
              min={1}
              max={1000}
              value={uses}
              onChange={(n) => setUses(Math.min(1000, Math.max(1, Math.round(n))))}
              disabled={!!link}
            />
          </Field>
        </div>
        <Field
          label="Parental controls"
          hint="What new friends start with. Change it later on their profile."
        >
          <Segmented
            ariaLabel="Parental controls"
            options={options}
            value={start}
            onChange={(v) => void choose(v)}
          />
        </Field>
        {link ? (
          <div className="space-y-2 rounded-lg border border-border bg-bg-subtle p-3">
            <Input aria-label="Invitation link" value={link} readOnly />
            <div className="flex flex-wrap items-center gap-3">
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  void navigator.clipboard
                    .writeText(link)
                    .then(() => toast.success("Invitation copied"))
                    .catch(() => toast.error("Select and copy the invitation link"))
                }
              >
                <Copy size={14} /> Copy link
              </Button>
              <p className="text-xs text-fg-muted">
                Use an address your friend can reach. The link is shown only once.
              </p>
            </div>
          </div>
        ) : null}
      </div>
    </Drawer>
  );
}
