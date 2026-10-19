import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { DomainPage, useRouteTab } from "../../components/layout";
import { apiFetch } from "../../lib/api";
import { useAsync, useInterval } from "../../lib/hooks";
import { Channels } from "./Channels";
import { Deliveries } from "./Deliveries";
import type { NotificationsData } from "./types";
import { WeeklyReport } from "./WeeklyReport";

const TABS = [
  { key: "channels", label: "Channels" },
  { key: "report", label: "Weekly report" },
  { key: "deliveries", label: "Delivery history" },
] as const;

export default function Notifications() {
  const tab = useRouteTab(TABS);
  const notices = useAsync(async () => {
    const n = await apiFetch<NotificationsData>("/notifications/");
    return {
      channels: n.channels ?? [],
      events: n.events ?? [],
      deliveries: n.deliveries ?? [],
    };
  }, []);
  useInterval(() => void notices.reload(), 30_000);

  const data = notices.data;
  const body =
    tab === "report" ? (
      <WeeklyReport notifications={data} />
    ) : notices.error && !data ? (
      <ErrorPanel error={notices.error} onRetry={() => void notices.reload()} />
    ) : !data ? (
      <LoadingRows rows={3} />
    ) : tab === "channels" ? (
      <Channels data={data} onChanged={notices.reload} />
    ) : (
      <Deliveries data={data} />
    );

  return (
    <DomainPage
      title="Notifications"
      description="Alerts for you, and a weekly viewing report."
      base="/notifications"
      tabs={TABS.map((t) =>
        t.key === "channels" && data?.channels.length ? { ...t, count: data.channels.length } : t,
      )}
      active={tab}
    >
      {body}
    </DomainPage>
  );
}
