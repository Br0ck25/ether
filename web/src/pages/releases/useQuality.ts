import { toast } from "@braedonsaunders/appkit-ui";
import { useCallback } from "react";
import { useApp } from "../../app-context";
import { api } from "../../lib/api";
import { useAsync } from "../../lib/hooks";
import type { EtherConfig } from "../../lib/types";

export type Quality = EtherConfig["quality"];

/** The quality section (profiles, client rules, defaults) with a save that reloads it. */
export function useQuality() {
  const { refreshDashboard } = useApp();
  const config = useAsync(() => api.getConfig(), []);
  const quality = config.data?.quality ?? null;
  const commit = useCallback(
    async (next: Quality, message: string) => {
      await api.putSection("quality", next);
      toast.success(message);
      await config.reload();
      void refreshDashboard();
    },
    [config, refreshDashboard],
  );
  /** Reload after a change saved elsewhere, such as a profile import. */
  const refresh = useCallback(async () => {
    await config.reload();
    void refreshDashboard();
  }, [config, refreshDashboard]);
  return { config, quality, commit, refresh };
}
