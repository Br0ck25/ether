import { Avatar, Badge, Button, DetailPageLayout, EmptyState, Spinner, SubtabNav, toast } from "@braedonsaunders/appkit-ui";
import { ArrowLeft, CheckCircle2, History, LayoutTemplate, RefreshCw } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Artwork } from "../../components/Artwork";
import { Meter } from "../../components/charts";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { titleSubtitle } from "../../components/Poster";
import { UserLimitsForm } from "../../components/UserLimits";
import { viewerName } from "../../components/pickers";
import { api, errorMessage, isUnavailable } from "../../lib/api";
import type { Viewer, ViewerDetail } from "../../lib/contract";
import { formatDateTime, formatNumber, relativeTime } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import { ContextBar, type ViewContext } from "../designer/ContextBar";
import { HomeCanvas } from "../designer/HomeCanvas";
import { TasteView } from "./TasteView";

type Tab = "taste" | "activity" | "home" | "limits";

export default function ViewerProfile() {
  const { id: raw } = useParams();
  const id = Number(raw);
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") as Tab) || "taste";
  const navigate = useNavigate();

  const viewer = useAsync<ViewerDetail | null>(async () => {
    try { return await api.viewer(id); }
    catch (e) {
      // Servers without the viewers API: fall back to the user record.
      if (!isUnavailable(e)) throw e;
      const u = (await api.users()).find((x) => x.id === id);
      if (!u) throw e;
      return { id: u.id, plexId: u.plexId, username: u.username, title: u.title, thumb: u.thumb, isOwner: u.isOwner, plays30d: 0, hoursWatched30d: 0, recent: [] };
    }
  }, [id]);
  const taste = useAsync(() => api.taste(id), [id]);
  const [refreshing, setRefreshing] = useState(false);

  const v = viewer.data;
  const setTab = (t: string) => { const p = new URLSearchParams(params); p.set("tab", t); setParams(p, { replace: true }); };

  const refresh = async () => {
    setRefreshing(true);
    try { await api.refreshViewer(id); toast.success("Rows rebuilt", { description: "Their Plex Home is up to date." }); }
    catch (e) { toast.error("Couldn't rebuild rows", { description: errorMessage(e) }); }
    finally { setRefreshing(false); }
  };

  if (viewer.error) return <div className="p-6"><BackLink /><ErrorPanel error={viewer.error} onRetry={() => void viewer.reload()} /></div>;
  if (!v) return <div className="grid flex-1 place-items-center"><Spinner size={22} label="Loading viewer" /></div>;
  const name = viewerName(v);

  return (
    <DetailPageLayout
      header={
        <div>
          <BackLink />
          <div className="mt-2 flex flex-wrap items-center gap-4">
            <Avatar src={v.thumb} name={name} size={56} />
            <div className="min-w-0 flex-1">
              <h1 className="flex flex-wrap items-center gap-2 text-xl font-semibold text-fg">{name} {v.isOwner ? <Badge>Owner</Badge> : null}</h1>
              <p className="text-sm text-fg-muted">
                {v.username} · active {relativeTime(v.lastActive)} · {formatNumber(v.plays30d, 0)} plays and {formatNumber(v.hoursWatched30d, 1)} hours in 30 days
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={refreshing}><RefreshCw size={14} className={refreshing ? "animate-spin" : ""} /> Rebuild rows</Button>
              <Button variant="outline" size="sm" onClick={() => navigate("/designer")}><LayoutTemplate size={14} /> Home Designer</Button>
            </div>
          </div>
        </div>
      }
      subtabs={<SubtabNav ariaLabel="Viewer sections" active={tab} onSelect={setTab} tabs={[
        { key: "taste", label: "Taste" }, { key: "activity", label: "Activity", count: v.recent?.length }, { key: "home", label: "Their Home" }, { key: "limits", label: "Limits" },
      ]} />}
    >
      {tab === "taste" ? (
        taste.error ? <ErrorPanel error={taste.error} feature="Taste profiles" onRetry={() => void taste.reload()} /> : !taste.data ? <LoadingRows rows={4} /> : (
          <TasteView userId={id} taste={taste.data} onChange={(t) => taste.setData(t)} />
        )
      ) : tab === "activity" ? <Activity viewer={v} /> : tab === "home" ? <TheirHome viewer={v} /> : <Limits id={id} />}
    </DetailPageLayout>
  );
}

function BackLink() {
  return <Link to="/viewers" className="inline-flex items-center gap-1 text-sm text-fg-muted hover:text-fg"><ArrowLeft size={14} /> Viewers</Link>;
}

function Activity({ viewer }: { viewer: ViewerDetail }) {
  const recent = viewer.recent ?? [];
  if (recent.length === 0) return <EmptyState icon={<History />} title="Nothing watched recently" description="Plays, finishes and abandons appear here as they happen." />;
  return (
    <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-surface">
      {recent.map((r, i) => (
        <li key={`${r.title.key}-${i}`} className="flex items-center gap-4 p-3">
          <Artwork src={r.title.poster} alt="" title={r.title.title} className="aspect-[2/3] w-12 shrink-0 rounded" />
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium text-fg">{r.title.title}</p>
            <p className="truncate text-xs text-fg-muted">{titleSubtitle(r.title)}</p>
            {!r.completed ? <div className="mt-1.5 max-w-xs"><Meter value={r.progress} valueLabel={`${Math.round(r.progress * 100)}%`} label="Stopped at" /></div> : null}
          </div>
          <div className="shrink-0 text-right">
            {r.completed ? <Badge variant="success"><CheckCircle2 size={11} /> Finished</Badge> : <Badge variant="warning">In progress</Badge>}
            <p className="mt-1 text-xs text-fg-subtle" title={formatDateTime(r.at)}>{relativeTime(r.at)}</p>
          </div>
        </li>
      ))}
    </ul>
  );
}

function TheirHome({ viewer }: { viewer: Viewer }) {
  const now = new Date();
  const [ctx, setCtx] = useState<ViewContext>({ viewerId: viewer.id, device: "tv", day: now.getDay(), hour: now.getHours(), section: "home" });
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <ContextBar ctx={ctx} onChange={(c) => setCtx({ ...c, viewerId: viewer.id })} viewers={[viewer]} idPrefix="Home" />
        <Link to="/designer" className="text-sm font-medium text-primary hover:underline">Shape it in Home Designer →</Link>
      </div>
      <div className="rounded-xl bg-bg p-2 sm:p-4">
        <HomeCanvas label={`${viewerName(viewer)}'s Home`} ctx={ctx} rules={[]} dirty={false} refreshNonce={0} editable={false}
          actionFor={() => undefined} onAction={() => undefined} onReorder={() => undefined} />
      </div>
    </div>
  );
}

function Limits({ id }: { id: number }) {
  const users = useAsync(() => api.users(), []);
  const config = useAsync(() => api.getConfig(), []);
  if (users.error) return <ErrorPanel error={users.error} onRetry={() => void users.reload()} />;
  if (!users.data) return <LoadingRows rows={3} />;
  const u = users.data.find((x) => x.id === id);
  if (!u) return <EmptyState title="No limits to set" description="This viewer has no user record on the server yet." />;
  return (
    <div className="max-w-3xl">
      <UserLimitsForm user={u} defaultStreamLimit={config.data?.server.defaultStreamLimit} onSaved={(saved) => users.setData((list) => (list ?? []).map((x) => (x.id === saved.id ? saved : x)))} />
    </div>
  );
}
