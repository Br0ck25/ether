# Discovery through Plex

Discovery is configured by the owner in **Discovery**. Viewers browse, search,
watch, and dismiss Continue Watching through their existing Plex apps.

## Watchlists and release checks

Monitoring starts disabled. Once enabled, the cancellable background worker
syncs each viewer every 30 minutes. The owner controls the minimum interval
between release checks (1–168 hours, default six). One pass checks at most 20
explicitly watched-for titles per viewer. Released films and released episodes
of watched-for shows/seasons must resolve to a healthy NZB before they move
from Coming Soon to Now available. Unknown release dates are not treated as
confirmed releases. Confirmed missing releases retract availability; transient
upstream failures preserve the last successful snapshot.

Plex ingestion uses the account's discover Watchlist endpoint, authenticating
with an account token in the request header. The app supplies its owner token
and may obtain unprotected managed Home accounts' tokens through the owner's
authorized Home switch operation. PIN-protected accounts need their own
authorization; the service does not guess PINs. Sharing a server does not grant
access to a friend's private Watchlist, and a server access token is not treated
as an account token. Friends can use their connected Trakt account. The source
interface also accepts an explicitly authorized account-token provider.

Snapshots are per viewer and per source. Failed source syncs do not delete the
last successful snapshot. Ingested/reminded titles and rendered rows pass the
adult, certification, library-section, and owner-policy guards. Adult titles
are excluded even for the owner. Manual owner reminders use the same guard.

Optional availability notifications enqueue `title.available` to the configured
owner notification channels. These do not disclose which viewer requested the
title. Optional prewarming calls the engine's low-priority opening-segment
prefetch. Rechecks do not repeat successful notification or prewarm effects
until a confirmed unavailable → available transition occurs.

## Ratings and artwork

The owner supplies an OMDb or MDBList key. Settings redact configured keys on
read. OMDb looks up the title's IMDb ID. MDBList uses the documented
`/tmdb/{movie|show}/{id}/` lookup. Successful responses are cached in SQLite for
an owner-controlled interval. Metadata rendering reads only the cache and
queues bounded background lookups; provider failures back off for 15 minutes.
No API key is included in a transport error returned to the browser.

Each badge preserves the source scale: IMDb /10, Rotten Tomatoes critic and
audience /100, Metacritic /100, Letterboxd /5. Plex's main numeric fields use
/10 with the corresponding Plex-supported IMDb/Rotten Tomatoes icon. Missing
scores stay absent; Metacritic and Letterboxd remain named poster badges.
OMDb has no fabricated Rotten Tomatoes audience or Letterboxd score.

Poster variants use pure-Go image decoding, drawing, and encoding. TMDB image
paths are validated; external URLs and traversal are rejected. The generator
bounds image dimensions and its in-memory cache. Status, verified quality, and
ratings change the versioned poster URL. Private response caching and the
mandatory viewer guard apply to poster, backdrop, and logo requests. Plex
`Image` children and rating tags match the decorated top-level fields.

Owner artwork choices include pinned posters/backdrops and optional genre or
person focus tags. Automatic selection compares those tags against the viewer's
learned taste, prioritizes language matches, and supports textless backdrops.
The owner supplies focus tags; TMDB does not supply actor/genre tags for each
individual image. The preview uses unsaved display settings without persisting
them. Status and quality badges require evidence supplied by the app's facts
callback; estimated media fields never become quality availability claims.
The “Last chance in row” badge compares the viewer's actual current and next
configured rotating-collection subsets after personal filtering, ordering, and
limits. Its deadline is the next daily/weekly rotation boundary in the collection
clock's timezone, including DST. It is absent for unbuilt/disabled collections,
untargeted viewers, or titles that remain in the next subset. This describes
leaving a collection row, not a loss of playback availability.

Localization uses the Plex request language and TMDB's language/image-language
parameters. Cached translations are per title and language, never written over
the canonical catalogue title. A bounded worker refreshes translations so a
cold Plex grid does not serially wait on TMDB. Missing translations retain the
canonical text/artwork. Canonical identity and safety fields remain unchanged.

## Optional LLM and local history features

Natural-language search and recaps default off. They use the owner's existing
LLM configuration. Model-suggested titles are resolved through the catalogue;
unresolvable suggestions are dropped, and failed/disabled natural search falls
back to ordinary catalogue search. The final Plex visibility guard still runs.

Recaps send only the current viewer's completed episode overviews up to the
requested season/episode boundary. They do not send unseen episode titles or
summaries. Instructions restrict the model to those supplied facts; generated
text is not guaranteed to be perfectly factual. Metadata serves cached recaps
while a bounded worker prepares them.

Year in Review contains deduplicated completed titles from one calendar year,
using the caller's private history. It is exposed as a Home row and through a
collection-engine adapter as viewer-owned movie/show collections. Virtual
collection keys reserve SmartKey payloads beginning at 900000000000000; owner
smart-collection operations continue through the original engine. Rediscover
contains unfinished titles whose last play was at least 30 days ago. Removing
Continue Watching stores a persistent show-level dismissal, so later episodes
and repeated timelines do not immediately restore the row. Dismissals are per
viewer; an explicit restore method removes them.

## Plex push spike

Read-only reference PMS logs recorded POSTs
to `https://notifications.plex.tv/api/v1/notifications` and HTTP 201 responses
during account event delivery. It does not contain the request payload,
required identity assertions, or a supported authorization contract. This proves
that the reference PMS used that endpoint, not that Ether can legitimately
produce mobile pushes. Ether does not send requests to that endpoint;
configured notification channels remain the supported transport. No live account
writes or notifications were performed for this work.

## Primary API references

- [Plex PMS API](https://developer.plex.tv/pms/): metadata, image, hub and rating semantics.
- [OMDb API](https://www.omdbapi.com/): owner-key lookup parameters and response formats.
- [MDBList API](https://api.mdblist.com/), [published OpenAPI schema](https://api.mdblist.com/schema/): authenticated title lookups and provider IDs. The schema was inspected directly.
- [TMDB movie translations](https://developer.themoviedb.org/reference/movie-translations), [image languages](https://developer.themoviedb.org/docs/image-languages): language matching and textless-image fallback.

The cloud Plex discover Watchlist endpoint is not covered by the published PMS
contract. Its account-token scope remains an integration constraint; failures
never cause Ether to substitute another viewer's credentials.
