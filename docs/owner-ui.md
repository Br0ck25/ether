# Owner UI and personalization: product spec and API contract

## The principle

Viewers never see Ether. They open Plex on a TV, phone or browser and see a
Home screen and libraries that feel hand-curated for them, the way Netflix
does. Everything clever happens on the server, in the background, and reaches
viewers only through what Ether returns to Plex: Home rows (`/hubs`), section
rows, collections, playlists, artwork and ordering.

The web UI at `/ether/` is for the **owner**: set up the server, design
collections and rows, see exactly what each person's Plex Home looks like,
understand why, and tune it. It is built on AppKit
(`@braedonsaunders/appkit-ui`, `@braedonsaunders/appkit-tokens`).

## What viewers get (through Plex)

A personal Home per Plex user, rebuilt in the background and served instantly:

- **Row selection and order are personal.** Each user gets the rows most
  likely to lead to a play, ranked by predicted engagement, not a fixed list.
- **Context-aware.** Time of day, weekday versus weekend, and device class
  (TV, mobile, web, from `X-Plex-Product`/`X-Plex-Platform`) change which rows
  appear and what's in them: shorter titles on weeknights and phones, films
  on weekend evenings on the TV, kid-friendly before evening on shared
  accounts that watch family titles.
- **Rows Netflix-style:** Continue Watching, Top 10 for you, Because you
  watched X, More from <director/actor you love>, Trending in your taste,
  Hidden gems for you, New this week in genres you like, Comfort rewatches,
  Something different (exploration), Short on time, owner-designed
  collections, franchise "finish it" rows, LLM themed rows (optional).
- **Freshness.** Rows and items rotate. Every `/hubs` response is recorded as
  impressions; titles shown repeatedly without a play are demoted, so Home
  never goes stale.
- **Feedback loop.** Plays, completions, abandons (stopped early), ratings and
  impressions feed each user's taste model continuously.
- **Collections that look made by hand.** Generated poster art for owner
  collections; collections appear in the Collections tab and, when promoted,
  as Home rows for the users they target.

## What the owner gets (`/ether/`)

1. **Home**: health at a glance (now playing, Usenet, indexers, cache,
   catalogue), plus the most-watched and most-engaging rows.
2. **Collection Studio** (better than Kometa): build a collection visually
   from sources and rules, see the result live with posters as you edit,
   start from templates, or describe it in plain English and let the
   optional LLM draft the rules. Schedule refreshes, rotate members, choose
   artwork, and target who sees it and where (Collections tab, Home row,
   section row, specific users).
3. **Home Designer**: a Netflix-style layered Home. The owner edits a
   **Global Home** template for everyone (which rows are in the pool and
   whether they are always shown, eligible or off; anchored slots;
   "never above / never below" bounds; per-page and days-per-week caps;
   schedules by day, hour, daypart, device, month and date; page length
   per section and device). **Personalization** then picks and orders the
   eligible rows per viewer and context, and **per-viewer overrides** sit
   on top (pin, hide, add a collection just for them, change limits),
   each with "reset to global". The preview shows the Plex Home of a
   viewer, or of the typical viewer, for any device, day, time and
   section, rendered server-side from the unsaved draft, with every row's
   provenance (Global anchor / Personalized / Override) and why it sits at
   its slot; editing Global shows how many viewers it affects.
4. **Viewers**: each Plex user's taste profile (genres, people, decades,
   moods), recent activity, and what the engine is learning; the owner can
   correct it ("not into horror") and set limits.
5. **Settings, setup wizard, logs, resolver traces**: the existing features,
   rebuilt on AppKit.

## API contract

All endpoints are under `/ether/api/`, JSON, with the existing auth and CSRF
rules (`X-Ether-Request: 1` on mutations). Existing endpoints keep working.
Times are RFC 3339. Rating keys are decimal strings. Images are URLs the
browser can load directly (`/ether/api/image?...` proxies TMDB artwork).

### Shared shapes

```ts
type TitleCard = {
  key: string; type: "movie" | "show" | "season" | "episode";
  title: string; year?: number; summary?: string;
  poster?: string; backdrop?: string;          // browser-loadable URLs
  rating?: number; contentRating?: string; runtimeMinutes?: number;
  genres?: string[]; showTitle?: string; season?: number; episode?: number;
};

type Rule = {                                  // one condition
  field: "genre" | "keyword" | "person" | "studio" | "network" | "country" |
         "language" | "year" | "decade" | "rating" | "votes" | "popularity" |
         "runtime" | "contentRating" | "released" | "watchProvider" |
         "collection" | "watched" | "inProgress" | "addedAt";
  op: "is" | "isNot" | "in" | "notIn" | "gte" | "lte" | "between" | "within";
  value: unknown;                              // typed by field
  label?: string;                              // display text for ids (person name…)
};

type Source =                                  // where candidates come from
  | { kind: "catalog" }                        // the whole browsable catalogue
  | { kind: "tmdbList"; list: "trending_day" | "trending_week" | "popular" | "top_rated" | "now_playing" | "upcoming" | "airing_today" | "on_the_air"; mediaType: "movie" | "show" }
  | { kind: "tmdbCollection"; id: number; label?: string }
  | { kind: "tmdbDiscover"; params: Record<string, string> }
  | { kind: "tmdbKeyword" | "tmdbCompany" | "tmdbNetwork" | "tmdbPerson"; id: number; label?: string }
  | { kind: "watchProvider"; id: number; region: string; label?: string }
  | { kind: "traktList"; url: string }
  | { kind: "letterboxdList"; url: string }
  | { kind: "mdblist"; url: string }
  | { kind: "titles"; keys: string[] };         // hand-picked

type CollectionSpec = {
  id?: string;                                 // server-assigned
  name: string; description?: string;
  mediaType: "movie" | "show" | "mixed";
  sources: Source[];                           // union of candidates
  match: "all" | "any"; rules: Rule[];         // then filtered by rules
  exclude: Rule[];
  sort: "popularity" | "rating" | "released" | "title" | "added" | "random" | "personal" | "source";
  sortDesc: boolean; limit: number;            // 0 = no limit
  refresh: "live" | "hourly" | "daily" | "weekly";
  rotate?: { size: number; every: "daily" | "weekly" } | null;   // show a rotating subset
  artwork: { mode: "auto" | "collage" | "backdrop" | "tmdb" | "custom"; url?: string; accent?: string };
  targets: {
    collectionsTab: boolean;                   // Plex Collections tab of the matching sections
    homeRow: boolean; sectionRow: boolean;
    users: number[] | "all";                   // local user ids
    schedule?: { days?: number[]; fromHour?: number; toHour?: number; devices?: ("tv" | "mobile" | "web" | "desktop")[] } | null;
  };
  enabled: boolean;
  templateId?: string;
  updatedAt?: string; lastBuiltAt?: string; itemCount?: number;
};
```

### Collection Studio

| Method | Path | Body / query | Response |
| --- | --- | --- | --- |
| GET | `collections` | | `{ collections: CollectionSpec[] }` (owner collections, plus built-ins marked `builtIn: true`) |
| POST | `collections` | `CollectionSpec` | `CollectionSpec` |
| GET | `collections/{id}` | | `CollectionSpec` |
| PUT | `collections/{id}` | `CollectionSpec` | `CollectionSpec` |
| DELETE | `collections/{id}` | | `204` |
| POST | `collections/preview` | `CollectionSpec` (unsaved) | `{ total: number, items: TitleCard[] /* first 60 */, warnings: string[], tookMs: number }` |
| POST | `collections/{id}/rebuild` | | `{ itemCount, lastBuiltAt }` |
| GET | `collections/templates` | | `{ templates: { id, name, description, category, spec: CollectionSpec }[] }` (≥40 curated templates) |
| POST | `collections/generate` | `{ prompt: string, mediaType?: string }` | `{ spec: CollectionSpec, explanation: string }` (501 when the LLM is off) |
| GET | `collections/{id}/artwork` | `?w=` | image (generated poster) |

### Catalogue helpers (for the builder)

| Method | Path | Query | Response |
| --- | --- | --- | --- |
| GET | `catalog/search` | `q, type?, limit?` | `{ items: TitleCard[] }` |
| GET | `catalog/facets` | `type, facet=genre\|decade\|contentRating\|studio\|country\|language` | `{ values: { key, title, count }[] }` |
| GET | `catalog/lookup` | `kind=person\|keyword\|company\|network\|collection\|watchProvider, q, region?` | `{ results: { id, name, subtitle?, image? }[] }` |
| GET | `catalog/title/{key}` | | `TitleCard & { cast, directors, keywords, collection }` |
| GET | `image` | `path=/tmdb/path.jpg, w=` | proxied image |

### Viewers and personalization

```ts
type Viewer = { id: number; plexId: number; username: string; title: string; thumb?: string;
  isOwner: boolean; lastActive?: string; plays30d: number; hoursWatched30d: number };

type TasteProfile = {
  userId: number; updatedAt: string; confidence: number;   // 0..1, grows with history
  genres: { name: string; weight: number }[];               // weight -1..1
  people: { id: number; name: string; role: string; weight: number; image?: string }[];
  keywords: { name: string; weight: number }[];
  decades: { decade: number; weight: number }[];
  languages: { code: string; weight: number }[];
  habits: { weeknightMinutes: number; weekendMinutes: number; preferredDevice: string;
            bingeScore: number; rewatchScore: number; peakHours: number[] };
  overrides: { kind: "genre" | "person" | "keyword"; value: string; weight: number }[];
};

type HomeRow = {
  hubId: string; title: string; kind: string;      // e.g. "because-you-watched", "collection:<id>"
  reason: string;                                  // why this row / these titles
  score: number;                                   // predicted engagement used for ordering
  pinned?: boolean;                                // anchored to a slot
  hidden?: boolean;                                // not on the page (never sent to Plex)
  source: "engine" | "owner" | "plex";
  items: TitleCard[];                              // what Plex receives, in order
  itemReasons?: Record<string, string>;            // key -> why this title
  // Provenance in the layered Home Designer:
  layer?: "global" | "personalized" | "override";  // who put it there: a Global anchor/Always row,
                                                   // the engine within the Global constraints, or
                                                   // the viewer's override
  slot?: number;                                   // 1-based position among the rows the engine arranges
  anchorSlot?: number;                             // the slot its policy anchors it to
  placement?: string;                              // why it is at this slot
  hiddenReason?: string;                           // why a hidden row is not on the page
  policy?: RowPolicy; policyLayer?: "global" | "override";  // the effective policy and its layer
};
```

| Method | Path | Body / query | Response |
| --- | --- | --- | --- |
| GET | `viewers` | | `{ viewers: Viewer[] }` |
| GET | `viewers/{id}` | | `Viewer & { recent: { title: TitleCard, at, completed, progress }[] }` |
| GET | `viewers/{id}/taste` | | `TasteProfile` |
| PUT | `viewers/{id}/taste/overrides` | `{ overrides: TasteProfile["overrides"] }` | `TasteProfile` |
| GET | `viewers/{id}/home` | `device=tv\|mobile\|web\|desktop, at=<RFC3339>, section?, count?` | `{ rows: HomeRow[], generatedAt, context: { device, daypart, weekend } }` — exactly what `/hubs` would return to that user in that context (same code path), plus Plex's own rows (`source: "plex"`) and, last, the rows that are not on the page with `hidden: true` and a `hiddenReason` |
| POST | `viewers/{id}/refresh` | | `204` (rebuild their rows now) |

### Home Designer (layered layout)

A Home is composed in three layers, like Netflix's homepage (rows chosen
from a candidate pool under page-level constraints, then personalised per
profile):

1. **Global Home**: the owner's template for everyone, per page (`home`,
   `movies`, `shows`): a row policy for any row in the pool, and the page
   length (per device if wanted). Rows without a policy are *eligible*.
2. **Personalized**: within those constraints the engine picks and orders
   the eligible rows per viewer and context (`docs/personalization.md`).
3. **Overrides**: per viewer. An override replaces the Global policy for
   the same `row` (or the page length); deleting it is "reset to global".

```ts
type RowPolicy = {
  row: string;              // kind ("short-on-time"), "collection:<id>", a hub id ("because.123"),
                            // or a glob ("because.*", "collection:*"); hub id beats kind beats glob
  status: "always" | "eligible" | "off";
  slot?: number;            // anchored slot, 1 = top (implies "always")
  minSlot?: number;         // never above this slot
  maxSlot?: number;         // never below this slot
  priority?: number;        // ranking weight 0.1–5 (default 1)
  maxPerPage?: number;      // rows this policy matches on one page (1–10; engine default per kind)
  maxDaysPerWeek?: number;  // frequency cap per viewer (1–6; stays all day once shown)
  when?: LayoutWhen;        // only shown then; off otherwise
};
type LayoutWhen = {
  days?: number[];          // 0 = Sunday … 6
  fromHour?: number; toHour?: number;   // [from, to), server local time, wraps midnight
  dayparts?: ("morning" | "afternoon" | "evening" | "late")[];
  devices?: ("tv" | "mobile" | "web" | "desktop")[];
  months?: number[];        // 1 = January … 12 ("only in December")
  startDate?: string; endDate?: string; // "YYYY-MM-DD", inclusive
};
type LayoutPage = {
  section: "home" | "movies" | "shows";
  maxRows?: number;         // page length: rows the engine arranges (Plex's own not counted); 0 = inherit
  deviceMaxRows?: Partial<Record<"tv" | "mobile" | "web" | "desktop", number>>;
  rows: RowPolicy[];
};
type HomeLayout = {
  global: LayoutPage[];
  viewers: { userId: number; pages: LayoutPage[] }[];   // overrides
  updatedAt?: string;
  notes?: string[];         // server remarks (legacy conversion); cleared by the next save
};
type LayoutImpact = { viewerId: number; title: string; overrides: number; changed: boolean;
  added: string[]; removed: string[]; moved: number };
```

Rules the server enforces (400 `validation` with dotted field paths
otherwise): one policy per `row` and one anchor per slot on a page; an
anchored row has no bounds and cannot be off; `minSlot ≤ maxSlot`;
Continue Watching (Plex's own row, Home only) can be anchored or bounded
but not turned off, scheduled or capped; Recently Added and Recent
Playlists are built by Plex outside the arrangement and cannot be
targeted. Page length resolves viewer device → viewer page → Global
device → Global page → engine default (`defaults.maxRows`).

| Method | Path | Body | Response |
| --- | --- | --- | --- |
| GET | `layout` | | `{ layout: HomeLayout, availableRows: { hubId, title, kind, description }[], defaults: { maxRows: { home, movies, shows } } }` |
| PUT | `layout` | `{ layout: HomeLayout }` (legacy `{ rules: LayoutRule[] }` is still accepted and converted; the response then carries `notes`) | same as GET |
| POST | `layout/preview` | `{ layout?: HomeLayout /* unsaved draft; omitted = saved */, viewerId?: number \| null /* null = typical viewer */, device?, at?, section?, count? }` | `{ rows: HomeRow[], generatedAt, context, viewer: { id, title } \| null }` — the same assembly as `/hubs` |
| POST | `layout/impact` | `{ layout: HomeLayout, device?, at?, section? }` | `{ viewers: LayoutImpact[], summary: { viewers, withOverrides, changed } }` — every viewer's page under the saved layout vs the draft |

The legacy `LayoutRule` list (pin / hide / boost / bury with scopes and
schedules) is converted once, the first time this version starts: server-wide
rules become Global policies, rules for specific viewers become their
overrides; pin at position p anchors to slot p+1, hide is off, boost is
priority 1.5, bury priority 0.3, `maxRows` the page length. A hide limited
to days, hours or devices becomes a schedule for the complement; other
scheduled rules keep their effect at all times. Every approximation is
listed in `notes`.

### Insights (dashboard)

| Method | Path | Response |
| --- | --- | --- |
| GET | `insights/rows` | `{ rows: { hubId, title, impressions7d, plays7d, playRate }[] }` |
| GET | `insights/activity` | `{ days: { date, plays, hours, uniqueViewers }[] }` (30 days) |

## Ownership

| Area | Owner | Packages |
| --- | --- | --- |
| Personalization engine, viewers, layout, insights APIs | Engine work package | `internal/recommend`, `internal/personalize` (new), `internal/admin/viewers_api.go`, `internal/admin/layout_api.go`, `internal/admin/insights_api.go`, hub context + impressions hook in `internal/plex/handlers_hubs.go` |
| Collection engine, studio, catalogue helper APIs, artwork | Collections work package | `internal/collections`, `internal/artwork` (new), `internal/admin/collections_api.go`, `internal/admin/catalog_api.go`, collection artwork route in `internal/plex` |
| Owner web app | UI work package | `web/` (all), `internal/admin/dist` (built output) |
| Wiring | Orchestrator | `internal/app`, `cmd/ether` |

Additions to `internal/core` and `internal/config` stay additive. Each backend
work package adds its own fields to `admin.Deps` in a clearly separated block.
