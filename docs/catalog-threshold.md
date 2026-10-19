# Catalogue popularity threshold — recommendation

Answers the design-doc open question: *"What popularity threshold gives a
useful browsable catalogue: roughly how many titles?"*

## How TMDB popularity behaves

TMDB recomputes its `popularity` score daily from recent views, votes, watch
and favourites activity on TMDB itself. Three consequences matter for Ether:

1. **It is heavily right-skewed.** A handful of current hits score in the
   hundreds or thousands; the median TMDB movie scores well under 1. Counts
   above a threshold therefore grow roughly as a power law as the threshold
   drops — halving the bar much more than doubles nothing in the head and
   adds a long tail of near-invisible titles at the bottom.
2. **It measures *current* attention, not enduring value.** A 1975 classic
   can sit below 2 today while an forgotten streaming original from last
   month sits above 20. A popularity-only catalogue would hide the canon.
3. **Vote count is the complementary signal.** `vote_count` accumulates over
   a title's whole life. Anything with a few hundred TMDB votes is a real
   release real people watched, regardless of this week's attention.

Ether therefore keeps the rule already encoded in
`catalog.MeetsThreshold`: a released title is browsable when
`popularity >= MinPopularity` **or** `vote_count >= MinVotes`. The sync does
one popularity-sorted discover pass and one `vote_count.gte` pass, so both
halves of the OR are fetched efficiently.

## Rough sizes

These are order-of-magnitude estimates (the model in
`catalog.EstimateCatalogSize`, fitted to the shape above and TMDB's public
catalogue scale of roughly a million movies and ~200k shows, most of them
with no votes at all). They are for sizing the setup UI, not measurements.

| Setting (pop / votes) | Movies | Shows | Total | Character |
| --- | ---: | ---: | ---: | --- |
| 20 / 1000 | ~15k | ~5k | ~20k | Hits and the canon only. Browsing feels like a good video shop's front wall. |
| **5 / 100 (default)** | **~71k** | **~26k** | **~97k** | Everything a household is plausibly looking for, plus deep catalogue. |
| 1 / 10 | ~290k | ~100k | ~390k | Deep long tail: straight-to-video, regional and duplicate-adjacent entries start to dominate. |
| 0 / 0 | ~900k | ~180k | ~1.1M | All of TMDB, including titles with no votes, no overview and no artwork. Not a browsable library. |

Two checks on the default row:

- **Storage and sync cost are trivial at ~100k titles.** The mirror stores one
  row plus a few join rows per title — tens of megabytes in SQLite. The
  discover sync is a few thousand paged requests worst case, well inside one
  install's share of TMDB's rate limits at the client's 40 req/s cap, and it
  runs once a day in the background.
- **~100k is the right order for "feels infinite, still findable".** It is
  far larger than any personal Plex library (typically 1–5k titles) and
  comparable to the *visible* catalogue of a major streaming service, while
  facet counts (genre, decade, studio) stay clean because every title in it
  has votes and artwork.

## Recommendation

Keep the shipped defaults: **`MinPopularity = 5`, `MinVotes = 100`**
(`internal/config/defaults.go`), with `ShowUnreleased = true`.

Rationale beyond the table:

- **Search backstops the threshold.** Anything below the bar is still one
  search away, and a searched title permanently joins the browsable set
  (`added_at` is set and never revoked). The threshold only decides what is
  *browsed*, never what is *reachable* — so erring strict costs the owner
  nothing they actually ask for.
- **Raising the bar should not hide known titles.** Sync only ever *adds*
  browsable flags. If an owner raises the threshold later, titles they have
  already watched or searched for stay visible; only new low scorers stop
  appearing. This is deliberate and worth keeping.
- **The settings UI should show the estimate.** `EstimateCatalogSize` exists
  so the admin wizard can say "about 97,000 titles" next to the sliders
  instead of exposing two bare numbers whose effect nobody can predict.
  Present the three presets from the table (Curated ~20k, Default ~97k,
  Maximalist ~390k) rather than free numeric fields.

One caveat to revisit with live data once the bundled key exists: popularity
inflates during awards season and around major releases, so the head of the
distribution breathes by perhaps ±20% in count terms. That does not change
the recommendation — the vote floor keeps the catalogue's *composition*
stable even when popularity wobbles.
