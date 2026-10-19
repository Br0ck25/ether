# Ether product and architecture specification

Ether is a self-hosted media server for Plex apps. It combines a released-title
TMDB catalogue, each viewer's watch history and recommendations, optional local
media, and streaming through owner-supplied services. Ether ships no content,
provider accounts, indexer accounts, or credentials.

## Viewer experience

Official Plex apps provide browsing, search, playback, watch progress and
playlists. Item identities remain stable across metadata refreshes and source
changes. Movies, shows, seasons and episodes have distinct identities and types.
A season's episode list contains episodes in order, beginning with episode one;
a season is never a playable episode.

Catalogue and search results exclude titles with unknown or future release
dates. Movies use theatrical release dates where available. Television seasons
and episodes use their own air dates. Artwork and navigation must apply the same
release and viewer restrictions as playback.

Home and library pages offer a useful default mix of new releases, popular and
acclaimed titles, genre discovery, and personal recommendations. Imported history,
completions and ratings inform each viewer's taste independently. Continue
Watching reflects current state. Optional Trakt and model integrations supplement
the experience; unavailable services must not prevent browsing.

## Owner experience

The browser UI groups related controls into tabs and subtabs. Lists and previews
are the main page content. Create and edit forms open in drawers; advanced
controls remain available near the relevant feature. Pages scroll vertically,
use consistent content margins, and show recent log entries first.

Home Designer stores one canonical layout document with global pages and viewer
overrides. Row policies support eligibility, required rows, hidden rows, positions,
priorities, schedules, and page limits. Previewing a draft does not change the
saved layout. Previously saved rules migrate without losing viewer policies.

Owners configure quality profiles, audio and subtitle preferences, local folders,
providers, indexers, caching, sharing restrictions and integrations. Changes to
TMDB credentials, locale and model settings apply to new requests without a
restart. Secrets are redacted in ordinary configuration responses.

## Accounts and access

Plex sharing controls viewer membership. Ether resolves Plex account identities,
maintains per-viewer restrictions, and removes access after an authoritative
sharing refresh revokes membership. A previously validated personal token does
not grant perpetual access. Existing media grants also stop working when the
viewer becomes inactive.

After setup, the owner UI requires an owner credential on every connection,
including localhost. A reverse proxy's loopback address and Host header are not
credentials. Initial setup is available on the local network; owners should
complete setup before exposing a proxy to other networks.

Ordinary viewers can inspect and control only their own sessions. Playback and
watch-state notifications are scoped to that viewer; the owner can monitor server
activity. Media segment requests require either the owning viewer's Plex token or
an independent random credential scoped to the running stream. A client-chosen
session ID alone grants no access. Stream credentials do not authorize control
or administration, and are removed when the session ends.

## Playback

Playback selects releases according to viewer policy, device capabilities,
quality preferences and source health. Provider priorities and backfill remain
owner-controlled. Source selection must distinguish unavailable articles or
invalid media from a temporary provider outage. Timeouts, network errors and
missing configuration do not permanently blacklist releases or hide titles for a
long cooling-off period.

Primary NNTP pools retain a small warm connection floor. Idle connections receive
keepalive commands; excess connections expire. Reads have a short inactivity
deadline in addition to their absolute operation deadline. Healthy read progress
renews the inactivity window. Repeated connection or command failures open the
provider circuit breaker, allowing another provider to answer promptly.

Detail-page preparation caches a resolution and media probe for ten minutes,
keyed by viewer, client, title and selected quality profile. Playback rechecks
that the selected profile still matches. Health checks inspect archive headers
and sample actual article bodies as well as article existence.

The media decision chooses direct play, copy-only remux, or transcoding. HLS and
DASH use output containers appropriate to the selected codecs. Apple HLS includes
codec and dynamic-range declarations, Dolby Vision signaling where applicable,
and valid initialization-segment URLs. A client that explicitly requests segmented
Matroska receives independent Matroska fragments; direct-play support for MKV
alone does not enable that transport. Hardware encoding is used when available;
software encoding remains a fallback.

Missing or corrupt articles, stalled reads, and premature source termination can
trigger bounded failover attempts. HLS/DASH restart the input at the mapped media
time, giving the player new container headers. Non-spliceable direct-play media
such as MKV and MP4 restrict replacement candidates to the original file size;
size equality is a conservative filter and does not establish byte identity.
Arbitrary different encodes are not safe to splice into an existing byte-range
response. Copy-only streaming is preferred for supported on-demand clients.

MP4 sync-sample tables and Matroska cues provide bounded keyframe time indexes
when available. Without a usable index, byte/time mapping is approximate.
Different edits or cuts can still produce a timestamp discontinuity during
failover.

## Sources, history and live television

Local media can supplement the catalogue and takes precedence when selected.
Owners may supply M3U playlists and XMLTV guides or assemble virtual channels.
Source URLs, channel access, availability and account entitlements remain the
owner's responsibility. A successful tune response alone does not establish that
a particular physical client has played the stream.

Plex and CSV imports are reviewed before application, preserve viewer identity,
and deduplicate detailed history. State, lists and import bookkeeping commit
atomically. After commit, ordinary state events update recommendations and
optional Trakt history synchronization. Imports do not simulate active playback
sessions. Repeating a completed import does not duplicate history events.

## Component contracts and delivery

Ether runs as one Go process with an embedded browser UI and SQLite storage.
Domain interfaces are defined in `internal/core`. Components own their schemas,
migrations and query APIs; orchestration wires dependencies without querying
another component's tables. Catalogue readers support batched local snapshots
and hydrated title reads without triggering remote requests.

The backend supports pure Go builds with `CGO_ENABLED=0`. Package tests run
offline using local fixtures and fake services. Release scripts inject the
bundled TMDB key through the exported build symbol `tmdb.BundledKey`; a linker
smoke test verifies the symbol. Owner-provided credentials take precedence.

Packaging workflows cover macOS, Windows and Linux. Installer availability is
reported separately from packaging support; an unpublished build is not an
available download. The repository includes this specification, installation
instructions and the user guide. Private research and planning notes are excluded.
