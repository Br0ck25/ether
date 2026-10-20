import { SettingsSection } from "@braedonsaunders/appkit-ui";
import { Meter } from "../../components/charts";
import { SettingsRow } from "../../components/SettingsRow";
import { api } from "../../lib/api";
import { formatBytes, isPlainObject } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import type { DiskInfo } from "../../lib/types";

/** Cache fill and free disk space for the folder and budget being edited. */
export function CacheStatus({
  dir,
  budget,
  status,
}: {
  dir: string;
  budget: number;
  status: unknown;
}) {
  const disk = useAsync<DiskInfo>(() => api.disk(dir), [dir]);
  const used = isPlainObject(status) ? Number(status.used ?? 0) : 0;
  return (
    <SettingsSection title="Usage">
      <SettingsRow
        stacked
        title="Cache"
        description={`${formatBytes(used)} of ${formatBytes(budget)} budget in use.`}
      >
        <Meter value={budget > 0 ? used / budget : 0} />
      </SettingsRow>
      <SettingsRow
        stacked
        title="Disk"
        description={
          disk.error
            ? "Couldn't read free space for that folder."
            : disk.data
              ? `${formatBytes(disk.data.freeBytes)} free of ${formatBytes(disk.data.totalBytes)} on ${disk.data.probedPath || dir}.`
              : "Checking free space…"
        }
      >
        {disk.data ? (
          <Meter
            value={disk.data.totalBytes > 0 ? budget / disk.data.totalBytes : 0}
            label="Budget as a share of the disk"
            tone={disk.data.freeBytes < budget - used ? "warning" : "primary"}
          />
        ) : null}
        {disk.data && disk.data.freeBytes < budget - used ? (
          <p className="mt-1 text-xs text-warning">
            The budget is larger than the free space left on this disk.
          </p>
        ) : null}
      </SettingsRow>
    </SettingsSection>
  );
}
