import { Badge, EmptyState, PagedTable } from "@braedonsaunders/appkit-ui";
import { Inbox } from "lucide-react";
import { Section } from "../../components/layout";
import { channelName, type Delivery, type NotificationsData } from "./types";

function statusVariant(status: string) {
  const s = status.toLowerCase();
  if (/(fail|error|timeout|reject)/.test(s)) return "destructive" as const;
  if (/(sent|deliver|ok|success)/.test(s)) return "success" as const;
  if (/(queue|pending|digest|wait)/.test(s)) return "info" as const;
  return "secondary" as const;
}

/** Recent notification deliveries and their outcome. */
export function Deliveries({ data }: { data: NotificationsData }) {
  const name = (d: Delivery) => {
    const c = data.channels.find((c) => c.id === d.channel);
    return c ? channelName(c) : d.channel;
  };
  return (
    <Section
      title="Delivery history"
      description="Failed deliveries aren't retried; send a new test or wait for the next event. Timeouts are never retried automatically, since the alert may have arrived."
    >
      {data.deliveries.length ? (
        <PagedTable
          rows={data.deliveries}
          rowKey={(d) => String(d.id)}
          pageSize={25}
          empty="No queued notifications."
          defaultSort={{ key: "created", dir: "desc" }}
          labels={{
            searchLabel: "Search deliveries",
            searchPlaceholder: "Find a channel or status…",
          }}
          columns={[
            {
              key: "created",
              header: "Queued",
              sortValue: (d) => d.created,
              cell: (d) => new Date(d.created * 1000).toLocaleString(),
            },
            {
              key: "channel",
              header: "Channel",
              search: name,
              sortValue: name,
              cell: name,
            },
            {
              key: "status",
              header: "Status",
              search: (d) => d.status,
              sortValue: (d) => d.status,
              cell: (d) => <Badge variant={statusVariant(d.status)}>{d.status}</Badge>,
            },
          ]}
        />
      ) : (
        <EmptyState
          icon={<Inbox />}
          title="No queued notifications"
          description="Deliveries appear here once a channel sends its first alert or test."
        />
      )}
    </Section>
  );
}
