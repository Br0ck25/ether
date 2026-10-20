import { DashboardMetricCard } from "@braedonsaunders/appkit-dashboard/primitives";
import { Skeleton, Sparkline } from "@braedonsaunders/appkit-ui";
import { Cpu, Gauge, MemoryStick, Network } from "lucide-react";
import type { ReactNode } from "react";
import { api } from "../lib/api";
import { formatBytes, formatRate } from "../lib/format";
import { useAsync, useInterval } from "../lib/hooks";
import type { LoadSample, SystemLoad } from "../lib/types";

/** How often the Overview refreshes load; the server samples every 2 s. */
export const LOAD_POLL_MS = 2_000;

type Tone = "primary" | "info" | "success" | "warning" | "danger";

function pct(v: number | undefined): string {
  return v === undefined || !Number.isFinite(v) ? "—" : `${Math.round(v)}%`;
}

/** Busy machines read amber, saturated ones red. */
function loadTone(v: number | undefined, base: Tone): Tone {
  if (v === undefined) return base;
  return v >= 90 ? "danger" : v >= 75 ? "warning" : base;
}

/** "NVIDIA GeForce RTX 4080" → "RTX 4080", so the card has room for the rest. */
function shortGPUName(name: string): string {
  return name.replace(/^(NVIDIA|AMD|Apple|Intel\(R\))\s+/i, "").replace(/^GeForce\s+/i, "") || name;
}

function trend(history: LoadSample[], pick: (s: LoadSample) => number, label: string): ReactNode {
  if (history.length < 2) return undefined;
  return <Sparkline points={history.map(pick)} tone="neutral" area ariaLabel={label} />;
}

/**
 * This server's CPU, memory, network and GPU load with five-minute trends,
 * refreshed live while the Overview is open.
 */
export function ServerLoad() {
  const load = useAsync((signal) => api.systemLoad(signal), []);
  useInterval(() => void load.reload(), LOAD_POLL_MS);
  const data = load.data;
  return (
    <section aria-label="Server load" className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-semibold text-fg">Server load</h2>
        <span className="text-xs text-fg-muted">
          {data?.error
            ? `Unavailable: ${data.error}`
            : load.error && !data
              ? "Load isn't available"
              : [
                  data?.current
                    ? `Ether uses ${pct(data.current.procCpu)} CPU and ${formatBytes(data.current.procRss)}`
                    : null,
                  data?.swapUsed ? `swap ${formatBytes(data.swapUsed)}` : null,
                  "live, last 5 minutes",
                ]
                  .filter(Boolean)
                  .join(" · ")}
        </span>
      </div>
      {!data ? (
        load.error ? null : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            {Array.from({ length: 4 }, (_, i) => (
              <Skeleton key={i} className="h-28 rounded-xl" />
            ))}
          </div>
        )
      ) : (
        <LoadCards data={data} />
      )}
    </section>
  );
}

function LoadCards({ data }: { data: SystemLoad }) {
  const cur = data.current;
  const h = data.history;
  const memPct = cur && data.memTotal ? (cur.memUsed / data.memTotal) * 100 : undefined;
  const gpu = [...data.gpus].sort((a, b) => (b.util ?? -1) - (a.util ?? -1))[0];
  const loadAvg = data.load?.length ? `load ${data.load[0]!.toFixed(2)}` : null;

  const cpuDetail = [loadAvg, `${data.cores} ${data.cores === 1 ? "core" : "cores"}`];
  const gpuDetail = gpu
    ? [
        gpu.encoder !== undefined ? `enc ${pct(gpu.encoder)}` : null,
        gpu.decoder !== undefined ? `dec ${pct(gpu.decoder)}` : null,
        gpu.encoder === undefined && gpu.memUsed ? `${formatBytes(gpu.memUsed)} in use` : null,
      ]
    : [];
  const join = (parts: (string | null)[]) => parts.filter(Boolean).join(" · ");

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <DashboardMetricCard
        label="CPU"
        value={pct(cur?.cpu)}
        icon={<Cpu size={18} />}
        tone={loadTone(cur?.cpu, "primary")}
        detail={join(cpuDetail)}
        trend={trend(h, (s) => s.cpu, "CPU use, last 5 minutes")}
      />
      <DashboardMetricCard
        label="Memory"
        value={pct(memPct)}
        icon={<MemoryStick size={18} />}
        tone={loadTone(memPct, "info")}
        detail={cur ? `${formatBytes(cur.memUsed)} of ${formatBytes(data.memTotal)}` : undefined}
        trend={trend(h, (s) => s.memUsed, "Memory in use, last 5 minutes")}
      />
      <DashboardMetricCard
        label="Network"
        value={cur ? `↓ ${formatRate(cur.netRxBps)}` : "—"}
        icon={<Network size={18} />}
        tone="success"
        detail={cur ? `↑ ${formatRate(cur.netTxBps)} up` : undefined}
        trend={trend(h, (s) => s.netRxBps + s.netTxBps, "Network traffic, last 5 minutes")}
      />
      <DashboardMetricCard
        label={gpu ? `GPU · ${shortGPUName(gpu.name)}` : "GPU"}
        value={gpu ? pct(gpu.util) : "—"}
        icon={<Gauge size={18} />}
        tone={loadTone(gpu?.util, "primary")}
        detail={gpu ? join(gpuDetail) || "Utilisation" : "Not reported here"}
        trend={gpu ? trend(h, (s) => s.gpu ?? 0, "GPU use, last 5 minutes") : undefined}
      />
    </div>
  );
}
