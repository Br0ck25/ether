import { Badge, Button, EmptyState, toast } from "@braedonsaunders/appkit-ui";
import { Radio, Square } from "lucide-react";
import { useState } from "react";
import { Artwork } from "../../components/Artwork";
import { Meter } from "../../components/charts";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { Section } from "../../components/layout";
import { apiFetch, errorMessage } from "../../lib/api";
import { formatBytes, formatRate, plural, relativeTime } from "../../lib/format";
import type { NowPlaying as Stream } from "../../lib/health";
import type { AsyncState } from "../../lib/hooks";
import { useTitle } from "../../lib/titles";

function decisionLabel(decision: string): string {
  if (decision === "transcode") return "Transcoding";
  if (decision === "copy") return "Direct stream";
  return decision ? "Direct play" : "Starting";
}

function StreamRow({ s, stopping, onStop }: { s: Stream; stopping: boolean; onStop: () => void }) {
  const title = useTitle(s.key || undefined);
  const delivered = s.size > 0 ? Math.min(1, s.bytesServed / s.size) : null;
  return (
    <li className="flex flex-wrap items-center gap-4 py-4 first:pt-0 last:pb-0">
      <Artwork
        src={title?.poster}
        alt={s.title}
        title=""
        className="aspect-[2/3] w-12 shrink-0 rounded-md"
      />
      <div className="min-w-0 flex-1 space-y-2">
        <div className="min-w-0">
          <p className="truncate font-medium text-fg">{s.title}</p>
          <p className="truncate text-sm text-fg-muted">
            {s.user} · {s.device}
            {s.product && s.product !== s.device ? ` (${s.product})` : ""}
          </p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <Badge variant={s.decision === "transcode" ? "warning" : "success"}>
            {decisionLabel(s.decision)}
          </Badge>
          {s.remote ? <Badge variant="info">Remote</Badge> : null}
          {s.failovers ? <Badge variant="warning">{plural(s.failovers, "failover")}</Badge> : null}
          <Badge variant="secondary">{formatRate(s.speedBps)}</Badge>
        </div>
        <div className="max-w-sm">
          {s.progress !== null ? (
            <Meter
              value={s.progress}
              label={`Started ${relativeTime(s.startedAt)}`}
              valueLabel={`${Math.round(s.progress * 100)}% watched`}
            />
          ) : delivered !== null ? (
            <Meter
              value={delivered}
              label={`Started ${relativeTime(s.startedAt)}`}
              valueLabel={`${formatBytes(s.bytesServed)} of ${formatBytes(s.size)} delivered`}
            />
          ) : (
            <p className="text-xs text-fg-muted">
              Started {relativeTime(s.startedAt)} · {formatBytes(s.bytesServed)} delivered
            </p>
          )}
        </div>
        {s.release ? (
          <p className="truncate font-mono text-[11px] text-fg-subtle" title={s.release}>
            {s.release}
          </p>
        ) : null}
      </div>
      <Button variant="outline" size="sm" disabled={stopping} onClick={onStop}>
        <Square size={14} /> Stop stream
      </Button>
    </li>
  );
}

/** Live sessions with a stop control for each. */
export function NowPlaying({ state }: { state: AsyncState<Stream[]> }) {
  const [stopping, setStopping] = useState<string | null>(null);
  const streams = state.data ?? [];

  const stop = async (id: string) => {
    setStopping(id);
    try {
      await apiFetch(`/stats/sessions/${encodeURIComponent(id)}/stop`, {
        method: "POST",
        body: "{}",
      });
      toast.success("Stream stopped");
      await state.reload();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setStopping(null);
    }
  };

  if (state.error && !state.data)
    return <ErrorPanel error={state.error} onRetry={() => void state.reload()} />;
  return (
    <Section
      title="Now playing"
      description="Streams update every few seconds. Stopping a stream ends it on the viewer's device."
      actions={
        state.data ? <Badge variant="secondary">{plural(streams.length, "stream")}</Badge> : null
      }
    >
      {!state.data ? (
        <LoadingRows rows={2} />
      ) : streams.length ? (
        <ul className="divide-y divide-border">
          {streams.map((s) => (
            <StreamRow
              key={s.id || s.key}
              s={s}
              stopping={stopping === s.id}
              onStop={() => void stop(s.id)}
            />
          ))}
        </ul>
      ) : (
        <EmptyState
          icon={<Radio />}
          title="Nothing is playing"
          description="Streams appear here as soon as a viewer presses play."
        />
      )}
    </Section>
  );
}
