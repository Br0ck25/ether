import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  EmptyState,
  Spinner,
} from "@braedonsaunders/appkit-ui";
import { CalendarClock, Filter, Layers, RefreshCw, SearchX, Users } from "lucide-react";
import type { ReactNode } from "react";
import { Artwork } from "../../components/Artwork";
import { ErrorPanel, PosterSkeletons } from "../../components/feedback";
import { PosterGrid } from "../../components/Poster";
import { api } from "../../lib/api";
import type { CollectionSpec, CollectionTemplate } from "../../lib/contract";
import { relativeTime } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import {
  REFRESH_OPTIONS,
  SORTS,
  describeRule,
  describeSource,
  describeTargets,
  previewableSpec,
} from "../../lib/rules";
import { ArtworkPreview, describeSchedule } from "./SettingsPanels";

/** Read-only preview of a saved collection or a template, without opening an editor. */
export function CollectionDetail({
  spec,
  template,
  actions,
}: {
  spec: CollectionSpec;
  template?: CollectionTemplate;
  actions: ReactNode;
}) {
  const previewKey = JSON.stringify(previewableSpec(spec));
  const preview = useAsync(
    (signal) => api.previewCollection(previewableSpec(spec), signal),
    [previewKey, spec.lastBuiltAt],
  );
  const items = preview.data?.items ?? [];
  const targets = describeTargets(spec);
  const total = preview.data?.total;
  const refresh = REFRESH_OPTIONS.find((r) => r.value === spec.refresh)?.label ?? spec.refresh;
  return (
    <div className="min-w-0">
      <section
        className="relative isolate overflow-hidden border-b border-border bg-surface"
        aria-labelledby="collection-title"
      >
        {items[0] ? (
          <>
            <Artwork
              src={items[0].backdrop ?? items[0].poster}
              alt=""
              title=""
              icon={<span />}
              className="pointer-events-none absolute inset-0 -z-10 opacity-15"
              imgClassName="object-cover"
            />
            <div className="pointer-events-none absolute inset-0 -z-10 bg-gradient-to-t from-surface via-surface/80 to-surface/40" />
          </>
        ) : null}
        <div className="flex items-start gap-5 p-5 sm:gap-6 sm:p-6 xl:p-8">
          <div className="hidden w-28 shrink-0 sm:block xl:w-32">
            <ArtworkPreview spec={spec} items={items} />
          </div>
          <div className="min-w-0 flex-1 py-1">
            <div className="mb-3 flex flex-wrap items-center gap-2">
              <span className="text-[10px] font-semibold uppercase tracking-[0.14em] text-fg-muted">
                {template
                  ? `${template.category} template`
                  : spec.builtIn
                    ? "Managed by Ether"
                    : "Collection preview"}
              </span>
              {!template ? (
                <Badge variant={spec.enabled ? "success" : "secondary"}>
                  {spec.enabled ? "Published" : "Draft"}
                </Badge>
              ) : null}
            </div>
            <h2
              id="collection-title"
              className="text-2xl font-semibold leading-tight tracking-tight text-fg xl:text-3xl"
            >
              {spec.name}
            </h2>
            <p className="mt-2 max-w-xl text-sm leading-relaxed text-fg-muted">
              {spec.description ||
                "A curated selection from your catalogue, ready to discover in Plex."}
            </p>
            <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-fg-subtle">
              <span>
                {spec.mediaType === "show"
                  ? "TV shows"
                  : spec.mediaType === "mixed"
                    ? "Movies & TV shows"
                    : "Movies"}
              </span>
              <span aria-hidden>·</span>
              <span>
                {spec.rules.length
                  ? `${spec.rules.length} ${spec.rules.length === 1 ? "rule" : "rules"}`
                  : "Whole source"}
              </span>
              {spec.limit > 0 ? (
                <>
                  <span aria-hidden>·</span>
                  <span>Up to {spec.limit.toLocaleString()} titles</span>
                </>
              ) : null}
            </div>
            <div className="mt-5 flex flex-wrap items-center gap-2">{actions}</div>
          </div>
        </div>
        <dl className="grid grid-cols-3 border-t border-border bg-surface/60 px-5 py-4 sm:px-6 xl:px-8">
          <Readout
            icon={<Layers size={13} />}
            label="Appears in"
            value={targets.length ? targets.join(" · ") : "Not placed yet"}
            detail={
              template
                ? "Customise before publishing"
                : spec.enabled
                  ? "Available in Plex"
                  : "Publish to make it visible"
            }
          />
          <Readout
            icon={<Users size={13} />}
            label="Audience"
            value={
              spec.targets.users === "all"
                ? "Everyone"
                : `${spec.targets.users.length} selected viewers`
            }
            detail={spec.targets.schedule ? describeSchedule(spec) : "No viewing schedule"}
          />
          <Readout
            icon={<CalendarClock size={13} />}
            label="Refresh"
            value={refresh}
            detail={spec.lastBuiltAt ? `Built ${relativeTime(spec.lastBuiltAt)}` : "Not built yet"}
          />
        </dl>
      </section>
      <div className="space-y-6 p-5 sm:p-6 xl:p-8">
        <section aria-labelledby="collection-titles">
          <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-baseline gap-2">
              <h3 id="collection-titles" className="text-sm font-semibold text-fg">
                {template ? "A taste of this template" : "In this collection"}
              </h3>
              <span aria-live="polite" className="text-xs tabular-nums text-fg-muted">
                {total === undefined
                  ? ""
                  : `${total.toLocaleString()} ${total === 1 ? "title" : "titles"}`}
              </span>
              {preview.loading ? <Spinner size={12} label="Loading title preview" /> : null}
            </div>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs"
              disabled={preview.loading}
              onClick={() => void preview.reload()}
            >
              <RefreshCw size={12} /> Refresh preview
            </Button>
          </div>
          {preview.data?.warnings?.map((w) => (
            <Alert key={w} variant="warning" className="mb-3">
              <AlertDescription>{w}</AlertDescription>
            </Alert>
          ))}
          {preview.error ? (
            <ErrorPanel error={preview.error} onRetry={() => void preview.reload()} />
          ) : !preview.data ? (
            <PosterSkeletons count={12} />
          ) : items.length ? (
            <>
              <PosterGrid items={items} />
              <p className="mt-4 text-[11px] text-fg-subtle">
                {items.length < (total ?? 0)
                  ? `Showing the first ${items.length} matching titles. `
                  : ""}
                Preview of current catalogue matches.
              </p>
            </>
          ) : (
            <EmptyState
              icon={<SearchX />}
              title="No titles match yet"
              description={
                template
                  ? "Try customising this template to match more of your catalogue."
                  : "Edit the collection to broaden its sources or rules."
              }
              className="py-10"
            />
          )}
        </section>
        <details className="group rounded-lg border border-border bg-surface">
          <summary className="flex cursor-pointer items-center gap-2 p-3 text-xs font-medium text-fg marker:content-none">
            <Filter size={13} className="text-fg-muted" /> How titles are selected{" "}
            <span className="ml-auto text-fg-subtle group-open:hidden">View rules ↓</span>
          </summary>
          <div className="space-y-4 border-t border-border p-4 text-xs text-fg-muted">
            <div>
              <h4 className="mb-1.5 font-medium text-fg">Sources</h4>
              <ul className="space-y-1">
                {spec.sources.map((s, i) => (
                  <li key={i}>{describeSource(s)}</li>
                ))}
              </ul>
            </div>
            <div>
              <h4 className="mb-1.5 font-medium text-fg">
                {spec.match === "any" ? "Match any rule" : "Match every rule"}
              </h4>
              {spec.rules.length ? (
                <ul className="space-y-1">
                  {spec.rules.map((r, i) => (
                    <li key={i}>{describeRule(r)}</li>
                  ))}
                </ul>
              ) : (
                <p>No additional rules.</p>
              )}
            </div>
            {spec.exclude.length ? (
              <div>
                <h4 className="mb-1.5 font-medium text-fg">Exclude</h4>
                <ul className="space-y-1">
                  {spec.exclude.map((r, i) => (
                    <li key={i}>{describeRule(r)}</li>
                  ))}
                </ul>
              </div>
            ) : null}
            <p>
              Sorted by {SORTS.find((s) => s.value === spec.sort)?.label.toLowerCase() ?? spec.sort}
              {spec.sortDesc ? " (descending)" : " (ascending)"}
              {spec.rotate ? ` · Rotates ${spec.rotate.size} titles ${spec.rotate.every}` : ""}.
            </p>
          </div>
        </details>
      </div>
    </div>
  );
}

function Readout({
  icon,
  label,
  value,
  detail,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  detail: string;
}) {
  return (
    <div className="min-w-0 border-l border-border px-3 first:border-l-0 first:pl-0 sm:px-5">
      <dt className="flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-wide text-fg-subtle">
        <span className="hidden sm:inline">{icon}</span>
        {label}
      </dt>
      <dd className="mt-1 break-words text-xs font-medium leading-relaxed text-fg">{value}</dd>
      <dd className="mt-0.5 text-[11px] leading-relaxed text-fg-muted">{detail}</dd>
    </div>
  );
}
