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
3. **Home Designer**: pick a viewer and a device context and see their Plex
   Home exactly as Plex will receive it, with each row's reason. Pin, hide or
   reorder rows server-wide or per user; set rules ("Short on time" only on
   weeknights).
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
  reason: string;                                  // human explanation
  score: number;                                   // predicted engagement used for ordering
  pinned?: boolean; hidden?: boolean; source: "engine" | "owner" | "plex";
  items: TitleCard[];                              // what Plex receives, in order
  itemReasons?: Record<string, string>;            // key -> why this title
};
```

| Method | Path | Body / query | Response |
| --- | --- | --- | --- |
| GET | `viewers` | | `{ viewers: Viewer[] }` |
| GET | `viewers/{id}` | | `Viewer & { recent: { title: TitleCard, at, completed, progress }[] }` |
| GET | `viewers/{id}/taste` | | `TasteProfile` |
| PUT | `viewers/{id}/taste/overrides` | `{ overrides: TasteProfile["overrides"] }` | `TasteProfile` |
| GET | `viewers/{id}/home` | `device=tv\|mobile\|web\|desktop, at=<RFC3339>, section?` | `{ rows: HomeRow[], generatedAt, context: { device, daypart, weekend } }` — exactly what `/hubs` would return to that user in that context |
| POST | `viewers/{id}/refresh` | | `204` (rebuild their rows now) |

### Home Designer (layered layout)

The owner edits Global Home on the left third of the page and sees the
server-rendered Plex Home on the right two thirds. A viewer layer replaces
individual global row policies; deleting an override resets that row to global.
Page lengths inherit separately, with device-specific limits taking precedence.
Global editing includes a typical new viewer and a viewer switcher for spot checks.

```ts
type HomeLayout = {
  global: LayoutPage[];
  viewers: { userId: number; pages: LayoutPage[] }[];
  updatedAt?: string; notes?: string[];
};
type LayoutPage = {
  section: "home" | "movies" | "shows";
  maxRows?: number;
  deviceMaxRows?: Partial<Record<"tv" | "mobile" | "web" | "desktop", number>>;
  rows: RowPolicy[];
};
type RowPolicy = {
  row: string; // exact hub id, row kind, collection:<id>, or glob
  status: "always" | "eligible" | "off";
  slot?: number; minSlot?: number; maxSlot?: number; // 1-based
  priority?: number; maxPerPage?: number; maxDaysPerWeek?: number;
  when?: {
    days?: number[]; fromHour?: number; toHour?: number; devices?: string[];
    dayparts?: ("morning" | "afternoon" | "evening" | "late")[];
    months?: number[]; startDate?: string; endDate?: string;
  };
};
```

Selecting a collection explicitly in a layer can add it for that layer even
when its default Collection Studio targets do not promote it to Home. Disabled
collections and private ownership restrictions remain enforced, as do viewer
content restrictions. The global/override policy controls that row's publishing
schedule.

Slots and bounds apply to the complete page, including Plex-owned rows and
additional discovery/Live TV rows. An anchored row is always included when it
has candidates and matches its schedule; a viewer anchor wins a slot collision
with a global anchor. Bounds and schedules are hard constraints. Infeasible rows
appear in the preview's hidden list with an explanation. `maxPerPage` limits rows
matching the same policy (useful for row-family globs). `maxDaysPerWeek` rotates
a row across a deterministic subset of the local Monday–Sunday week per viewer,
so repeated requests do not consume a frequency budget. Date ranges are inclusive.

| Method | Path | Body | Response |
| --- | --- | --- | --- |
| GET | `layout` | | `{ layout: HomeLayout, availableRows: { hubId, title, kind, description }[], defaults: { maxRows: { home, movies, shows } }, rules: LayoutRule[] }` |
| PUT | `layout` | `{ layout: HomeLayout }` | same as GET |
| POST | `layout/preview` | `{ viewerId: number|null, layout?: HomeLayout, device?, at?: RFC3339, section?, count? }` | viewer Home response; `viewer` is `{ id, title }` or null for the typical new viewer |
| POST | `layout/impact` | `{ layout: HomeLayout, device?, at?: RFC3339, section?, count? }` | `{ viewers: { viewerId, title, overrides, changed, added: string[], removed: string[], moved }[], summary: { viewers, withOverrides, changed } }` |

Preview and impact accept unsaved documents and never save them or record
impressions. Missing `layout` uses the saved document. Home rows additionally
expose `layer: "global"|"personalized"|"override"`, `slot`, `anchorSlot`,
`placement`, `hiddenReason`, `policy` and `policyLayer`. The viewer Home endpoint
uses the same renderer. Impact compares actual row membership and positions for
each viewer in the selected context; it also counts their explicit override settings.

The legacy `{ rules: LayoutRule[] }` PUT remains supported and converts those
rules to a layered document. Existing SQLite rules migrate on first engine startup:
all-user rules become global pages, scoped rules become viewer layers, pin positions
increase by one, boost/bury become priority weights. No core fields were removed.

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

## Owner navigation and editing

The owner UI separates browsing from editing. Pages show saved summaries, current
activity or lists. An explicit Add, Create, Configure or Edit action opens the
existing AppKit Drawer; collection editing opens its full-width variant. The
setup wizard remains a guided first-run flow. Filters, context selectors and
operational actions such as Refresh, Scan and Stop stream stay on their page.

AppKit SubtabNav provides underlined tabs, horizontal overflow on narrow screens
and keyboard navigation. Use it for independent tasks rather than stacking their
forms. Keep advanced options beside the feature they affect, with specific names
such as Advanced scoring, Schedule or Device capabilities. Unknown server config
fields remain available under Advanced inside the settings drawer.

| Area | Task organization | Editing entry point |
| --- | --- | --- |
| Collections | Your collections, Templates, Built-in; Build, Schedule, Artwork, Audience inside the editor | New collection or open a collection; Describe it opens a nested drawer |
| Home Designer | Layout, Rows, Overrides | Edit page length, page structure or row constraints; preview and layout save remain in the workspace |
| Viewers | Taste, Activity, Their Home, Limits; taste has Overview, Genres, People & eras, Themes & languages, Corrections | Click a genre, Edit taste corrections or Edit viewer limits |
| Discovery | Watchlists, Ratings, Artwork, Search & recaps | Edit monitoring, Add reminder, Edit rating sources, Edit artwork preferences or Configure search & recaps |
| Live TV | Channels, IPTV sources, Guide preview, Viewer access | Create/Edit channel or source, Edit Live TV access; channel drawer has Basics, Programming, Schedule and Viewers |
| Insights | Now playing, History, Viewing stats, Providers & indexers, Notifications, Weekly report, Delivery history, Hardware | Edit plan price, New/Edit channel, Configure weekly report |
| Libraries & backups | Migration, Local libraries, Backups | Start Plex import, Open CSV tools, Add folder, Match file, Edit backup schedule, Restore backup |
| Playback | Quality profiles, Client rules, Defaults, Playback start, Advanced scoring, Device capabilities | Edit profile, Edit client rules, Edit defaults, Edit startup behavior, Edit custom formats |
| Audio & subtitles | Preferences, Show memory, Intro & credits, Subtitle timing | Edit preferences, Adjust subtitle timing |
| Episode ordering | Search for a show | Select a show to open its ordering drawer |
| Safety & sharing | Viewer policies, Invitations, Clean Home, Remote & security | Edit viewer policy or Invite a friend; policy drawer has Content, Playback limits and Allowed times |
| Settings | Feature navigation; categories inside each editor | Edit the feature, provider or indexer; Prowlarr import belongs with indexers |
| Logs & resolver traces | Latest logs first; traces list | Open a trace for details |

Client rules are edited at **Playback → Client rules → Edit client rules**. Rules
match a device product, an optional platform and an optional named viewer, then
choose an existing quality profile. The first match wins. Owners can choose
common products or enter a custom product name and reorder rules. Device
capabilities is reference information; codec support is still checked at playback.

Import previews and their explicit Import action stay in the import drawer. CSV
imports and exports have a separate viewer selector. Previewing never applies a
migration. Viewer policy requests are cancelled when selection changes, and
invitations copy the selected viewer's saved policy after it has loaded.

Logs sort newest first, retain arrival order for identical timestamps and place
newer arrivals above earlier entries. Live updates refresh without forcing a
scroll to the bottom. History, local media and backup snapshots use AppKit's
searchable, paginated table. The app shell owns the viewport; pages, drawer bodies
and the Designer's two columns each retain their appropriate vertical scroll area.

AppKit still owns drawer animation, focus restoration, Escape handling and scroll
locking. A small Ether wrapper associates each visible title with its dialog name
and automatically selects AppKit's stacked mode for nested editors.
