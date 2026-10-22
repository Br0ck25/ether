import { Badge, Button, EmptyState, Input, SubtabNav } from "@braedonsaunders/appkit-ui";
import {
  ArrowLeft,
  ArrowRight,
  Database,
  Film,
  LayoutGrid,
  List,
  RefreshCw,
  Search,
  Settings2,
  SlidersHorizontal,
  X,
} from "lucide-react";
import { useLayoutEffect, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useApp } from "../../app-context";
import { Drawer } from "../../components/Drawer";
import { TitleSearch } from "../../components/pickers";
import { Artwork } from "../../components/Artwork";
import { ErrorPanel, PosterSkeletons } from "../../components/feedback";
import { LevelBadge } from "../../components/status";
import { ToolbarPicker } from "../../components/ToolbarPicker";
import { Workspace } from "../../components/workspace";
import { api } from "../../lib/api";
import { useAsync, useDebouncedValue } from "../../lib/hooks";
import { CatalogSettings } from "./CatalogSettings";
import { TitleInspector } from "./TitleInspector";
import "./catalog.css";

const PAGE_SIZE = 48;
const SORTS = [
  { value: "popularity", label: "Most popular" },
  { value: "released", label: "Recently released" },
  { value: "rating", label: "Highest rated" },
  { value: "added", label: "Recently added" },
  { value: "title", label: "Title A–Z" },
];

export default function Catalog() {
  const { tab } = useParams();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const settings = tab === "settings";
  const { health, refreshDashboard } = useApp();
  const status = health.find((h) => h.id === "catalog");
  return (
    <Workspace
      title="Catalog"
      subtitle="The movies and shows behind your Plex library."
      actions={
        <>
          {status ? (
            <span className="hidden xl:inline-flex">
              <LevelBadge
                level={status.level}
                label={status.level === "ok" ? "Catalog healthy" : status.summary}
              />
            </span>
          ) : null}
          <Button
            size="sm"
            variant={settings ? "secondary" : "outline"}
            onClick={() =>
              navigate(
                settings
                  ? `/catalog${params.size ? `?${params}` : ""}`
                  : `/catalog/settings${params.size ? `?${params}` : ""}`,
              )
            }
          >
            {settings ? <ArrowLeft size={14} /> : <Settings2 size={14} />}
            {settings ? "Back to catalog" : "Catalog settings"}
          </Button>
        </>
      }
    >
      {settings ? (
        <CatalogSettings />
      ) : (
        <CatalogBrowser params={params} setParams={setParams} refreshHealth={refreshDashboard} />
      )}
    </Workspace>
  );
}

function CatalogBrowser({
  params,
  setParams,
  refreshHealth,
}: {
  params: URLSearchParams;
  setParams: (p: URLSearchParams, options?: { replace?: boolean }) => void;
  refreshHealth: () => Promise<void>;
}) {
  const query = params.get("q") ?? "";
  const debouncedQuery = useDebouncedValue(query, 300);
  const type = ["movie", "show"].includes(params.get("type") ?? "") ? params.get("type")! : "";
  const genre = params.get("genre") ?? "";
  const decade = params.get("decade") ?? "";
  const sort = SORTS.some((s) => s.value === params.get("sort"))
    ? params.get("sort")!
    : "popularity";
  const rawPage = Number(params.get("page"));
  const page = Number.isSafeInteger(rawPage) && rawPage > 0 ? Math.min(rawPage, 1000000) : 1;
  const list = params.get("view") === "list";
  const selected = params.get("title");
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [lookupOpen, setLookupOpen] = useState(false);
  // Router transitions can update the URL before the previous controls commit.
  // Merge rapid interactions into the latest requested state, not a stale render.
  const latestParams = useRef(params);
  useLayoutEffect(() => {
    latestParams.current = params;
  }, [params]);
  const update = (key: string, value: string, replace = false) => {
    const next = new URLSearchParams(latestParams.current);
    if (value) next.set(key, value);
    else next.delete(key);
    if (!["page", "view", "title"].includes(key)) next.delete("page");
    if (key === "type") {
      next.delete("genre");
      next.delete("decade");
    }
    latestParams.current = next;
    setParams(next, { replace });
  };
  const results = useAsync(
    (signal) =>
      api.browseCatalog(
        {
          q: debouncedQuery,
          type,
          genre,
          decade,
          sort,
          offset: (page - 1) * PAGE_SIZE,
          limit: PAGE_SIZE,
        },
        signal,
      ),
    [debouncedQuery, type, genre, decade, sort, page],
  );
  const facets = useAsync(async () => {
    const [genres, decades] = await Promise.all([
      api.facets(type, "genre"),
      api.facets(type, "decade"),
    ]);
    return { genres, decades };
  }, [type]);
  const busy = results.loading || query !== debouncedQuery;
  const total = results.data?.total ?? 0;
  const items = results.data?.items ?? [];
  const hasFilters = Boolean(query || genre || decade);
  const reset = () => {
    const next = new URLSearchParams(latestParams.current);
    ["q", "genre", "decade", "page"].forEach((k) => next.delete(k));
    latestParams.current = next;
    setParams(next);
  };
  return (
    <div className="catalog-scroll app-scroll">
      <div className="catalog-intro">
        <div className="catalog-intro-icon">
          <Database size={22} />
        </div>
        <div>
          <p className="catalog-eyebrow">YOUR PLEX LIBRARY, AT A GLANCE</p>
          <h2>Explore your catalog</h2>
          <p>Browse released titles, inspect their metadata, and shape what’s discoverable.</p>
        </div>
        <div className="catalog-intro-note">
          <span className="catalog-live-dot" /> Powered by TMDB
          <span>Movies & television · Updated automatically</span>
        </div>
      </div>
      <div className="catalog-toolbar">
        <SubtabNav
          ariaLabel="Catalog media type"
          active={type || "all"}
          onSelect={(v) => update("type", v === "all" ? "" : v)}
          tabs={[
            { key: "all", label: "All titles" },
            { key: "movie", label: "Movies" },
            { key: "show", label: "TV shows" },
          ]}
        />
        <div className="catalog-tools">
          <Button size="sm" variant="ghost" onClick={() => setLookupOpen(true)}>
            <Search size={14} />
            Find on TMDB
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              void results.reload();
              void facets.reload();
              void refreshHealth();
            }}
            disabled={busy}
            aria-label="Reload catalog"
          >
            <RefreshCw size={14} className={busy ? "animate-spin" : ""} />
          </Button>
          <div className="catalog-view-toggle" role="group" aria-label="Catalog layout">
            <Button
              size="icon"
              variant={!list ? "secondary" : "ghost"}
              aria-label="Poster view"
              aria-pressed={!list}
              onClick={() => update("view", "")}
            >
              <LayoutGrid size={15} />
            </Button>
            <Button
              size="icon"
              variant={list ? "secondary" : "ghost"}
              aria-label="List view"
              aria-pressed={list}
              onClick={() => update("view", "list")}
            >
              <List size={15} />
            </Button>
          </div>
        </div>
      </div>
      <div className="catalog-content">
        <aside
          className={`catalog-filters ${filtersOpen ? "is-open" : ""}`}
          aria-label="Catalog filters"
        >
          <div className="catalog-filter-heading">
            <SlidersHorizontal size={14} />
            <h3>Refine catalog</h3>
            {hasFilters ? <button onClick={reset}>Reset</button> : null}
          </div>
          <label className="catalog-field-label" htmlFor="catalog-search">
            Find a title
          </label>
          <div className="relative">
            <Search size={15} className="absolute left-3 top-3 text-fg-subtle" />
            <Input
              id="catalog-search"
              placeholder="Search catalog…"
              value={query}
              onChange={(e) => update("q", e.target.value, true)}
              className="pl-9"
            />
          </div>
          <div className="catalog-filter-field">
            <span className="catalog-field-label">Genre</span>
            <ToolbarPicker
              className="w-full justify-between"
              ariaLabel="Filter by genre"
              value={genre}
              onChange={(v) => update("genre", v)}
              options={[
                { value: "", label: "All genres" },
                ...(genre && !facets.data?.genres.some((v) => v.key === genre)
                  ? [{ value: genre, label: genre }]
                  : []),
                ...(facets.data?.genres ?? []).map((v) => ({ value: v.key, label: v.title })),
              ]}
            />
          </div>
          <div className="catalog-filter-field">
            <span className="catalog-field-label">Decade</span>
            <ToolbarPicker
              className="w-full justify-between"
              ariaLabel="Filter by decade"
              value={decade}
              onChange={(v) => update("decade", v)}
              options={[
                { value: "", label: "All decades" },
                ...(decade && !facets.data?.decades.some((v) => v.key === decade)
                  ? [{ value: decade, label: `${decade}s` }]
                  : []),
                ...(facets.data?.decades ?? []).map((v) => ({ value: v.key, label: v.title })),
              ]}
            />
          </div>
          <ErrorPanel error={facets.error} onRetry={() => void facets.reload()} />
          <div className="catalog-policy">
            <Film size={18} />
            <h4>Ready for discovery</h4>
            <p>
              Only released titles appear here. Popularity and vote thresholds are managed in
              Catalog settings.
            </p>
            <p>
              This is a metadata library. Playback sources are resolved when a viewer presses play.
            </p>
          </div>
        </aside>
        <section className="catalog-results" aria-label="Catalog titles" aria-busy={busy}>
          <div className="catalog-results-head">
            <div>
              <h3>
                {query
                  ? `Results for “${query}”`
                  : type === "movie"
                    ? "Movies"
                    : type === "show"
                      ? "TV shows"
                      : "Movies & TV shows"}
              </h3>
              <p role="status">
                {busy
                  ? "Loading titles…"
                  : results.error
                    ? "Catalog could not be loaded"
                    : `${total.toLocaleString()} ${hasFilters ? "matching " : ""}${total === 1 ? "title" : "titles"}`}
              </p>
            </div>
            <div className="catalog-tools">
              <Button
                size="sm"
                variant="outline"
                className="catalog-mobile-filter"
                aria-expanded={filtersOpen}
                onClick={() => setFiltersOpen(!filtersOpen)}
              >
                <SlidersHorizontal size={14} />
                Filters
              </Button>
              <ToolbarPicker
                ariaLabel="Sort titles"
                value={sort}
                options={SORTS}
                onChange={(v) => update("sort", v)}
              />
            </div>
          </div>
          {hasFilters ? (
            <div className="catalog-active-filters">
              {[
                ["q", query],
                ["genre", genre],
                ["decade", decade ? `${decade}s` : ""],
              ]
                .filter(([, v]) => v)
                .map(([key, label]) => (
                  <button
                    key={key}
                    onClick={() => update(key!, "")}
                    aria-label={`Remove ${label} filter`}
                  >
                    {label}
                    <X size={12} />
                  </button>
                ))}
            </div>
          ) : null}
          {results.error ? (
            <ErrorPanel error={results.error} onRetry={() => void results.reload()} />
          ) : busy ? (
            <PosterSkeletons count={12} className="catalog-grid" />
          ) : !items.length ? (
            <EmptyState
              icon={<Search size={30} />}
              title={
                hasFilters
                  ? "No matching titles"
                  : page > 1
                    ? "No titles on this page"
                    : "Your catalog is getting started"
              }
              description={
                hasFilters
                  ? "Try another title or broaden your filters."
                  : page > 1
                    ? "The catalog may have changed since your last visit."
                    : "Add a TMDB key in Catalog settings. Released movies and shows will appear after the catalog syncs."
              }
              action={
                hasFilters ? (
                  <Button variant="outline" onClick={reset}>
                    Clear filters
                  </Button>
                ) : page > 1 ? (
                  <Button variant="outline" onClick={() => update("page", "")}>
                    Back to first page
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <>
              <div className={list ? "catalog-list" : "catalog-grid"}>
                {items.map((title) => (
                  <button
                    key={title.key}
                    className={list ? "catalog-list-item" : "catalog-poster"}
                    onClick={() => update("title", title.key)}
                    aria-label={`Inspect ${title.title}`}
                  >
                    <div className="catalog-poster-image">
                      <Artwork
                        src={title.poster}
                        alt=""
                        title={title.title}
                        className="aspect-[2/3] w-full rounded-md"
                      />
                      {!list ? (
                        <span className="catalog-inspect">
                          Inspect title <ArrowRight size={13} />
                        </span>
                      ) : null}
                    </div>
                    <div className="catalog-title-copy">
                      <h4>{title.title}</h4>
                      <p>
                        {title.year || "Year unknown"}
                        <span>·</span>
                        {title.type === "show" ? "TV show" : "Movie"}
                      </p>
                      {list ? (
                        <p className="catalog-list-genres">
                          {title.genres?.join(" · ") || "Genres unavailable"}
                        </p>
                      ) : null}
                    </div>
                    {title.rating ? (
                      <span className="catalog-rating">
                        <span aria-hidden>★</span> {title.rating.toFixed(1)}
                      </span>
                    ) : null}
                    {list ? (
                      <>
                        <Badge variant="secondary">{title.contentRating || "Unrated"}</Badge>
                        <ArrowRight size={15} className="text-fg-subtle" />
                      </>
                    ) : null}
                  </button>
                ))}
              </div>
              <nav className="catalog-pagination" aria-label="Catalog pages">
                <span>
                  {((page - 1) * PAGE_SIZE + 1).toLocaleString()}–
                  {Math.min(page * PAGE_SIZE, total).toLocaleString()} of {total.toLocaleString()}{" "}
                  titles
                </span>
                <div className="catalog-tools">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={page <= 1}
                    onClick={() => update("page", String(page - 1))}
                  >
                    <ArrowLeft size={14} />
                    Previous
                  </Button>
                  <span>
                    Page {page} of {Math.ceil(total / PAGE_SIZE).toLocaleString()}
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={page * PAGE_SIZE >= total}
                    onClick={() => update("page", String(page + 1))}
                  >
                    Next
                    <ArrowRight size={14} />
                  </Button>
                </div>
              </nav>
            </>
          )}
        </section>
      </div>
      <Drawer
        open={lookupOpen}
        onClose={() => setLookupOpen(false)}
        title="Find a title on TMDB"
        size="md"
      >
        <p className="mb-5 text-sm text-fg-muted">
          Look beyond your browsable library. Search for a released movie or show to inspect its
          metadata.
        </p>
        <TitleSearch
          placeholder="Search movies and shows…"
          onPick={(title) => {
            setLookupOpen(false);
            update("title", title.key);
          }}
        />
      </Drawer>
      {selected ? (
        <TitleInspector key={selected} titleKey={selected} onClose={() => update("title", "")} />
      ) : null}
    </div>
  );
}
