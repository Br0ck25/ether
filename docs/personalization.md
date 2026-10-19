# Personalization engine

Viewers only ever use Plex. Everything in this document reaches them
through what Ether returns to Plex: which Home and section rows they get,
in which order, with which titles. The owner sees and steers it in the
`/ether/` UI (Viewers, Home Designer, Insights; API in `docs/owner-ui.md`).

Code: `internal/personalize` (engine), `internal/plex/handlers_hubs.go`
(context and impressions hook), `internal/admin/{viewers,layout,insights}_api.go`
(owner API), `internal/recommend` (detail-page rows, re-ranked with the
engine's scorer).

## Request flow

```
Plex client ──/hubs──▶ plex front end ──HomeHubsFor(user, context)──▶ engine
                         │                                             │
                         │  renders rows, puts its own Continue        │ assembly over the
                         │  Watching where the engine ranked it        │ viewer's cached model
                         ▼                                             │ (milliseconds)
                    response sent ──RecordImpressions(rows)──▶ queue ──▶ SQLite (batched)
State events (progress, stop, scrobble, rating) ──▶ plays + attribution ──▶ debounced rebuild
```

A `/hubs` request never waits on TMDB or the LLM: models are built in the
background and assembly is pure CPU over them. A viewer without a model yet
gets the generic model (popularity rows) while theirs is queued.

## Context

Every `/hubs`, `/hubs/promoted` and `/hubs/sections/{id}` call carries a
`core.HubContext`:

| Field | Derivation |
| --- | --- |
| Device | `core.ClassifyDevice(X-Plex-Product, X-Plex-Platform, X-Plex-Device)`: Apple TV, Android/Google/Fire TV, Shield, Roku, Samsung/LG/Vizio/Hisense TVs, Xbox, PlayStation → `tv`; Plex HTPC, Plex for Windows/Mac/Linux, Plex Media Player → `desktop`; Plex Web in any browser → `web`; Plex for iOS/Android phones and tablets → `mobile`. Unknown clients fall back on their platform, then `web`. |
| Daypart | Server local time: morning 05–12, afternoon 12–17, evening 17–22, late 22–05. |
| Weekend | Friday from 17:00 through Sunday. |

## Signals

| Signal | Source | Effect |
| --- | --- | --- |
| Completion | state history / view count | +1.0 per film (+0.3 per rewatch, max +0.6); shows +0.6…+1.8 by finished episodes |
| In progress | resume point | +0.1 … +0.5 by progress |
| Abandon | a play stopped before 10% and never resumed past 25%, or stalled under 30% for 14 days | −0.5 / −0.6 |
| Rating | the viewer's rating | +(rating − 5) / 5 × 1.2 |
| Impression without play | shown on 3+ days in 30, never started | −0.06 per day beyond 2 (max −0.3), half weight on features; plus item fatigue in ranking |
| Trakt history | plays from other apps | completion, when the local log has none |
| Owner override | `PUT viewers/{id}/taste/overrides` | replaces the learned weight |

Each title's weight decays with a 120-day half-life and is clamped to ±2.
Episodes fold onto their show.

## Taste model

The 150 strongest titles spread their weight onto features:

| Family | Weight per title | Normalisation floor |
| --- | --- | --- |
| Genres (TV's combined genres split: "Sci-Fi & Fantasy" → Science Fiction, Fantasy) | 1.0 | 3 |
| Keywords (first 15) | 0.5 | 2 |
| Directors / writers (3) / top-5 cast | 1.0 / 0.6 / 0.8…0.4 | 2 |
| Decade | 1.0 | 3 |
| Original language | 1.0 | 3 |
| Franchise | 0.8 | 1.5 |

Each family is divided by its strongest |value| or its floor, whichever is
larger, giving weights in −1…1: a heavy viewer's favourite genre is 1.0, a
new viewer's single film is a modest 0.33. Overrides are applied last.
Confidence is `1 − exp(−mass / 5)` where mass sums positive evidence and
half of negative evidence; personal rows start at 0.08.

**Fit** of a title, roughly −1…1, explained by its strongest part:

```
fit = 0.40·genre + 0.15·keywords + 0.25·people + 0.08·decade + 0.05·franchise + language
```

- genre: mean of the title's genre weights; a genre below −0.3 drags it
  down; one at −0.6 or below (usually "not into horror") vetoes the title.
- keywords: sum / √n, capped ±1. People: best + half the second.
- language only counts when it distinguishes: bonuses for each language a
  multilingual viewer watches, a small penalty for languages a
  monolingual viewer never watches.

**Habits** (shown on the Viewers page, used for context):
weeknight and weekend minutes per active day, peak start hours, preferred
device (from attributed plays), binge score (share of episodes in
same-day runs of 3+), rewatch score. The model also learns **genre lifts
per daypart** (what the viewer watches more of in the afternoon than at
night: family films before evening on shared accounts) and the **film
share per weekday/weekend × daypart**.

## Rows

Generators run at build time and propose candidate rows with scored
items and reasons. Kinds are the owner-facing identities used by layout
rules and insights.

| Kind | Items | Notes |
| --- | --- | --- |
| `continue-watching` | in progress | Plex's own row; the engine only ranks it |
| `up-next` | next episode of recently finished shows | |
| `top-10-today` | "Top 10 on <server> Today": TMDB trending (day, week) blended with catalogue popularity and what viewers here watched this week, vote-floored | 10 items; leads new viewers' Homes |
| `top-10` | best fits from a shared pool (similar to favourites, trending, top rated, popular, favourite genres), weighted rating ≥ 6.8 | 10 items; needs taste confidence ≥ 0.25 |
| `genre-showcase` | "Acclaimed <Genre>": acclaimed (full vote floor, weighted ≥ 7.0) popular titles of one broad genre | new viewers only: three, one per genre family (thrills / light / serious), rotating daily — what they play is the first taste evidence |
| `because-you-watched` | TMDB similar to a recent favourite | up to 3 per screen, rotating daily, recent seeds favoured |
| `more-from-person` | a director/writer/actor with affinity ≥ 0.35 | up to 2 |
| `trending-in-your-taste` | trending titles with fit > 0.08 | |
| `hidden-gems` | half the vote floor, weighted rating ≥ 7.0, under the 40th popularity percentile, fit ≥ 0.12 | |
| `new-this-week` | released in 7 days (21 if thin) in the top genres | |
| `comfort-rewatches` | finished favourites (rewatched, rated 8+, or 6+ episodes) not seen for 45 days | |
| `something-different` | acclaimed titles (full vote floor, weighted ≥ 7.2) outside the top genres, nothing disliked | exploration |
| `short-on-time` | films ≤ 100 min; series only with episodes ≤ 35 min **and** a short commitment (≤ 30 episodes) or self-contained sitcom episodes (comedy, not animation, ≤ 250 episodes); never news/talk/reality/soap | ordered by runtime fit to the context and the viewer's film/episode habit; missing runtimes and episode counts are fetched for the best candidates (80 per build) |
| `finish-the-franchise` | the unwatched rest of a started TMDB collection, in order | |
| `collection:<id>` | owner collections targeted at the viewer and context (`core.CollectionRows`) | owner's order |
| `llm` | themed rows from the owner's LLM, seeded by favourites | optional |
| `trakt-recommendations`, `trakt-watchlist` | connected Trakt account | optional |
| `trending`, `popular-on-server` | catalogue-wide | |
| `new-releases` | released in the last 120 days, notable only, by recency and popularity | |
| `top-rated` | 1.5× the vote floor, weighted rating ≥ 7.4, by weighted rating | sections, and new viewers' Homes |
| `popular`, `airing-today` | TMDB lists | sections only |

Discovery rows never contain anything the viewer has watched, started or
given any signal about; every row honours the viewer's content-rating
limit and allowed sections.

### Quality guards

TMDB ratings from few votes are noise (a new release at 9.1 from 470
votes), so:

- **Weighted rating** (IMDb-style): `(v·R + m·C) / (v + m)` with C = 6.4
  and m = 1,000 for films, 300 for series. It drives `quality` everywhere.
- **Vote floor** per type, scaled to the catalogue: half the median vote
  count of its 200 most popular titles, capped at 1,000 films / 300
  series and at least a tenth of that. Rating-driven rows ("acclaimed")
  need a multiple of it, and titles released in the last year need three
  times as many votes (early ratings come from fans).
- **Notable**: every discovery row except what viewers here watched, Trakt
  and LLM picks skips titles that are not: the full vote floor; a fifth of
  it with TMDB popularity ≥ 10; or a release of the last 120 days with 25+
  votes and popularity in the top quarter of recent releases. Titles rated
  under 6 by 20+ voters, or with a weighted rating under 6 on 200+ votes,
  are never notable.

### Cold start

Below taste confidence 0.3 a viewer's Home leads with the curated
cold-start rows: Top 10 today (×1.8), three genre showcases (×1.5), new
releases (×1.6), plus Top Rated. Top 10 for You waits for confidence 0.25;
Because you watched and the other personal rows start at 0.08 and take
over as evidence grows.

## Assembly

For a request (viewer, context, Home or section):

1. **Items**: each item's context score is its generator score plus
   context adjustments: −0.12 × fatigue (shown repeatedly without a play),
   the daypart genre lift, runtime fit to the time the viewer has
   (target: phone 40 min, weeknight 45–60, weekend evening TV 140,
   blended with the viewer's real session length), and a small
   date-seeded nudge so near-ties rotate daily.
2. **Diversity**: MMR (λ = 0.75) over genres, franchise and director, so a
   row is not one franchise wall-to-wall.
3. **Row score** (shown as `score`):
   `Thompson sample of the play rate × context multiplier × content strength × row scale × daily rotation`.
   The play rate is a Beta posterior per viewer and kind: a hand-set prior
   per kind feeds the server-wide estimate (prior worth 30 row-days),
   which feeds the viewer's own (worth 40 row-days). Rewards: finished 1.0,
   past half 0.85, started 0.6, abandoned 0.1. The sample is seeded by
   viewer, date, context and kind (exploration 0.5 by default): stable
   within a day, re-explored daily, and shrinking as evidence grows.
   Context multipliers: Short on Time ×1.3 weeknights, ×1.4 on phones,
   ×0.55 on Saturday-night TVs; Comfort ×1.3 late; film-heavy rows up on
   weekend-evening TVs and where the viewer watches films, down on phones.
4. **Layout** (Home Designer, three layers, see below): each row's
   effective policy is the viewer's override if one matches, else the
   Global policy, else the defaults (eligible). Off rows, rows outside
   their schedule and rows over their days-per-week cap leave the pool;
   `priority` multiplies the score.
5. **The page**: per-page caps (a policy's `maxPerPage`, else the kind's:
   Because you watched 3, More from 2, showcases 3 (one per genre
   family), franchise 2, LLM 2; at most 4 owner collections unless a
   policy places them). Rows that must be there (Always, anchored) are
   selected first, then the eligible rows by score up to the page length
   (viewer device → viewer page → Global device → Global page → 14 on
   Home, 10 in sections; Plex's own rows not counted).
6. **Slots**: the solver (`slots.go`) puts anchored rows on their slot (an
   override beats a Global anchor on the same slot; the loser takes the
   nearest free slot; an anchor beyond the page sits last), then fills the
   free slots in score order while honouring "never above"/"never below"
   bounds: each slot takes the best-ranked row that may sit there and
   leaves the rest a feasible arrangement (earliest-deadline-first check).
   An eligible row whose "never above" bound lies past the end of the page
   is not shown. On Home the engine always places Continue Watching (an
   empty placeholder when nothing is in progress), so slot numbers mean
   the same for every viewer.
7. **Dedupe**: titles are allocated so each appears once per screen:
   anchored-content rows (franchise, Up Next, owner collections,
   watchlist) keep theirs, the rest draft titles in turns in display
   order so no row is starved by a greedy one above it. Rows left under
   their minimum are dropped, the next eligible row moves up and the page
   is arranged again.

Every row carries its provenance: `layer` (`global` for a Global
anchor or Always row, `personalized` when the engine chose and ordered
it, `override` when the viewer's override decided it), `slot`,
`anchorSlot`, the effective `policy` and its layer, and `placement`, a
sentence on why it sits there ("Anchored to slot 1 in Global Home.",
"2nd of 11 by predicted engagement (score 0.41); moved up to slot 3:
never below slot 3 in Global Home."). Owner previews also list the rows
that are not on the page, with `hiddenReason` (off, out of schedule,
frequency or per-page cap, page full, too few titles after dedupe);
`/hubs` skips that work.

Assembled screens are memoised for two minutes, keyed by everything they
depend on (model version, layout version, statistics, owner rows,
Continue Watching, hour), so polling clients cost nothing.

## Home Designer layers

Like Netflix's homepage (a candidate pool of rows, page-level
constraints, then per-profile personalisation), a page is composed from:

1. **Global Home**, the owner's template: for any row in the pool a
   status (`always`, `eligible`, `off`), an anchored slot, bounds
   ("never above slot N", "never below slot N"), a priority, a per-page
   cap, a days-per-week frequency cap and a schedule (days, hours,
   dayparts, devices, months, date range: "a seasonal row only in
   December"); plus the page length per section and device. Rows without
   a policy are eligible.
2. **Personalized**: the engine's ranking above, within those
   constraints.
3. **Per-viewer overrides**: the same policies and page lengths for one
   viewer, replacing the Global setting for that row; "reset to global"
   deletes the override. An override can place an owner collection that
   is not targeted at the viewer (`collection:<id>`, fetched through
   `core.CollectionRowLookup`); a Global placement still honours the
   collection's audience.

Policies match a row by hub id, kind or glob; the most specific match
wins within a layer and an override always beats Global. Frequency caps
count the days a row kind was shown to the viewer (row impressions): it
may appear when it was shown on fewer than N of the previous six days, and
stays for the rest of a day it was shown on. Previews of unsaved drafts
run the same assembly without touching the memo or the saved layout;
`layout/impact` assembles every viewer's page with the saved layout and
the draft and reports what changes.

The previous flat rule list (pin / hide / boost / bury) is converted to
this model once, on the first start of this version (pin at position p →
anchor slot p+1, hide → off, boost → priority 1.5, bury → priority 0.3,
scoped rules → overrides); approximations are kept as notes for the
owner.

## Feedback and storage

- **Impressions**: after each hubs response the front end hands the rows
  it wrote to the engine, which queues them without blocking (dropping
  under extreme load) and writes batches every 2 s. Rows are compacted on
  write: one row per viewer, day and hub, one per viewer, day, item and
  hub, however often the client polls. Item impressions are kept 60 days,
  row impressions and plays 400.
- **Plays**: watch-state events become plays (a new play after 30 minutes
  of silence), labelled with the device of the viewer's latest Home, the
  daypart and weekend, and attributed to the most recent row that showed
  the title (or its show) in the previous 48 hours. Stopping before 10% is
  an abandon. Plays of one title on one day count once.
- **Rebuilds**: a stop, completion, scrobble or rating schedules the
  viewer's rebuild 20 s later (bursts coalesce); a scheduler rebuilds
  active viewers' models every 3 hours; the owner can rebuild now.

Tables: `personalize_row_impressions`, `personalize_item_impressions`,
`personalize_plays`, `personalize_overrides`, `personalize_home_layout`
(the Home Designer document) and `personalize_layout` (the legacy rule
list, read once to convert it).

## Owner API

See `docs/owner-ui.md`. `viewers/{id}/home` and `layout/preview` run the
same assembly as `/hubs` and add the rows the front end builds itself
(source `plex`); rows that are not on the page are listed last with
`hidden: true` and a reason, and are never sent to Plex. `layout/preview`
takes an unsaved draft and previews the typical viewer (the generic
model) when no viewer is given. Insights count row-days shown and
attributed plays per row kind.

## Wiring

```go
eng, err := personalize.New(personalize.Options{
    DB: db, Catalog: catalog, State: state, Events: state, Users: auth,
    Collections: collectionRows, // core.CollectionRows, optional
    LLM: llmService, Trakt: traktClient, Playlists: state.Playlists(),
    Base: recommender, // detail-page rows
    ServerName: func() string { return cfg.Get().Server.FriendlyName },
    Logger: log,
})
recommender.SetScorer(eng)        // personal "More Like This"
plexDeps.Recommender = eng        // + ContextualRecommender + ImpressionSink
adminDeps.Personalization = eng
defer eng.Close()
```
