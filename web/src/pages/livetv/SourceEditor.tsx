import { Badge, Button, Input, confirmDialog, toast } from "@braedonsaunders/appkit-ui";
import {
  AlertTriangle,
  CalendarRange,
  FileUp,
  KeyRound,
  Link2,
  ListVideo,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { useEffect, useState } from "react";
import { Field, SecretInput, Segmented } from "../../components/fields";
import { errorMessage } from "../../lib/api";
import { formatNumber, relativeTime } from "../../lib/format";
import {
  dayClock,
  duration,
  emptySource,
  tv,
  type AuthType,
  type Source,
  type SourceAuth,
  type SourceDraft,
} from "./model";

export type SourceKind = "playlist" | "guide";

async function readUpload(file: File): Promise<string> {
  if (/\.gz$/i.test(file.name) && typeof DecompressionStream !== "undefined") {
    const stream = file.stream().pipeThrough(new DecompressionStream("gzip"));
    return new Response(stream).text();
  }
  return file.text();
}

/** Create or edit an M3U playlist source or a guide-only XMLTV source, in place. */
export function SourceEditor({
  source,
  kind: initialKind,
  onSaved,
  onDeleted,
}: {
  source: Source | null;
  kind: SourceKind;
  onSaved: (s: Source) => Promise<void> | void;
  onDeleted: () => Promise<void> | void;
}) {
  const [draft, setDraft] = useState<SourceDraft>(emptySource());
  const [kind, setKind] = useState<SourceKind>(initialKind);
  const [busy, setBusy] = useState<"" | "save" | "refresh" | "delete">("");
  const [error, setError] = useState("");
  useEffect(() => {
    setDraft(
      source
        ? {
            ...emptySource(),
            id: source.id,
            name: source.name,
            playlistUrl: source.playlistUrl ?? "",
            guideUrl: source.guideUrl ?? "",
            refreshHours: source.refreshHours,
            auth: source.auth ? structuredClone(source.auth) : undefined,
          }
        : emptySource(),
    );
    setKind(
      source
        ? source.hasPlaylist === false ||
          (!source.playlistUrl && !source.channelCount && source.hasGuide)
          ? "guide"
          : "playlist"
        : initialKind,
    );
    setError("");
  }, [source, initialKind]);

  const upload = async (file: File | undefined, key: "playlist" | "guide") => {
    if (!file) return;
    const limit = key === "playlist" ? 16 * 1024 * 1024 : 64 * 1024 * 1024;
    try {
      const text = await readUpload(file);
      if (text.length > limit) throw new Error("The selected file exceeds the import size limit.");
      setDraft((s) => ({
        ...s,
        [key]: text,
        [key === "playlist" ? "playlistUrl" : "guideUrl"]: "",
      }));
    } catch (e) {
      setError(errorMessage(e));
    }
  };

  const auth = draft.auth ?? {};
  const xtream = kind === "playlist" && auth.type === "xtream";
  const setAuth = (patch: SourceAuth) => setDraft((d) => ({ ...d, auth: { ...d.auth, ...patch } }));
  const canSave =
    draft.name.trim() !== "" &&
    (xtream
      ? Boolean(auth.server?.trim() && auth.username?.trim() && auth.password)
      : kind === "playlist"
        ? Boolean(draft.playlist || draft.playlistUrl)
        : Boolean(draft.guide || draft.guideUrl));

  const save = async () => {
    setBusy("save");
    setError("");
    try {
      const body: SourceDraft =
        kind === "guide" || xtream ? { ...draft, playlist: "", playlistUrl: "" } : { ...draft };
      body.auth = cleanAuth(draft.auth, kind);
      const saved = await tv.saveSource(body);
      toast.success(source ? "Source re-imported" : "Source imported", {
        description: "Channel numbers, mappings and access rules are kept.",
      });
      await onSaved(saved);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  const refresh = async () => {
    if (!source) return;
    setBusy("refresh");
    setError("");
    try {
      const s = await tv.refreshSource(source.id);
      toast.success("Source refreshed", { description: "Fresh listings are in the guide." });
      await onSaved(s);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy("");
    }
  };

  const remove = async () => {
    if (!source) return;
    const ok = await confirmDialog({
      title: `Delete ${source.name}?`,
      message:
        kind === "playlist"
          ? "Its channels and their listings are removed from Live TV."
          : "Channels mapped to this guide lose their listings.",
      confirmLabel: "Delete source",
      tone: "danger",
    });
    if (!ok) return;
    setBusy("delete");
    try {
      await tv.deleteSource(source.id);
      toast.success("Source deleted");
      await onDeleted();
    } catch (e) {
      setError(errorMessage(e));
      setBusy("");
    }
  };

  const auto = (draft.refreshHours ?? 0) >= 0;
  const urlBacked = xtream || Boolean(draft.playlistUrl || draft.guideUrl);

  return (
    <div className="mx-auto max-w-3xl space-y-5 p-3 sm:p-6">
      <div className="flex items-start gap-4">
        <span
          className={
            kind === "guide"
              ? "grid size-12 shrink-0 place-items-center rounded-xl bg-info/15 text-info"
              : "grid size-12 shrink-0 place-items-center rounded-xl bg-primary-subtle text-primary"
          }
        >
          {kind === "guide" ? <CalendarRange size={22} /> : <ListVideo size={22} />}
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-xl font-semibold tracking-tight text-fg">
            {source ? source.name : kind === "guide" ? "New guide source" : "New IPTV source"}
          </h2>
          <p className="text-sm text-fg-muted">
            {kind === "guide"
              ? "An XMLTV guide on its own. Map any channel to it for listings."
              : "An M3U playlist of live streams, with an optional XMLTV guide."}
          </p>
        </div>
        {source ? (
          <div className="flex shrink-0 gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={Boolean(busy) || !urlBacked}
              onClick={() => void refresh()}
              title={urlBacked ? undefined : "Uploaded files can't be refreshed; upload again"}
            >
              <RefreshCw size={13} className={busy === "refresh" ? "animate-spin" : ""} /> Refresh
              now
            </Button>
          </div>
        ) : null}
      </div>

      {source ? (
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          {[
            kind === "guide"
              ? [
                  "Coverage",
                  source.guideFrom
                    ? `${duration(Date.parse(source.guideUntil ?? source.guideFrom) - Date.parse(source.guideFrom))}`
                    : "—",
                ]
              : ["Channels", formatNumber(source.channelCount ?? 0)],
            ["Guide channels", formatNumber(source.guideChannelCount ?? 0)],
            ["Listings", formatNumber(source.programmeCount ?? 0)],
            ["Guide runs to", source.guideUntil ? dayClock(source.guideUntil) : "—"],
          ].map(([k, v]) => (
            <div key={k} className="rounded-xl border border-border bg-surface px-3 py-2.5">
              <p className="text-[11px] font-medium uppercase tracking-wider text-fg-subtle">{k}</p>
              <p className="mt-0.5 truncate text-lg font-semibold tabular-nums text-fg">{v}</p>
            </div>
          ))}
        </div>
      ) : null}

      {source?.lastError ? (
        <div className="flex items-start gap-2 rounded-xl border border-danger/40 bg-danger/10 p-3 text-sm">
          <AlertTriangle size={15} className="mt-0.5 shrink-0 text-danger" />
          <div>
            <p className="font-medium text-fg">The last automatic refresh failed</p>
            <p className="text-fg-muted">
              {source.lastError}. The previous listings are still in use.
            </p>
          </div>
        </div>
      ) : null}

      <section className="space-y-4 rounded-2xl border border-border bg-surface p-5">
        {!source ? (
          <Segmented
            ariaLabel="Source type"
            value={kind}
            onChange={setKind}
            options={[
              { value: "playlist", label: "M3U playlist", icon: <ListVideo size={13} /> },
              { value: "guide", label: "Guide only (XMLTV)", icon: <CalendarRange size={13} /> },
            ]}
          />
        ) : null}
        <Field label="Source name" htmlFor="src-name">
          <Input
            id="src-name"
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.currentTarget.value })}
            placeholder={kind === "guide" ? "e.g. Regional listings" : "e.g. My IPTV provider"}
          />
        </Field>
        {kind === "playlist" ? (
          <Field label="Connect with">
            <Segmented
              ariaLabel="Connect with"
              size="sm"
              value={xtream ? "xtream" : "url"}
              onChange={(v) =>
                setDraft((d) => ({
                  ...d,
                  auth:
                    v === "xtream"
                      ? { ...d.auth, type: "xtream" }
                      : { ...d.auth, type: d.auth?.type === "xtream" ? "" : d.auth?.type },
                }))
              }
              options={[
                { value: "url", label: "Playlist URL or file", icon: <Link2 size={13} /> },
                { value: "xtream", label: "Xtream Codes login", icon: <KeyRound size={13} /> },
              ]}
            />
          </Field>
        ) : null}
        {xtream ? (
          <div className="space-y-4 rounded-xl border border-border bg-bg-subtle/60 p-4">
            <Field
              label="Server"
              htmlFor="xt-server"
              hint="The panel address your provider gave you, e.g. http://provider.example:8080"
            >
              <Input
                id="xt-server"
                value={auth.server ?? ""}
                onChange={(e) => setAuth({ server: e.currentTarget.value })}
                placeholder="http://provider.example:8080"
              />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Username" htmlFor="xt-user">
                <Input
                  id="xt-user"
                  value={auth.username ?? ""}
                  autoComplete="off"
                  onChange={(e) => setAuth({ username: e.currentTarget.value })}
                />
              </Field>
              <Field label="Password" htmlFor="xt-pass">
                <SecretInput
                  id="xt-pass"
                  value={auth.password ?? ""}
                  onChange={(v) => setAuth({ password: v })}
                />
              </Field>
            </div>
            <Field
              label="Stream format"
              hint="TS works with almost every panel; pick HLS if yours prefers it."
            >
              <Segmented
                ariaLabel="Stream format"
                size="sm"
                value={auth.output ?? "ts"}
                onChange={(v) => setAuth({ output: v })}
                options={[
                  { value: "ts", label: "MPEG-TS" },
                  { value: "m3u8", label: "HLS" },
                ]}
              />
            </Field>
          </div>
        ) : null}
        {kind === "playlist" && !xtream ? (
          <UrlOrFile
            id="src-m3u"
            label="M3U playlist"
            url={draft.playlistUrl}
            fileReady={Boolean(draft.playlist)}
            accept=".m3u,.m3u8,text/plain"
            placeholder="https://…/playlist.m3u"
            onUrl={(v) => setDraft({ ...draft, playlistUrl: v, playlist: "" })}
            onFile={(f) => void upload(f, "playlist")}
          />
        ) : null}
        <UrlOrFile
          id="src-xmltv"
          label={
            xtream
              ? "XMLTV guide override (optional)"
              : kind === "playlist"
                ? "XMLTV guide (optional)"
                : "XMLTV guide"
          }
          url={draft.guideUrl}
          fileReady={Boolean(draft.guide)}
          accept=".xml,.gz,application/xml,text/xml,application/gzip"
          placeholder={
            xtream ? "Leave blank to use the panel's guide" : "https://…/guide.xml or .xml.gz"
          }
          hint={
            xtream
              ? "Ether uses the panel's own XMLTV export unless you set one here."
              : "Gzipped guides (.xml.gz) are fine."
          }
          onUrl={(v) => setDraft({ ...draft, guideUrl: v, guide: "" })}
          onFile={(f) => void upload(f, "guide")}
        />
        {!xtream ? <SignIn auth={auth} onChange={setAuth} /> : null}
        <ClientIdentity auth={auth} onChange={setAuth} />
        <Field
          label="Keep it fresh"
          hint={
            !urlBacked
              ? "Uploaded files stay as they are until you upload again."
              : auto
                ? `Refreshes on its own before the listings run out${source?.nextRefresh ? ` · next ${relativeTime(source.nextRefresh)}` : ""}.`
                : "Only refreshes when you press Refresh now."
          }
        >
          <Segmented
            ariaLabel="Refresh"
            size="sm"
            value={auto ? "auto" : "manual"}
            onChange={(v) => setDraft({ ...draft, refreshHours: v === "auto" ? 0 : -1 })}
            options={[
              { value: "auto", label: "Automatic" },
              { value: "manual", label: "Manual" },
            ]}
          />
        </Field>
        {error ? (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        ) : null}
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <Button disabled={!canSave || Boolean(busy)} onClick={() => void save()}>
            {busy === "save" ? "Importing…" : source ? "Save and re-import" : "Import source"}
          </Button>
          {source ? (
            <span className="text-xs text-fg-muted">
              Last imported {relativeTime(source.updatedAt)}
              {source.guideFrom && source.guideUntil
                ? ` · ${duration(Date.parse(source.guideUntil) - Date.parse(source.guideFrom))} of listings`
                : ""}
            </span>
          ) : null}
          {source ? (
            <Button
              variant="ghost"
              className="ml-auto text-danger hover:bg-danger/10"
              disabled={Boolean(busy)}
              onClick={() => void remove()}
            >
              <Trash2 size={14} /> Delete source
            </Button>
          ) : null}
        </div>
      </section>
    </div>
  );
}

/** Drop an empty login so plain sources stay plain. */
function cleanAuth(a: SourceAuth | undefined, kind: SourceKind): SourceAuth | undefined {
  if (!a) return undefined;
  const type = kind === "guide" && a.type === "xtream" ? "" : (a.type ?? "");
  const headers = Object.fromEntries(
    Object.entries(a.headers ?? {}).filter(([k]) => k.trim() !== ""),
  );
  const out: SourceAuth = {
    type,
    ...(type === "basic" || type === "xtream"
      ? { username: a.username?.trim(), password: a.password }
      : {}),
    ...(type === "bearer" ? { token: a.token } : {}),
    ...(type === "xtream" ? { server: a.server?.trim(), output: a.output ?? "ts" } : {}),
    ...(Object.keys(headers).length ? { headers } : {}),
    ...(a.userAgent?.trim() ? { userAgent: a.userAgent.trim() } : {}),
    ...(a.referer?.trim() ? { referer: a.referer.trim() } : {}),
  };
  if (type === "headers" && !out.headers) out.type = "";
  if (!out.type && !out.headers && !out.userAgent && !out.referer) return undefined;
  return out;
}

const SIGN_IN: { value: Exclude<AuthType, "xtream">; label: string }[] = [
  { value: "", label: "None" },
  { value: "basic", label: "Username & password" },
  { value: "bearer", label: "Token" },
  { value: "headers", label: "Custom headers" },
];

/** How Ether signs in to a provider that protects its playlist, guide and streams. */
function SignIn({ auth, onChange }: { auth: SourceAuth; onChange: (p: SourceAuth) => void }) {
  const type = auth.type === "xtream" ? "" : (auth.type ?? "");
  return (
    <Field
      label="Sign-in"
      hint={
        type
          ? "Used for the playlist, the guide and this source's streams. Never sent to other providers."
          : "Choose a sign-in if the provider protects its URLs."
      }
    >
      <div className="space-y-3">
        <Segmented
          ariaLabel="Sign-in"
          size="sm"
          value={type}
          onChange={(v) => onChange({ type: v })}
          options={SIGN_IN}
        />
        {type === "basic" ? (
          <div className="grid gap-3 sm:grid-cols-2">
            <Input
              aria-label="Username"
              placeholder="Username"
              autoComplete="off"
              value={auth.username ?? ""}
              onChange={(e) => onChange({ username: e.currentTarget.value })}
            />
            <SecretInput
              ariaLabel="Password"
              placeholder="Password"
              value={auth.password ?? ""}
              onChange={(v) => onChange({ password: v })}
            />
          </div>
        ) : null}
        {type === "bearer" ? (
          <SecretInput
            ariaLabel="Token"
            placeholder="Bearer token"
            value={auth.token ?? ""}
            onChange={(v) => onChange({ token: v })}
          />
        ) : null}
        {type === "headers" ? (
          <HeaderRows headers={auth.headers ?? {}} onChange={(headers) => onChange({ headers })} />
        ) : null}
      </div>
    </Field>
  );
}

function HeaderRows({
  headers,
  onChange,
}: {
  headers: Record<string, string>;
  onChange: (h: Record<string, string>) => void;
}) {
  // Rows live locally so blank and in-progress rows survive; the source only
  // receives named headers.
  const [list, setList] = useState<[string, string][]>(() => {
    const rows = Object.entries(headers);
    return rows.length ? rows : [["", ""]];
  });
  const commit = (next: [string, string][]) => {
    setList(next.length ? next : [["", ""]]);
    onChange(Object.fromEntries(next.filter(([k]) => k.trim() !== "")));
  };
  return (
    <div className="space-y-2">
      {list.map(([k, v], i) => (
        <div key={i} className="flex items-start gap-2">
          <Input
            aria-label={`Header ${i + 1} name`}
            placeholder="X-Api-Key"
            value={k}
            className="w-44 shrink-0 font-mono text-xs"
            onChange={(e) =>
              commit(list.map((r, n) => (n === i ? [e.currentTarget.value, r[1]] : r)))
            }
          />
          <div className="min-w-0 flex-1">
            <SecretInput
              ariaLabel={`Header ${i + 1} value`}
              placeholder="Value"
              value={v}
              onChange={(val) => commit(list.map((r, n) => (n === i ? [r[0], val] : r)))}
            />
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="size-9 shrink-0 p-0 text-fg-subtle hover:text-danger"
            aria-label={`Remove header ${i + 1}`}
            onClick={() => commit(list.filter((_, n) => n !== i))}
          >
            <X size={14} />
          </Button>
        </div>
      ))}
      <Button variant="ghost" size="sm" onClick={() => commit([...list, ["", ""]])}>
        <Plus size={13} /> Add header
      </Button>
    </div>
  );
}

/** User agent and referer: many providers only answer known players. */
function ClientIdentity({
  auth,
  onChange,
}: {
  auth: SourceAuth;
  onChange: (p: SourceAuth) => void;
}) {
  const set = Boolean(auth.userAgent || auth.referer);
  return (
    <details open={set} className="rounded-xl border border-border bg-bg-subtle/40 px-4 py-3">
      <summary className="cursor-pointer text-sm font-medium text-fg">
        Player identity{" "}
        <span className="font-normal text-fg-muted">
          {set ? "· custom" : "· for providers that only answer known players"}
        </span>
      </summary>
      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <Field label="User agent" htmlFor="src-ua">
          <Input
            id="src-ua"
            placeholder="e.g. VLC/3.0.20 LibVLC/3.0.20"
            value={auth.userAgent ?? ""}
            onChange={(e) => onChange({ userAgent: e.currentTarget.value })}
          />
        </Field>
        <Field label="Referer" htmlFor="src-referer">
          <Input
            id="src-referer"
            placeholder="https://…"
            value={auth.referer ?? ""}
            onChange={(e) => onChange({ referer: e.currentTarget.value })}
          />
        </Field>
      </div>
    </details>
  );
}

function UrlOrFile({
  id,
  label,
  url,
  fileReady,
  accept,
  placeholder,
  hint,
  onUrl,
  onFile,
}: {
  id: string;
  label: string;
  url: string;
  fileReady: boolean;
  accept: string;
  placeholder: string;
  hint?: string;
  onUrl: (v: string) => void;
  onFile: (f: File | undefined) => void;
}) {
  return (
    <Field label={label} htmlFor={id} hint={hint}>
      <div className="flex gap-2">
        <Input
          id={id}
          value={url}
          onChange={(e) => onUrl(e.currentTarget.value)}
          placeholder={fileReady ? "Using the uploaded file" : placeholder}
          className="min-w-0 flex-1"
        />
        <label className="inline-flex h-9 shrink-0 cursor-pointer items-center gap-1.5 rounded-md border border-border bg-surface px-3 text-sm font-medium text-fg hover:bg-surface-hover">
          <FileUp size={14} />
          Upload
          <input
            type="file"
            accept={accept}
            className="sr-only"
            aria-label={`Upload ${label}`}
            onChange={(e) => onFile(e.target.files?.[0])}
          />
        </label>
      </div>
      {fileReady ? (
        <Badge variant="success" className="mt-1.5">
          File ready to import
        </Badge>
      ) : null}
    </Field>
  );
}
