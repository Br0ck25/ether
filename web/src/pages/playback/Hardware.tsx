import { Badge } from "@braedonsaunders/appkit-ui";
import { Cpu } from "lucide-react";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { Section } from "../../components/layout";
import { apiFetch } from "../../lib/api";
import { useAsync } from "../../lib/hooks";

type Hardware = {
  hwaccel?: { name: string; encoders: string[] | null; device?: string };
  ffmpeg?: unknown;
  error?: string;
};

/** Detected transcoding hardware: acceleration, encoders and the bundled FFmpeg. */
export function HardwareStatus() {
  const hw = useAsync(
    async () => (await apiFetch<{ value: Hardware | null }>("/status/hwaccel")).value ?? {},
    [],
  );
  const data = hw.data;
  const accel = data?.hwaccel;
  const encoders = accel?.encoders ?? [];
  const none = !accel?.name || accel.name === "none";
  const ffmpeg = data?.ffmpeg;

  return (
    <Section
      title="Transcoding hardware"
      description="What this machine can use to convert video for devices that can't play it directly."
    >
      {hw.error && !data ? (
        <ErrorPanel error={hw.error} onRetry={() => void hw.reload()} />
      ) : !data ? (
        <LoadingRows rows={1} />
      ) : data.error ? (
        <p role="alert" className="text-sm text-danger">
          {data.error}
        </p>
      ) : (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <span className="grid size-9 place-items-center rounded-lg bg-primary-subtle text-primary">
              <Cpu size={18} />
            </span>
            <div className="min-w-0">
              <p className="text-sm font-medium text-fg">
                {none ? "Software only" : `Hardware acceleration: ${accel!.name}`}
              </p>
              <p className="text-xs text-fg-muted">
                {accel?.device ? `Device ${accel.device}` : none ? "Transcoding uses the CPU." : ""}
                {typeof ffmpeg === "string" && ffmpeg ? ` · FFmpeg ${ffmpeg}` : ""}
              </p>
            </div>
          </div>
          <div className="flex flex-wrap gap-1.5" aria-label="Hardware encoders">
            {encoders.length ? (
              encoders.map((e) => (
                <Badge key={e} variant="secondary" className="font-mono">
                  {e}
                </Badge>
              ))
            ) : (
              <span className="text-xs text-fg-muted">No hardware encoders detected</span>
            )}
          </div>
          {ffmpeg && typeof ffmpeg !== "string" ? (
            <details>
              <summary className="cursor-pointer text-xs font-medium text-fg-muted">
                FFmpeg details
              </summary>
              <pre className="mt-2 overflow-x-auto whitespace-pre-wrap rounded-md bg-bg-subtle p-3 text-xs">
                {JSON.stringify(ffmpeg, null, 2)}
              </pre>
            </details>
          ) : null}
          <p className="text-xs text-fg-subtle">
            Live CPU and GPU load, disk checks and tone-mapping benchmarks aren't collected.
          </p>
        </div>
      )}
    </Section>
  );
}
