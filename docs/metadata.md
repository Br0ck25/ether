# Episode ordering and Plex metadata identity

Ether keeps TMDB season/episode keys canonical for watch history, resolver
queries, direct parts, and playback. The owner Metadata page chooses a TMDB
episode group per show, either as the default for everyone or an explicit
viewer override. An explicit aired-order viewer override is distinct from
following the owner default; reset deletes the override.

TMDB defines original-air, absolute, DVD, digital, story-arc, production and TV
groups. Ether reads the [show's groups](https://developer.themoviedb.org/reference/tv-series-episode-groups)
and [group detail](https://developer.themoviedb.org/reference/tv-episode-group-details).
The selected group becomes the Plex show/season/episode tree, including detail
children, All Episodes, On Deck, parent navigation, continuous queues and
container expansion. Presentation seasons occupy local season-key slots
9000–9998; 9999 is Other episodes. These are presentation keys only and have
local GUIDs. Canonical episode keys remain unchanged. Display indices are
one-based within each group. Canonical aired season keys are not reused for
alternate groups. This prevents an episode's canonical parent from silently
becoming a different group.

Specials explicitly placed inside a group retain its placement. Remaining
season-zero episodes get a Specials row before or after regular episodes.
Hide excludes all specials from browse lists, including ones inside groups.
Non-special canonical episodes omitted from an incomplete group are retained
in Other episodes. This avoids silently losing new episodes when community
orders lag behind TMDB. Metadata group refreshes are cached for 24 hours,
with presentation layouts cached for one minute and invalidated on preference
changes. No absolute episode number is substituted into a Usenet query.

The owner API is mounted under `/ether/api/metadata`: `GET
/shows/{showKey}/groups`; `GET|PUT|DELETE
/shows/{showKey}/users/{userId}`; `GET
/shows/{showKey}/users/{userId}/preview`. User zero represents the shared owner
default. Save validates group membership against the show's actual group list.
Preview reports both the displayed position and original season/episode.

## Plex GUID spike, read-only, 19 October 2026

The [Plex PMS contract](https://developer.plex.tv/pms/) describes metadata
matching and external `Guid` identifiers. Plex's own
[example metadata provider](https://github.com/plexinc/tmdb-example-provider/blob/main/docs/Metadata.md)
describes `plex://movie|show/{id}` identities. A locally generated arbitrary
24-character ID is not evidence of a Plex identity.

Authenticated read-only GET requests to `https://metadata.provider.plex.tv/library/metadata/matches`
with `type=1|2`, `guid=tmdb://{id}` and `includeGuids=1` returned HTTP 200:

| Canonical title | Verified Plex GUID | Returned external identity |
| --- | --- | --- |
| TMDB movie 550 | `plex://movie/5d7768265af944001f1f6976` | `tmdb://550` |
| TMDB show 1399 | `plex://show/5d9c086c46115600200aa2fe` | `tmdb://1399` |

`internal/plexguid` implements this read-only match, accepting only an exact
external TMDB identity, correct media type, valid Plex GUID syntax and an
unambiguous response. A title/year similarity match alone is insufficient.
Mappings and misses are cached in SQLite; only movie/show detail opens resolve
uncached mappings, with a two-second request deadline. Lists use cached IDs
without network activity. Credential headers are never forwarded on redirects,
and errors omit request URLs and tokens. Existing TMDB fallback GUIDs remain
when lookup fails. No key, database identity or watch-state row is rewritten.

GET detail requests for these two verified identities with `includeExtras=1`
returned zero extras. The show returned a theme attribute; the movie did not.
Both returned four rating entries. These observations do not establish trailer
playability, reusable theme licensing, Common Sense Media access, or remote
watch-state write authorization. The implementation therefore only adopts the
verified identity. It does not invent trailer sources, scrape YouTube, expose
counterfeit downloads jobs, advertise BIF indexes without generated assets, or
infer Plex Pass/cloud rights from the presence of a GUID. Watchlist controls
and cloud history behavior still require official client verification.

Offline tests cover endpoint JSON decoding, owner/viewer inheritance and reset,
foreign-group rejection, omitted episodes, hidden specials, canonical identity,
Plex tree membership and ordering, virtual parent navigation, restricted section
access, exact GUID verification, negative caching and redirect credential
isolation. The owner page has a save/preview/reset interaction test. Physical
Plex iOS/Apple TV behavior is not claimed by these protocol tests.
