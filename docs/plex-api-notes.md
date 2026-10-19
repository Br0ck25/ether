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
| `testdata/pms` fixtures + `cmd/pmsdiff` (main) | Structural diffs of the spike server against the reference |

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
  alphabetical with `decision`, `selected`, `location` appended; in JSON
  `allowSync`, `librarySectionID`, `mediaTagVersion`, Media/Part/Stream `id`
  and `hasVoiceActivity` are strings (`"id": "221632"`, `"allowSync": "1"`).
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
| `/library/metadata/{keys}` | Comma lists supported. Detail adds all tags with `id`/`filter`, `Guid` children (imdb/tmdb/tvdb), `Rating` (TMDB audience), `Image`, streams. `includeChildren`, `includeOnDeck`, `includeRelated`, `includeExtras` honoured; `checkFiles`, `asyncAugmentMetadata` etc. accepted and ignored. Collections and playlists resolve here too. |
| `/children`, `/allLeaves`, `/grandchildren`, `/parent`, `/grandparent`, `/similar`, `/related`, `/onDeck` | Show children include PMS's "All episodes" `Directory` (suppressed with `excludeAllLeaves=1`); seasons/shows get `viewedLeafCount` from watch state. |
| `/library/metadata/{key}/{thumb,art,clearLogo,...}/{ts}` | Proxied from TMDB through an in-memory LRU (128 MiB) with request coalescing. |
| `/photo/:/transcode` | `url` may be one of our artwork paths (relative or absolute to this server / localhost / plex.direct) or an absolute TMDB URL; anything else is 403 (not an open proxy). No pixel work: the closest TMDB width variant ≥ the requested width is served (`w92`…`w1280`, `original`). |
| `/hubs`, `/hubs/promoted` | Continue Watching first (`home.continue`, key `/hubs/home/continueWatching`, style `hero`), then `Recommender.HomeHubs`. `contentDirectoryID`, `excludeContinueWatching`, `identifier` honoured. |
| `/hubs/continueWatching` (+`/items`) | `hubIdentifier="continueWatching"`, `context="hub.continueWatching"`, key `/hubs/continueWatching/items`. |
| `/hubs/sections/{id}` | `movie.inprogress.{id}` / `tv.inprogress.{id}` (key `/hubs/sections/{id}/continueWatching/items`), `movie.recentlyadded.{id}` (key `/library/sections/{id}/all?sort=addedAt:desc`) / `tv.recentlyadded.{id}` (key `/hubs/home/recentlyAdded?type=2&sectionID={id}`), then `Recommender.SectionHubs`. Hubs keep `size=0` rows like PMS. |
| `/hubs/items?identifier=` | Re-fetches a generated hub with more items (count / container size). Ether hubs without a backing collection use this as their `key`. |
| `/hubs/search`, `/search`, `/library/search` | Hub search returns Movies/Episodes/Collections/Shows hubs (empty ones included) with `score` strings; `/library/search` returns `SearchResult{score, Metadata}`. |
| `/library/onDeck`, `/library/recentlyAdded`, `/hubs/home/*` | From `State.InProgress` and `Catalog.Browse(SortAdded)`. |
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

- Direct play: `generalDecisionCode=1000`, Part `decision="directplay"`,
  streams `location="direct"`. The client then fetches the Part key
  `/library/parts/{ratingKey}/{changestamp}/file.{ext}`, which goes to
  `Playback.ServeRange` (preparing a session if none exists, e.g. clients that
  skip the decision). Part and Media ids are the ratingKey; Stream ids are a
  stable 31-bit hash of (ratingKey, stream position) resolved through an
  in-memory registry.
- Remux/transcode: `1001 Direct play not available; Conversion OK.`,
  `directPlayDecisionCode=3000` with "Direct play is disabled." or the
  transcoder's reason, Media `protocol="hls" container="mpegts"`, Part without
  key/file, streams `location="segments-av"` (subtitles `sidecar`/`burn`).
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
   check it accepts Ether's directplay/transcode decisions (attribute order
   and string-typed ids now mirror PMS).
5. **Plex Pass gating** for remote streams against a third-party server:
   what the apps check (`myPlexSubscription`, `ownerFeatures`).
6. **plex.direct certificate** availability (handled by plextv/remote).
7. **Catalogue facets.** `firstCharacter` needs `Catalog.Facet` support for
   the alphabet jump bar; director/actor filter value lists aren't offered.
8. **Downloads/sync.** `allowSync` is reported false everywhere; the new
   `downloadQueue` API is not implemented.
