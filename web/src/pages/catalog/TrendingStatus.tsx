import { Badge } from "@braedonsaunders/appkit-ui";
import { TrendingUp } from "lucide-react";
import { apiFetch } from "../../lib/api";
import { useAsync, useInterval } from "../../lib/hooks";
import { relativeTime } from "../../lib/format";

type Chart = {
  mediaType: string;
  window: string;
  titles: number;
  fetchedAt?: string;
  state: string;
};
export function TrendingStatus() {
  const data = useAsync(() => apiFetch<{ value: { trending?: Chart[] } }>("/status/catalog"), []);
  useInterval(() => void data.reload(), 60000);
  return (
    <section
      className="rounded-xl border border-border bg-surface p-5"
      aria-label="Global trending status"
    >
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <TrendingUp size={18} />
        Global trending
      </h3>
      <p className="mt-2 text-xs text-fg-muted">
        TMDB daily + weekly charts · refreshed hourly · shared by all viewers
      </p>
      {data.error ? <p className="mt-3 text-sm text-danger">Chart status unavailable</p> : null}
      <div className="mt-4 divide-y divide-border">
        {(data.data?.value.trending ?? []).map((c) => (
          <div className="py-3" key={c.mediaType + c.window}>
            <div className="flex items-center justify-between gap-2 text-sm">
              <span>
                {c.mediaType === "movie" ? "Movies" : "TV shows"} ·{" "}
                {c.window === "day" ? "Today" : "This week"}
              </span>
              <Badge variant="secondary">{c.state}</Badge>
            </div>
            <p className="mt-1 text-xs text-fg-muted">
              {c.titles} titles · updated {relativeTime(c.fetchedAt)}
            </p>
          </div>
        ))}
      </div>
      <p className="mt-3 text-xs text-fg-muted">
        The last successful chart survives restarts and temporary outages. Eligibility and your
        visibility rules still apply.
      </p>
    </section>
  );
}
