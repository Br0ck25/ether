# Plex Media Server API notes

What `internal/plex` mirrors, where the shapes came from, and what the spike
still has to answer. Keep this current when a client teaches us something.

## Sources

| Source | Used for |
| --- | --- |
| Plex's official PMS OpenAPI contract (developer.plex.tv/pms, API 1.2.3, PMS 1.43.x) | Endpoint list, parameter names, decision/start parameters, media-query grammar, `X-Plex-Client-Profile-Extra` grammar, media-provider feature list, paging rules |
| Community spec `github.com/LukeHagar/plex-api-spec` | Cross-check of endpoint paths (`/myplex/account`, `/servers`, `/security/resources`) |
| `python-plexapi` source | Attribute names clients actually parse (Media/Part/Stream, play queues, playlists) |
| A reference PMS 1.43.4 (read-only GETs and decision probes, redacted) | Byte-level shapes: attribute order and JSON types, hub identifiers/keys/contexts, decision responses for Plex Web/iOS/Apple TV header sets, section listings, filters/sorts/fields |
| `testdata/pms` fixtures + `cmd/pmsdiff` (main) | Structural diffs of Ether against the reference; see [Conformance](#conformance-pmsdiff) |
| The Plex Web bundle served by the reference PMS (read-only, not copied) | `hasMDE=1` on decisions, which `*DecisionCode` fields the player reads, `/library/search` usage |

No third-party code was copied; there are no notices to add.

## Encoding (internal/plex/model)

One struct model renders as either format (`model.Marshal`).

- XML by default; JSON with `Accept: application/json`. Content types are
  `text/xml;charset=utf-8` and `application/json`, both with
  `X-Plex-Protocol: 1.0` and `Cache-Control: no-cache`.
- XML booleans are `1`/`0`; JSON booleans are `true`/`false`. JSON children
  are arrays keyed by element name (`"Metadata": [...]`, `"Directory"`, `"Hub"`)
  except single objects PMS emits as objects: `Meta`, `User`, `Player`,
  `Session`, `TranscodeSession`, `SearchResult.Metadata`, `Related`, `OnDeck`.
- Items: XML element is `Video` for movie/episode, `Directory` for
  show/season/collection, `Playlist` for playlists; JSON puts all of them
  under `Metadata`.
- `MediaContainer` attributes: `size`, `totalSize`, `offset` first, then the
  rest alphabetically (PMS does this everywhere). Item attributes follow PMS's
  fixed order (ratingKey, key, parentRatingKey, grandparentRatingKey, guid...).
- The root element is always written as an open/close pair
  (`<MediaContainer size="0">\n</MediaContainer>`); children self-close with
  `" />"`. `'` is escaped as `&#39;` and `"` as `&#34;`, like PMS.
- Decision responses are special: every element's attributes are
  alphabetical with `decision`, `selected`, `location` appended (a Stream
  sorts `selected` with the rest); a transcoded Media/Part lists the
  attributes kept from the source first (`audioProfile hasVoiceActivity id
  videoProfile`), then its own. In JSON `allowSync`, `librarySectionID`,
  `mediaTagVersion`, Media/Part/Stream/tag `id`, `hasVoiceActivity`,
  `canNormalizeLoudness`, `aspectRatio` and Rating `value` are strings
  (`"id": "221632"`, `"allowSync": "1"`, `"aspectRatio": "1.85"`),
  `UltraBlurColors` is an array (it is an object everywhere else), and the
  item has no `Image` or `Guid` children.
- Children order on items: Media, Image, UltraBlurColors, then Genre,
  Country, Guid, Rating, Collection, Director, Writer, Role, Producer,
  Label, Similar; detail pages end with Extras then Related. List items that
  carry Guid (related hubs) put it before Genre.
- Quirks preserved: `Media.optimizedForStreaming` is an int in JSON while
  `Part.optimizedForStreaming` is a bool; both only appear for MP4/MOV.
  Season-children containers carry `grandparentRatingKey` as an int.
  Collections report `smart` as the string `"1"`/`"0"`.
- Bodies over 1 KiB are gzipped when the client accepts it
  (`X-Plex-Content-Original-Length` set).

## Request context

All `X-Plex-*` values are read from headers or the query string (clients put
the token in image and stream URLs). Paging uses `X-Plex-Container-Start/Size`
(header or query); responses carry `offset`/`totalSize` and the
`X-Plex-Container-Start` / `X-Plex-Container-Total-Size` headers. A size of 0
returns only the total. Unpaged lists are capped at 500 items.

LAN vs remote: loopback, RFC 1918, link-local, ULA and 100.64/10 (Tailscale)
are LAN. `X-Forwarded-For`/`X-Real-IP` are honoured only from a loopback peer.
Remote clients get `Transcode.RemoteMaxBitrateKbps` as their bitrate cap.

### Client capabilities

Base capabilities come from `X-Plex-Product` / `X-Plex-Platform` /
`X-Plex-Device` (Apple TV and iOS: HEVC + HDR10 + Dolby Vision, MKV direct
play; Plex Web: by browser, Safari adds HEVC/HDR and AC3/EAC3, Chrome/Edge add
VP9/AV1; Android TV/Shield: HEVC/HDR; generic: H.264/AAC 1080p). They are then
refined by:

- `X-Plex-Client-Profile-Extra` directives (raw, URL-encoded, or query-decoded
  with `+` turned into spaces): `add-transcode-target`,
  `add-transcode-target-codec`, `append-transcode-target-codec`,
  `add-direct-play-profile` add codecs/containers; `add-limitation` applies
  `video.height`/`video.width`/`video.bitrate` upper bounds, an 8-bit
  `video.bitDepth` cap on HEVC/AV1 disables HDR, `video.DOVIProfile notMatch *`
  disables DV.
- The legacy `X-Plex-Client-Capabilities` header (`videoDecoders=h264{resolution:1080}`...).
- Decision parameters `maxVideoBitrate`, `videoBitrate`, `videoResolution`,
  `location=wan|cellular`.

## Auth

Every endpoint except `/identity` needs a token that `core.Auth` maps to a
user; otherwise PMS's HTML `401 Unauthorized` body. Auth backend errors other
than "unauthorized" return 503 (so clients don't sign the user out on a
plex.tv hiccup). Owner-only: `/:/prefs`, `/accounts`, `/butler`, `/updater/*`,
section refresh, `/status/sessions/terminate`. Shared users only see their
`AllowedSections`; content-rating limits are passed to the catalogue as
`Filter.MaxContentRating`.

CORS: like PMS, CORS headers are only added when the request has an `Origin`
(Plex Web); the origin is echoed, preflights answer 200 with the requested
headers allowed.

## Endpoints

| Endpoint | Notes vs real PMS |
| --- | --- |
| `/identity` | `size=0 apiVersion claimed machineIdentifier version`. Version defaults to `1.43.4.10903-e5521bd8c`; clients gate features on it. |
| `/` | Full ServerConfiguration attribute set + root `Directory count/key/title` list. Capabilities are reported honestly: no sync, no live TV, no camera upload, transcoder flags follow whether a transcoder is wired, `myPlexSubscription`/`ownerFeatures` from `Identity`. |
| `/media/providers` | One `com.plexapp.plugins.library` provider with content (Home + sections + Playlists, each with PMS pivots), search, match, metadata, rate, imagetranscoder, promoted, continuewatching, actions, playlist/playqueue (`universal`), collection, timeline (+scrobble keys), queryParser, universalsearch. No `manage` or `subscribe` feature (Ether has no library management or downloads). Container omits `hubSearch/itemClusters/mediaProviders/multiuser/transcoderPhoto` exactly as PMS does here. |
| `/library/sections` | PMS shape including `Location`. Sections default to 1 = Movies, 2 = TV Shows (`Deps.Sections` overrides; the spike uses Movies only). |
| `/library/sections/{id}` | Legacy secondary listing (`all`, `unwatched`, `newest`, `recentlyAdded`, `recentlyViewed`, `onDeck` "Continue Watching", `collection`, `genre`, `year`, `decade`, `country`, `contentRating`, `firstCharacter`, `search?type=N`). `includeDetails=1`/`includeMeta=1` adds `Meta`. |
| `/library/sections/{id}/all` | Filters: `type`, `genre`, `year` (incl. `year>>=` style operators), `decade`, `contentRating`, `country`, `studio`, `collection`, `actor/director/writer` (TMDB person id), `title`, `audienceRating>>=`, `duration`, `unwatched=1`, `limit`. Sorts: `titleSort`, `originallyAvailableAt`, `rating`, `audienceRating`, `addedAt`, plus Ether's `popularity`. `includeMeta=1` returns `Meta` (Type with Filter/Sort/Field + FieldType operators) mirroring PMS's. Items in section listings omit `librarySection*` (the container has them). `unwatched` scans up to 5000 titles and filters by watch state. |
| `/library/sections/{id}/{genre,year,decade,contentRating,country,studio}` | `Directory fastKey key title type`, values from `Catalog.Facet`. Filter values may be facet keys or titles; they are mapped back to facet titles for `core.Filter`. |
| `/library/sections/{id}/firstCharacter` | Needs `Catalog.Facet(t, "firstCharacter")` (the spike implements it); empty otherwise. |
| `/library/sections/{id}/categories` | Genre tiles (`type="directory"`) + `Meta`. |
| `/library/sections/{id}/collections` | `Collections.List`; collection items are `/library/collections/{key}/children`. |
| `/library/metadata/{keys}` | Comma lists supported. Detail adds all tags with `id`/`filter`, `Guid` children (imdb/tmdb/tvdb), `Rating` (TMDB audience), streams. `includeChildren`, `includeOnDeck`, `includeRelated`, `includeExtras` honoured; `checkFiles`, `asyncAugmentMetadata` etc. accepted and ignored. Collections and playlists resolve here too. |
| Items everywhere (listings, hubs, children, search, detail) | `Image` children per PMS: movie/show `coverPoster`, `background`, `clearLogo`; season `coverPoster` (season, else show), `background`, show `clearLogo`; episode show `coverPoster`, `snapshot` (still), show `background`, show `clearLogo` (only artwork Ether can serve; no `backgroundSquare`). `UltraBlurColors` on movies, shows and collections (see below). `slug` from the title (`grandparentSlug`/`parentSlug` on episodes/seasons). Seasons carry `parentStudio`/`parentYear` and no dates, ratings, sort title or genre/country; episodes carry the show's `contentRating` and no `year`, sort title or genre/country. Lists show three plain tags per kind (shows: Role only); episodes in hubs and On Deck render in full (tag ids, Guid, Rating, streams) except in the Home hero row, as PMS does. Related hubs add `Guid` (before the tags) and omit `librarySection*`/`promoted`. |
| Title enrichment | TMDB list results (most hub rows) carry no credits, studio, certification, tagline or logo. The first render schedules a background `Catalog.Get` (3 workers, 512-deep queue, duplicates coalesced, 6 h cache of 4000 trimmed titles, failures retried after 30 min); later renders merge the detail in. Seasons/episodes use the cached show for clear logo, studio, year and rating. Listings never wait for it. |
| UltraBlurColors | Computed from the TMDB `w300` backdrop (through the artwork cache): each quadrant's mean colour is mapped into PMS's dark, saturated range (HSL lightness `0.10 + 0.35·L`, saturation `1.4·S + 0.15` unless near-grey). PMS picks palette colours, so hues match but values are not identical. Lazy: 2 workers, results kept in memory (50 000 entries) and in `plex_ultrablur` (key, backdrop path, four colours); renders batch-load stored colours, a changed backdrop is recomputed, failures retried after 1 h. |
| `/children`, `/allLeaves`, `/grandchildren`, `/parent`, `/grandparent`, `/similar`, `/related`, `/onDeck` | Show children include PMS's "All episodes" `Directory` (suppressed with `excludeAllLeaves=1`); seasons/shows get `viewedLeafCount` from watch state. |
| `/library/metadata/{key}/{thumb,art,clearLogo,...}/{ts}` | Proxied from TMDB through an in-memory LRU (128 MiB) with request coalescing. |
| `/photo/:/transcode` | `url` may be one of our artwork paths (relative or absolute to this server / localhost / plex.direct) or an absolute TMDB URL; anything else is 403 (not an open proxy). No pixel work: the closest TMDB width variant ≥ the requested width is served (`w92`…`w1280`, `original`). |
| `/hubs`, `/hubs/promoted` | Continue Watching first (`home.continue`, key `/hubs/home/continueWatching`, style `hero`), then `Recommender.HomeHubs`, then "Recent Playlists" (`home.playlists`, the user's playlists, only when there are some and no `contentDirectoryID`). `contentDirectoryID`, `excludeContinueWatching`, `identifier` honoured. |
| `/hubs/continueWatching` (+`/items`) | `hubIdentifier="continueWatching"`, `context="hub.continueWatching"`, key `/hubs/continueWatching/items`; items carry `includedAt` (their last view). |
| `/hubs/sections/{id}` | `movie.inprogress.{id}` / `tv.inprogress.{id}` (key `/hubs/sections/{id}/continueWatching/items`), `movie.recentlyadded.{id}` (key `/library/sections/{id}/all?sort=addedAt:desc`) / `tv.recentlyadded.{id}` (key `/hubs/home/recentlyAdded?type=2&sectionID={id}`), then `Recommender.SectionHubs`. Hubs keep `size=0` rows like PMS. |
| `/hubs/items?identifier=` | Re-fetches a generated hub with more items (count / container size). Ether hubs without a backing collection use this as their `key`. |
| `/hubs/search`, `/search`, `/library/search` | Hub search returns Movies/Episodes/Collections/Shows hubs (empty ones included) with `score` strings; an empty `query` is PMS's HTML 400. `/library/search` (Plex Web's universal search) returns `SearchResult{score, Metadata}` with `librarySectionTitle` but no section id/key, like PMS. |
| `/library/onDeck`, `/library/recentlyAdded`, `/hubs/home/*` | From `State.InProgress` and `Catalog.Browse(SortAdded)`. On Deck items lead with `allowSync` (false) and `librarySectionUUID`; episodes in full. |
| `/library/sections/{id}/prefs` | The section settings apps read (`hidden`, `collectionMode`, `originalTitles`, `enableBIFGeneration`, `enableCreditsMarkerGeneration`; movies `enableCinemaTrailers`; shows `episodeSort`, `showOrdering` = `tmdbAiring`, `useSeasonTitles`, `flattenSeasons`, `enableIntroMarkerGeneration`) with Ether's real values; `default`/`value` are strings here, as in PMS. Writes are ignored. |
| `/playlists...` items | Playlists report `duration` (sum of item runtimes) once every item's runtime is in the title cache. |
| `/playlists...` | List/create (`uri=` or `playQueueID=`), get, delete, items (`playlistItemID` = 1-based position), add (`uri=`), remove, clear, move (`after=`). Renames are not in `core.Playlists` and are ignored. |
| `/playQueues...` | In memory (6 h idle expiry, 2000 queues). Create from `uri` (server://, library://item, library://directory, plain paths; shows/seasons/collections/playlists expand to playable items) or `playlistID`; `key` selects the start item; `continuous=1` on an episode queues the rest of the show; `shuffle`. Get with `window`/`includeBefore`/`includeAfter`; add (`next=1` = play next), remove, move, shuffle/unshuffle/reset/clear. Timeline `playQueueItemID` moves the selection. |
| `/:/timeline` | `State.Progress`, session tracking for `/status/sessions` and `playing` notifications, `Playback.Touch` (optional `plex.SessionToucher`), transcode keep-alive, `Hooks.OnTimeline`. `state=stopped` stops the play and transcode sessions. Owner kills are delivered as `terminationCode="2006" terminationText=...` on the next timeline (this is how PMS shows a message on the client). |
| `/:/scrobble`, `/:/unscrobble`, `/:/rate`, `/:/progress`, `/actions/removeFromContinueWatching` | Shows/seasons scrobble every episode. Remove-from-continue-watching is `Progress(offset 0, "stopped")` (core has no dedicated call). |
| `/status/sessions` | Our timeline-tracked sessions with `User`, `Player`, `Session`, `TranscodeSession`; shared users see only their own. |
| `/:/websockets/notifications`, `/:/eventsource/notifications` | Real WebSocket (coder/websocket, origin check off: Plex Web is on app.plex.tv) with 30 s pings; SSE with 10 s `ping` events. `filters=` respected. Messages: `{"NotificationContainer":{"type":"playing","PlaySessionStateNotification":[...]}}` and `timeline` entries (state 5) on watched-state changes. |
| `/myplex/account` | Root element `MyPlex` (not MediaContainer), `mappingError=""` always present. |
| `/:/prefs` | Read-only subset with typed `default`/`value` (bool/int/string in JSON). Writes are accepted and ignored (Ether is configured in its own UI). |
| `/servers`, `/clients`, `/devices`, `/accounts`, `/system/agents`, `/activities`, `/butler`, `/security/resources`, `/security/token`, `/updater/status` | Minimal but well-formed. `/security/token` returns the caller's own token as the delegation token. |
| anything else | Logged at debug, empty `MediaContainer` 200 (what PMS does for many probe paths). `/:/resources/*` is 404. |

## Playback

Flow: client creates a play queue, fetches metadata, then asks
`/video/:/transcode/universal/decision`. The decision handler calls
`Playback.Prepare` (resolve + open; enforces stream limits), then
`Transcoder.Decide`, and remembers the binding under the client's `session`
id (falling back to `X-Plex-Session-Identifier`, then client id + key).

- Client-side decisions (`hasMDE=1`). Current Plex Web (and, per the
  reference PMS's answers, iOS and Apple TV) send `hasMDE=1`: the app's own
  media decision engine decides. With direct play allowed, PMS answers
  `mdeDecisionCode=1000 mdeDecisionText="Direct play OK."` with the source
  Media as direct play and no general/directPlay/transcode codes, even for
  media it would transcode for that client without MDE; Ether does the same.
  Ether still runs `Transcoder.Decide` and stores the result: if the app
  then asks `start.m3u8` with `directPlay=0`, a stored direct-play decision
  is re-decided without direct play (so a needed transcode isn't a plain
  remux). With `hasMDE=1&directPlay=0` (Plex Web's stream request) the
  normal transcode answer is returned. Failures add `mdeDecisionCode=2000`
  and the message to the usual 2000/3000/4000 codes; Plex Web shows the
  first `*DecisionText` whose code is ≥ 2000 (transcode, general, then mde).
- Direct play: `generalDecisionCode=1000`, Part `decision="directplay"`,
  streams `location="direct"` (only the video and the selected audio and
  subtitle streams, like PMS). The client then fetches the Part key
  `/library/parts/{ratingKey}/{changestamp}/file.{ext}`, which goes to
  `Playback.ServeRange` (preparing a session if none exists, e.g. clients that
  skip the decision). Part and Media ids are the ratingKey; Stream ids are a
  stable 31-bit hash of (ratingKey, stream position) resolved through an
  in-memory registry.
- Remux/transcode: `1001 Direct play not available; Conversion OK.`,
  `directPlayDecisionCode=3000` with "Direct play is disabled." or the
  transcoder's reason, Media `protocol="hls" container="mpegts"`, Part without
  key/file or aspectRatio, streams `location="segments-av"` (subtitles
  `sidecar`/`burn`). Transcoded streams drop the source-only details PMS
  drops (video profile/chroma/scan type, audio layout/profile/sampling
  rate/title); the decision's video `displayTitle` is the short form
  ("1080p") with the codec in `extendedDisplayTitle`.
  `start.m3u8` then calls `Transcoder.StartHLS` with `Playback.InputURL` and
  returns a master playlist pointing at
  `session/{id}/base/index.m3u8`; session playlist and segment URLs are passed
  to the `HLSSession`. A start with a different `offset` (seek) restarts the
  session; `ping`/`stop` are honoured.
- Failure (`core.ErrNotAvailable`, `core.ErrStreamLimit`, anything else): the
  decision returns 200 with `generalDecisionCode=2000`,
  `directPlayDecisionCode=3000`, `transcodeDecisionCode=4000` and the message
  ("Not available yet: no playable release of this title was found. Try
  again later." / "Stream limit reached: ...") in all three `*DecisionText`
  fields, so whichever field a client displays carries it. Direct part
  requests map not-available to 404 and stream-limit to 503.
- Stream selection: `PUT /library/parts/{id}?audioStreamID=&subtitleStreamID=`
  is stored per user and applied to later decisions; request parameters
  `audioStreamID`, `subtitleStreamID`, `subtitles=burn|none` override. Audio
  defaults follow `User.AudioLanguages`, then the default flag.
- External subtitles (`StreamInfo.External`) get `key="/library/streams/{id}"`,
  served from `ExternalURL` (http(s) proxied, or a local path).

## Open questions for the spike

1. **DASH.** Plex Web in Chrome/Firefox asks for `protocol=dash` and
   `start.mpd` when it can't direct play. `core.HLSSession` produces HLS/MPEG-TS
   only, so `start.mpd` returns 400 today. Check whether Plex Web falls back to
   HLS, or whether `internal/transcode` needs an fMP4/DASH output. Direct play
   of MKV with H.264/HEVC + AAC/Opus works in Chromium-based Plex Web.
2. **Error surfacing.** Confirm each app shows `generalDecisionText` (or
   which field it uses) for code 2000 instead of a generic "playback error",
   and that `terminationText` on `/:/timeline` is displayed.
3. **guid scheme.** Ether uses legacy-agent guids
   (`com.plexapp.agents.themoviedb://550?lang=en`) because it has no
   `plex://` guids. Verify the new apps don't hide watchlist/"Where to watch"
   affordances in a way that breaks detail pages, and that Plex's online
   features don't misbehave for these items.
4. **Apple TV decisions.** Record the Apple TV app's real profile-extra and
   check it accepts Ether's directplay/transcode decisions. Field names,
   JSON types, child order and XML attribute order now match the recorded
   PMS decisions for all three clients (legacy, MDE direct, MDE stream;
   `TestDecisionConformsToPMS`); what remains unverified is the apps'
   behaviour on a real stream.
5. **Plex Pass gating** for remote streams against a third-party server:
   what the apps check (`myPlexSubscription`, `ownerFeatures`).
6. **plex.direct certificate** availability (handled by plextv/remote).
7. **Catalogue facets.** `firstCharacter` needs `Catalog.Facet` support for
   the alphabet jump bar; director/actor filter value lists aren't offered.
8. **Downloads/sync.** `allowSync` is reported false everywhere; the new
   `downloadQueue` API is not implemented.

## Conformance (pmsdiff)

`testdata/pms` holds redacted responses recorded from a real PMS 1.43.4 for
Plex Web, iOS and Apple TV header sets (XML and JSON): 27 requests × 6
variants. `cmd/pmsdiff` compares a live Ether against them:

```sh
go build -o /tmp/pmsdiff ./cmd/pmsdiff
pmsdiff compare --reference testdata/pms --target http://127.0.0.1:32400 \
  --token <token> --client all --summary [--values] \
  [--ignore "$(cat testdata/pms/data-gaps.regexp)"] [--only <path substring>]
pmsdiff record --server <pms> --token <token> --only /transcode/universal/decision
```

How the comparison works (`internal/pmsconform`):

- Bodies become schemas: array indices are collapsed (`/MediaContainer/Video/@slug`,
  `$/MediaContainer/Metadata[]/Image[]/url`), each path keeps its value
  types, distinct values and occurrence count, and child-element order is
  kept as "a before b" pairs. Two libraries of different titles therefore
  compare by shape.
- `missing` (PMS has the path, Ether doesn't), `extra`, `type` (a JSON
  type PMS never uses at that path; disjoint inferred types in XML, except
  free-text attributes) and status/header/parse errors are blocking.
  Descendants of a missing/extra element are folded into it (`+N nested`).
- Values that legitimately differ (titles, ids, keys, timestamps, counts,
  sizes, versions, platform, redacted values...) are compared by type only;
  keys and URLs also by shape (`/library/metadata/{id}/children`). Other
  single-valued paths and child order are reported as informational
  `value` differences (`--values`).
- `--summary` groups by endpoint across clients and formats and ends with a
  count table (distinct schema paths). `--only` narrows the corpus; a
  partial `record` keeps the saved discovery IDs and manifest notes.
  `--ignore` drops paths matching a regexp; `testdata/pms/data-gaps.regexp`
  lists data Ether doesn't have (Common Sense Media ages, critic ratings,
  extras/chapters, producers/labels/similar, theme music, BIF indexes,
  scanner stream details, watch state, Live TV provider, public address).
- Older fixtures redacted `"multiuser":true` into invalid JSON; they are
  repaired on load, and `Redact` now keeps JSON valid.
- The iOS/Apple TV profiles now send the platform values the apps send
  (`iOS`, `tvOS`); PMS answered the old `iPhone`/`Apple TV` platforms' decision
  probes with 400. Those fixtures were re-recorded, and `hasMDE=1` decision
  probes (direct play and `directPlay=0`) plus searches with results were
  added.

Beyond the live diff, `internal/plex` renders the recorded titles
("The 'Burbs", "9-1-1" S01E01) with the same media and diffs them against
the fixtures in tests (`TestDecisionConformsToPMS`,
`TestDetailConformsToPMS`, `TestTVConformsToPMS`): no structural, type,
child-order or XML attribute-order differences are allowed beyond the data
gaps.

### Before / after (October 2026)

Dev instance with the real TMDB catalogue, same fixtures and pmsdiff build,
before = `main` at the start of this pass, after = this branch. Counts are
distinct schema paths summed over XML and JSON (all three clients agree).
The dev instance cannot resolve releases, so its decisions are the
"not available" answer: their remaining `missing` is the `Media` element
and the `extra` is the error codes.

All differences:

| Endpoint | Before: err / missing / extra / type | After: err / missing / extra / type |
| --- | --- | --- |
| `/` | 0 / 0 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/:/prefs` | 0 / 0 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/hubs` | 0 / 106 / 3 / 0 | 0 / 125 / 1 / 0 |
| `/hubs/continueWatching` | 0 / 72 / 0 / 0 | 0 / 94 / 0 / 0 |
| `/hubs/promoted` | 0 / 106 / 2 / 0 | 0 / 125 / 0 / 0 |
| `/hubs/search?query=` | 4 / 2 / 2 / 0 | 0 / 0 / 0 / 0 |
| `/hubs/search?query=burbs…` | 0 / 24 / 5 / 0 | 0 / 18 / 5 / 0 |
| `/hubs/sections/{{section}}` | 0 / 44 / 0 / 0 | 0 / 38 / 0 / 0 |
| `/identity` | 0 / 0 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/library/collections/{{collectionKey}}` | 0 / 12 / 2 / 0 | 0 / 10 / 2 / 0 |
| `/library/metadata/{{episodeKey}}` | 0 / 44 / 14 / 0 | 0 / 40 / 8 / 0 |
| `/library/metadata/{{movieKey}}?includeExtras…` | 0 / 100 / 14 / 0 | 0 / 86 / 6 / 0 |
| `/library/metadata/{{seasonKey}}/children` | 0 / 32 / 8 / 0 | 0 / 24 / 4 / 0 |
| `/library/metadata/{{showKey}}/children` | 0 / 14 / 14 / 0 | 0 / 6 / 0 / 0 |
| `/library/onDeck` | 0 / 76 / 0 / 0 | 0 / 96 / 0 / 0 |
| `/library/search?query=burbs…` | 0 / 24 / 9 / 0 | 0 / 18 / 5 / 0 |
| `/library/sections` | 0 / 0 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/library/sections/{{movieSection}}/prefs` | 0 / 2 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/library/sections/{{section}}/all` | 0 / 42 / 2 / 0 | 0 / 36 / 2 / 0 |
| `/library/sections/{{showSection}}/prefs` | 0 / 2 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/media/providers` | 0 / 12 / 0 / 0 | 0 / 12 / 0 / 0 |
| `/myplex/account` | 0 / 6 / 0 / 0 | 0 / 6 / 0 / 0 |
| `/playlists/{{playlistKey}}` | 0 / 2 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/status/sessions` | 0 / 0 / 0 / 0 | 0 / 0 / 0 / 0 |
| decision `hasMDE=1&directPlay=0` | 0 / 66 / 0 / 1 | 0 / 22 / 6 / 0 |
| decision `hasMDE=1&directPlay=1` | 0 / 70 / 12 / 1 | 0 / 22 / 14 / 0 |
| decision (legacy) | 0 / 66 / 0 / 1 | 0 / 22 / 2 / 0 |
| **total** | 4 / 924 / 87 / 3 | 0 / 800 / 55 / 0 |

The raw counts understate the change: before, whole elements were missing
and counted once (`Hub/Video/Image +3 nested`, `Media/Part/Stream +40
nested`); now those elements exist and the remaining gaps are individual
attributes PMS only has data for (e.g. the On Deck and hub rows went up
because detailed episodes now carry `Stream`, whose scanner attributes
Ether can't know).

Excluding `testdata/pms/data-gaps.regexp`:

| Endpoint | Before: err / missing / extra / type | After: err / missing / extra / type |
| --- | --- | --- |
| `/hubs` | 0 / 60 / 2 / 0 | 0 / 41 / 0 / 0 |
| `/hubs/continueWatching` | 0 / 40 / 0 / 0 | 0 / 28 / 0 / 0 |
| `/hubs/promoted` | 0 / 60 / 2 / 0 | 0 / 41 / 0 / 0 |
| `/hubs/search?query=` | 4 / 2 / 2 / 0 | 0 / 0 / 0 / 0 |
| `/hubs/search?query=burbs…` | 0 / 8 / 5 / 0 | 0 / 2 / 5 / 0 |
| `/hubs/sections/{{section}}` | 0 / 10 / 0 / 0 | 0 / 4 / 0 / 0 |
| `/library/collections/{{collectionKey}}` | 0 / 2 / 2 / 0 | 0 / 0 / 2 / 0 |
| `/library/metadata/{{episodeKey}}` | 0 / 14 / 4 / 0 | 0 / 10 / 0 / 0 |
| `/library/metadata/{{movieKey}}?includeExtras…` | 0 / 14 / 10 / 0 | 0 / 0 / 2 / 0 |
| `/library/metadata/{{seasonKey}}/children` | 0 / 20 / 4 / 0 | 0 / 10 / 0 / 0 |
| `/library/metadata/{{showKey}}/children` | 0 / 10 / 12 / 0 | 0 / 2 / 0 / 0 |
| `/library/onDeck` | 0 / 44 / 0 / 0 | 0 / 28 / 0 / 0 |
| `/library/search?query=burbs…` | 0 / 8 / 9 / 0 | 0 / 2 / 5 / 0 |
| `/library/sections/{{movieSection}}/prefs` | 0 / 2 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/library/sections/{{section}}/all` | 0 / 6 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/library/sections/{{showSection}}/prefs` | 0 / 2 / 0 / 0 | 0 / 0 / 0 / 0 |
| `/playlists/{{playlistKey}}` | 0 / 2 / 0 / 0 | 0 / 0 / 0 / 0 |
| decision `hasMDE=1&directPlay=0` | 0 / 46 / 0 / 1 | 0 / 2 / 6 / 0 |
| decision `hasMDE=1&directPlay=1` | 0 / 50 / 12 / 1 | 0 / 2 / 14 / 0 |
| decision (legacy) | 0 / 46 / 0 / 1 | 0 / 2 / 2 / 0 |
| **total** (all endpoints) | 4 / 446 / 64 / 3 | 0 / 174 / 36 / 0 |

(`/`, `/:/prefs`, `/identity`, `/library/sections`, `/media/providers`,
`/myplex/account` and `/status/sessions` are 0 / 0 / 0 / 0 in both.)

What remains, all data-dependent: the dev catalogue's episodes have no
crew/ratings (hub/On Deck episodes lack Role ids, Rating), no season items
in hubs, `/library/search` returning shows the
reference library doesn't have, a collection `art` PMS's collection lacks,
seasons without per-season UltraBlur (PMS computes it from season art),
TMDB writer photos PMS lacks. Values that differ by design (`/` and
`/media/providers` capabilities: no sync, camera upload, Live TV, channels,
media deletion, updater, voice search) are honest and informational.
