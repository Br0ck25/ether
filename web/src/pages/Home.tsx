import { DashboardMetricCard, DashboardPanel } from "@braedonsaunders/appkit-dashboard/primitives";
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Badge,
  Button,
  EmptyState,
  PageContainer,
  PageHeader,
  Skeleton,
  Sparkline,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@braedonsaunders/appkit-ui";
import {
  Activity,
  BarChart3,
  Clock,
  Film,
  HeartPulse,
  MonitorPlay,
  PlayCircle,
  RefreshCw,
  Table2,
  TrendingUp,
  Tv,
  Users,
} from "lucide-react";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useApp, useLiveDashboard } from "../app-context";
import { Artwork } from "../components/Artwork";
import { Meter, TimeBars } from "../components/charts";
import { ErrorPanel, LoadingRows } from "../components/feedback";
import { LevelBadge, StatusDot } from "../components/status";
import { api, isUnavailable } from "../lib/api";
import type { ActivityDay, RowInsight } from "../lib/contract";
import {
  formatBytes,
  formatNumber,
  formatPercent,
  formatRate,
  plural,
  relativeTime,
} from "../lib/format";
import { normalizeSessions, type HealthItem, type NowPlaying } from "../lib/health";
import { useAsync } from "../lib/hooks";
import { useTitle } from "../lib/titles";

function greeting(now = new Date()): string {
  const h = now.getHours();
  return h < 5
    ? "Good night"
    : h < 12
      ? "Good morning"
      : h < 17
        ? "Good afternoon"
        : "Good evening";
}

export default function Home() {
  useLiveDashboard();
  const { boot, dashboard, dashboardError, refreshDashboard, health } = useApp();
  const activity = useAsync((_s) => api.activity(), []);
  const rows = useAsync((_s) => api.rowInsights(), []);
  const sessions = useMemo(() => normalizeSessions(dashboard?.sections.sessions), [dashboard]);
  const hw = dashboard?.sections.hwaccel as
    { hwaccel?: { name?: string }; ffmpeg?: string; error?: string } | undefined;

  return (
    <PageContainer>
      <PageHeader
        title="Overview"
        description={`${greeting()}. Here's how ${boot.serverName || "Ether"} is doing.`}
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              void refreshDashboard();
              void activity.reload();
              void rows.reload();
            }}
          >
            <RefreshCw size={14} /> Refresh
          </Button>
        }
      />
      <div className="mt-6 space-y-6">
        {!boot.setupComplete ? (
          <Alert variant="warning">
            <AlertTitle>Setup isn't finished</AlertTitle>
            <AlertDescription>
              Plex apps can't find Ether until setup is complete.{" "}
              <Link className="font-medium underline" to="/setup">
                Continue setup
              </Link>
            </AlertDescription>
          </Alert>
        ) : null}
        {dashboardError && !dashboard ? (
          <ErrorPanel error={dashboardError} onRetry={() => void refreshDashboard()} />
        ) : null}

        {dashboard ? <NeedsAttention items={health} /> : null}

        <KpiRow
          days={activity.data}
          unavailable={isUnavailable(activity.error)}
          nowPlaying={sessions.length}
          loading={activity.loading && !activity.data}
        />

        <div className="grid items-start gap-6 xl:grid-cols-3">
          <div className="min-w-0 space-y-6 xl:col-span-2">
            <DashboardPanel
              title="Now playing"
              icon={<PlayCircle size={16} />}
              actions={
                <span className="flex items-center gap-2">
                  {sessions.length ? (
                    <Badge variant="info">
                      {sessions.length} {sessions.length === 1 ? "stream" : "streams"}
                    </Badge>
                  ) : null}
                  <Link
                    to="/activity/now"
                    className="text-xs font-medium text-primary hover:underline"
                  >
                    Activity
                  </Link>
                </span>
              }
            >
              {!dashboard ? (
                <LoadingRows rows={2} />
              ) : sessions.length === 0 ? (
                <EmptyState
                  icon={<MonitorPlay />}
                  title="Nothing playing"
                  description="Streams appear here the moment someone presses play in Plex."
                  className="py-10"
                />
              ) : (
                <ul className="space-y-3">
                  {sessions.map((s) => (
                    <SessionCard key={s.id || s.key} s={s} />
                  ))}
                </ul>
              )}
            </DashboardPanel>
            <ActivityPanel state={activity} />
          </div>
          <div className="min-w-0 space-y-6">
            <DashboardPanel
              title="Health"
              icon={<HeartPulse size={16} />}
              actions={
                hw?.hwaccel?.name ? (
                  <Badge variant="secondary" title={hw.ffmpeg ? `ffmpeg ${hw.ffmpeg}` : undefined}>
                    {hw.hwaccel.name}
                  </Badge>
                ) : null
              }
            >
              {!dashboard ? <LoadingRows rows={4} /> : <HealthList items={health} />}
            </DashboardPanel>
            <RowsPanel state={rows} />
          </div>
        </div>
      </div>
    </PageContainer>
  );
}

function KpiRow({
  days,
  unavailable,
  nowPlaying,
  loading,
}: {
  days?: ActivityDay[];
  unavailable: boolean;
  nowPlaying: number;
  loading: boolean;
}) {
  const totals = useMemo(() => {
    const d = days ?? [];
    const plays = d.reduce((n, x) => n + x.plays, 0);
    const hours = d.reduce((n, x) => n + x.hours, 0);
    const peakViewers = d.reduce((n, x) => Math.max(n, x.uniqueViewers), 0);
    const half = Math.floor(d.length / 2);
    const recent = d.slice(half).reduce((n, x) => n + x.plays, 0);
    const earlier = d.slice(0, half).reduce((n, x) => n + x.plays, 0);
    return { plays, hours, peakViewers, change: earlier > 0 ? (recent - earlier) / earlier : null };
  }, [days]);
  if (loading) {
    return (
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-28 rounded-xl" />
        ))}
      </div>
    );
  }
  const spark = (key: "plays" | "hours") =>
    days && days.length > 1 ? (
      <Sparkline
        points={days.map((d) => d[key])}
        tone="neutral"
        area
        ariaLabel={`${key} per day, last 30 days`}
      />
    ) : undefined;
  const na = unavailable ? "—" : undefined;
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <DashboardMetricCard
        label="Plays · 30 days"
        value={na ?? formatNumber(totals.plays, 0)}
        icon={<PlayCircle size={18} />}
        detail={
          totals.change === null
            ? "Across every viewer"
            : `${totals.change >= 0 ? "+" : "−"}${formatPercent(Math.abs(totals.change))} vs. prior`
        }
        trend={spark("plays")}
      />
      <DashboardMetricCard
        label="Hours watched"
        value={na ?? formatNumber(totals.hours, 0)}
        icon={<Clock size={18} />}
        tone="info"
        detail="Last 30 days"
        trend={spark("hours")}
      />
      <DashboardMetricCard
        label="Busiest day"
        value={na ?? `${totals.peakViewers} ${totals.peakViewers === 1 ? "viewer" : "viewers"}`}
        icon={<Users size={18} />}
        tone="success"
        detail="Most people watching in a day"
      />
      <DashboardMetricCard
        label="Streaming now"
        value={String(nowPlaying)}
        icon={<Tv size={18} />}
        tone={nowPlaying ? "warning" : "primary"}
        detail={nowPlaying ? "Live sessions" : "All quiet"}
      />
    </div>
  );
}

function SessionCard({ s }: { s: NowPlaying }) {
  const title = useTitle(s.key);
  const delivered = s.size > 0 ? Math.min(1, s.bytesServed / s.size) : null;
  return (
    <li className="relative overflow-hidden rounded-lg border border-border">
      <Artwork src={title?.backdrop} alt="" title="" className="absolute inset-0" icon={<span />} />
      <div className="scrim-left absolute inset-0" />
      <div className="relative flex gap-4 p-3 sm:p-4">
        <Artwork
          src={title?.poster}
          alt={s.title}
          title={s.title}
          className="aspect-[2/3] w-16 shrink-0 rounded-md shadow-md sm:w-20"
        />
        <div className="min-w-0 flex-1 text-on-scrim">
          <p className="truncate text-base font-semibold">{title?.title ?? s.title}</p>
          <p className="truncate text-sm opacity-80">
            {s.user} · {s.device}
            {s.product && s.product !== s.device ? ` (${s.product})` : ""}
          </p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            <Badge variant={s.decision === "transcode" ? "warning" : "success"}>
              {s.decision === "transcode"
                ? "Transcoding"
                : s.decision === "copy"
                  ? "Direct stream"
                  : s.decision
                    ? "Direct play"
                    : "Starting"}
            </Badge>
            {s.remote ? <Badge variant="info">Remote</Badge> : null}
            {s.failovers ? (
              <Badge variant="warning">
                {s.failovers} failover{s.failovers === 1 ? "" : "s"}
              </Badge>
            ) : null}
            <Badge variant="secondary">{formatRate(s.speedBps)}</Badge>
          </div>
          <div className="mt-3 max-w-md">
            {s.progress !== null ? (
              <Meter
                onScrim
                value={s.progress}
                label={`Started ${relativeTime(s.startedAt)}`}
                valueLabel={`${Math.round(s.progress * 100)}% watched`}
              />
            ) : delivered !== null ? (
              <Meter
                onScrim
                value={delivered}
                label={`Started ${relativeTime(s.startedAt)}`}
                valueLabel={`${formatBytes(s.bytesServed)} of ${formatBytes(s.size)} delivered`}
              />
            ) : (
              <p className="text-xs opacity-80">
                Started {relativeTime(s.startedAt)} · {formatBytes(s.bytesServed)} delivered
              </p>
            )}
          </div>
          {s.decisionReason ? <p className="mt-2 text-xs opacity-80">{s.decisionReason}</p> : null}
          {s.release ? (
            <p className="mt-2 truncate font-mono text-[11px] opacity-70" title={s.release}>
              {s.release}
            </p>
          ) : null}
        </div>
      </div>
    </li>
  );
}

/** Problems first, each one link away from the screen that fixes it. */
function NeedsAttention({ items }: { items: HealthItem[] }) {
  const problems = items.filter((h) => h.level === "error" || h.level === "warn");
  if (problems.length === 0) return null;
  const severe = problems.some((h) => h.level === "error");
  return (
    <Alert variant={severe ? "destructive" : "warning"} role="region" aria-label="Needs attention">
      <AlertTitle>
        {problems.length === 1
          ? "1 thing needs attention"
          : `${problems.length} things need attention`}
      </AlertTitle>
      <AlertDescription>
        <ul className="mt-2 space-y-1.5">
          {problems.map((h) => (
            <li key={h.id} className="flex flex-wrap items-center justify-between gap-2">
              <span className="flex min-w-0 items-center gap-2">
                <StatusDot level={h.level} />
                <span className="font-medium text-fg">{h.label}</span>
                <span className="truncate text-fg-muted">{h.summary}</span>
              </span>
              <Link
                to={h.href}
                className="shrink-0 text-sm font-medium text-primary hover:underline"
              >
                Fix
              </Link>
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  );
}

function HealthList({ items }: { items: HealthItem[] }) {
  return (
    <ul className="-mx-1 space-y-0.5">
      {items.map((h) => (
        <li key={h.id}>
          <Link
            to={h.href}
            className="group flex items-start gap-3 rounded-lg px-2 py-2 hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            title={h.details.join("\n")}
          >
            <StatusDot level={h.level} className="mt-1.5" />
            <span className="min-w-0 flex-1">
              <span className="flex items-center justify-between gap-2">
                <span className="text-sm font-medium text-fg">{h.label}</span>
                <LevelBadge level={h.level} />
              </span>
              <span className="mt-0.5 block truncate text-xs text-fg-muted">{h.summary}</span>
              {h.fill !== undefined ? (
                <span className="mt-1.5 block">
                  <Meter
                    value={h.fill}
                    tone={h.fill > 0.95 && h.id !== "cache" ? "warning" : "primary"}
                  />
                </span>
              ) : null}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function ActivityPanel({ state }: { state: ReturnType<typeof useAsync<ActivityDay[]>> }) {
  const [metric, setMetric] = useState<"plays" | "hours">("plays");
  const [asTable, setAsTable] = useState(false);
  const days = state.data ?? [];
  const data = days.map((d) => ({
    key: d.date,
    label: new Date(`${d.date}T12:00:00`).toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
    }),
    value: d[metric],
    detail:
      metric === "plays"
        ? `${formatNumber(d.hours, 1)} h · ${plural(d.uniqueViewers, "viewer")}`
        : `${plural(d.plays, "play")} · ${plural(d.uniqueViewers, "viewer")}`,
  }));
  return (
    <DashboardPanel
      title="Activity · last 30 days"
      icon={<BarChart3 size={16} />}
      actions={
        <div className="flex items-center gap-1">
          <div
            role="radiogroup"
            aria-label="Metric"
            className="inline-flex rounded-md border border-border bg-bg-subtle p-0.5"
          >
            {(["plays", "hours"] as const).map((m) => (
              <button
                key={m}
                type="button"
                role="radio"
                aria-checked={metric === m}
                onClick={() => setMetric(m)}
                className={`rounded px-2 py-0.5 text-xs font-medium ${metric === m ? "bg-surface text-fg shadow-sm" : "text-fg-muted hover:text-fg"}`}
              >
                {m === "plays" ? "Plays" : "Hours"}
              </button>
            ))}
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2"
            onClick={() => setAsTable((t) => !t)}
            aria-pressed={asTable}
            title={asTable ? "Show chart" : "Show as table"}
          >
            {asTable ? <BarChart3 size={14} /> : <Table2 size={14} />}
            <span className="sr-only">{asTable ? "Show chart" : "Show as table"}</span>
          </Button>
        </div>
      }
    >
      {state.error ? (
        <ErrorPanel
          error={state.error}
          feature="Activity insights"
          onRetry={() => void state.reload()}
        />
      ) : state.loading && !state.data ? (
        <Skeleton className="h-40 w-full" />
      ) : days.length === 0 ? (
        <EmptyState
          icon={<Activity />}
          title="No plays yet"
          description="Activity appears after the first title is watched."
          className="py-10"
        />
      ) : asTable ? (
        <div className="max-h-56 overflow-auto">
          <Table containerClassName="border-0 shadow-none">
            <TableHeader>
              <TableRow>
                <TableHead>Day</TableHead>
                <TableHead className="text-right">Plays</TableHead>
                <TableHead className="text-right">Hours</TableHead>
                <TableHead className="text-right">Viewers</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {[...days].reverse().map((d) => (
                <TableRow key={d.date}>
                  <TableCell>{d.date}</TableCell>
                  <TableCell className="text-right tabular-nums">{d.plays}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatNumber(d.hours, 1)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{d.uniqueViewers}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <TimeBars
          data={data}
          ariaLabel={`${metric === "plays" ? "Plays" : "Hours watched"} per day over the last 30 days`}
          valueFormat={(v) => formatNumber(v, metric === "hours" ? 1 : 0)}
        />
      )}
    </DashboardPanel>
  );
}

function RowsPanel({ state }: { state: ReturnType<typeof useAsync<RowInsight[]>> }) {
  const rows = [...(state.data ?? [])].sort((a, b) => b.playRate - a.playRate).slice(0, 6);
  const maxRate = Math.max(0.0001, ...rows.map((r) => r.playRate));
  return (
    <DashboardPanel
      title="Best-performing rows"
      icon={<TrendingUp size={16} />}
      actions={
        <Link to="/home-screens" className="text-xs font-medium text-primary hover:underline">
          Home screens
        </Link>
      }
    >
      {state.error ? (
        <ErrorPanel
          error={state.error}
          feature="Row insights"
          onRetry={() => void state.reload()}
        />
      ) : state.loading && !state.data ? (
        <LoadingRows rows={4} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<Film />}
          title="No row data yet"
          description="Once viewers browse Plex Home, the rows that lead to plays rank here."
          className="py-8"
        />
      ) : (
        <ol className="space-y-3">
          {rows.map((r, i) => (
            <li
              key={r.hubId}
              title={`${r.plays7d} plays from ${r.impressions7d} impressions in 7 days`}
            >
              <div className="mb-1 flex items-baseline justify-between gap-2 text-sm">
                <span className="min-w-0 truncate text-fg">
                  <span className="mr-1.5 tabular-nums text-fg-subtle">{i + 1}</span>
                  {r.title}
                </span>
                <span className="shrink-0 tabular-nums text-fg-muted">
                  {formatPercent(r.playRate, 1)}
                </span>
              </div>
              <div className="h-1.5 rounded-full bg-bg-subtle">
                <div
                  className="h-full rounded-full bg-primary"
                  style={{ width: `${(r.playRate / maxRate) * 100}%` }}
                />
              </div>
              <p className="mt-1 text-[11px] text-fg-subtle">
                {r.plays7d} plays · {formatNumber(r.impressions7d, 0)} impressions
              </p>
            </li>
          ))}
        </ol>
      )}
    </DashboardPanel>
  );
}
