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

Personal rows also draw on public crowd knowledge with no extra configuration:
TMDB's co-engagement recommendations aggregated over each viewer's favourites
(titles several favourites' fans agree on rank highest), trend momentum from
TMDB trending and, when Trakt is set up, Trakt's public trending, most-watched
and anticipated lists, and co-watching among the server's own viewers. These
lists are cached for days and fetched within a per-build budget.

Detail pages can offer a version picker of distinct candidate releases (the
automatic choice first, then quality tiers and editions such as an extended or
director's cut) and a per-viewer decision header combining match, the strongest
taste reason and the local finish time. Post-play suggestions rank by taste fit,
similarity to what just finished and runtime fit for the hour. Titles a viewer
opens without playing briefly lift related genres, decades and people on Home and
section rows; the signal is in memory only and fades over about an hour.

Search also understands descriptive queries such as decades, genres, runtimes,
ratings, languages, people and studios. It returns matching titles together with
a generated collection the viewer can open as a grid; rating that collection saves
it as a personal collection. Generated collections are viewer-owned, in memory and
expire. Plain title searches behave as before.

When the owner enables extras, trailers appear as extras, and before a feature as
Cinema Trailers, only when an owner-configured resolver can play their TMDB
trailer videos. While a trailer plays, the
feature is warmed so it can start without a resolution delay. Preview thumbnails for scrubbing are generated progressively
during playback. Permitted viewers can download titles for offline viewing; the
Download button also depends on plex.tv entitlements that Ether does not control.

## Owner experience

The browser UI groups related controls into tabs and subtabs. Lists and previews
are the main page content. Create and edit forms open in drawers; advanced
controls remain available near the relevant feature. Pages scroll vertically,
use consistent content margins, and show recent log entries first.

Home Designer stores one canonical layout document with global pages and viewer
overrides. Row policies support eligibility, required rows, hidden rows, positions,
priorities, schedules, and page limits. Previewing a draft does not change the
saved layout.

Owners configure quality profiles, audio and subtitle preferences, local folders,
providers, indexers, caching, sharing restrictions and integrations. Changes to
TMDB credentials, locale and model settings apply to new requests without a
restart. Secrets are redacted in ordinary configuration responses.

Indexer setup requires a URL and API key, not knowledge of an account's quotas.
Ether follows service limit responses, honors Retry-After when supplied, and
otherwise pauses briefly before allowing another attempt. Pauses survive server
restarts and do not prevent cached playback or use of another indexer. Daily
usage is still recorded. Optional personal caps live under Advanced; an unset
cap does not imply that the upstream account has an unlimited allowance.

Cache management spans no persistent media storage, small rolling caches,
adaptive retention of replayed titles, and multi-terabyte managed shelves.
Detail-page opening windows and background warming are configurable. Opt-in
complete-movie and episode preloading has an allocation inside the total cache budget,
a title limit, bandwidth cap, free-space reserve, and persistent pause. It
uses the local released catalogue and owner quality/source rules, verifies
retention of each fetched span, and never writes watch history. Preloads stay
pinned within their allocation; higher-ranked candidates can replace
lower-ranked reservations. Cached sources may still require provider link
renewal and are not an independent offline library. See `docs/cache.md`.

Managed cache selection blends normalized catalogue popularity with decaying,
per-viewer-capped household watch frequency and weaker genre affinity. Household
membership defaults to active Plex Home users and the owner. TV preloading keeps
a bounded rolling window around household viewing positions, interleaving viewers
and admitting only aired regular episodes. Owners can tune the blend, household
scope and episode window, and inspect aggregate selection explanations.

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
owner-controlled. The owner can search a title's releases ranked as playback
ranks them, pin a release to a movie or episode (tried first, overriding
profile policy but not codec or failover limits, and kept with its failing
state when it breaks), block and unblock releases, and review a bounded
persistent history of resolution decisions. Owner blocks never expire. Source selection must distinguish unavailable articles or
invalid media from a temporary provider outage. Timeouts, network errors and
missing configuration do not permanently blacklist releases or hide titles for a
long cooling-off period.

Primary NNTP pools keep an owner-configurable warm connection floor (two per
non-backfill provider by default). The floor is restored in the background, with
backoff and respect for the circuit breaker, when connections are dropped. Idle
connections receive keepalive commands; excess connections expire. Reads have a short inactivity
deadline in addition to their absolute operation deadline. Healthy read progress
renews the inactivity window. Repeated connection or command failures open the
provider circuit breaker, allowing another provider to answer promptly.

Detail-page preparation caches a resolution, media probe and keyframe index for
ten minutes per viewer, client and title. Play reuses it when the release is still
acceptable for the quality profile and capabilities of the actual Play request.
A trailer before a feature, or the end of a film, may warm the next title under a
separate bounded budget at background priority. Warm-ups never reserve a stream
or write history. Health checks inspect archive headers, read the yEnc headers of
mid-file article bodies as well as testing article existence, and stop as soon as
one sampled article is confirmed missing on every provider.

PAR2 is never used for repair while streaming: a recovery block combines every
slice of the set, so rebuilding one article means reading the whole file. Only a
release's PAR2 index file (at most 1 MiB, read in parallel with the first article
fetches at the same priority and kept in the segment caches) is used. For
obfuscated posts, the MD5 of each file's first 16 KiB, taken from its first
article, finds the file description carrying its real name and exact length (a
unique exact length is the fallback); real names classify files and order archive
volumes, and when one obfuscated file dominates the post and is named a video, the
other files are not probed at all. Health checks verify the first article of the
main video, or of the first archive volume, against the recorded length,
first-16 KiB MD5 and any slice checksums it covers; a mismatch marks the release
unhealthy as wrong or damaged content, and Play rejects it too. A missing,
oversize, unparseable or foreign PAR2 never fails or delays a release; the engine
falls back to probing articles.

Source validation continues past missing top-ranked releases, within a bounded
candidate count and time budget. Confirmed source failures are skipped briefly
on retries, without permanently banning the release. An exhausted validation
budget does not establish that untried releases are unavailable. Within a health
sample, a confirmed missing article takes precedence over another sample's
transient error; a timeout alone never proves absence.
Recently verified working releases are preferred within the viewer's and
client's permitted profile, including after a restart.

The media decision chooses direct play, copy-only remux, or transcoding. HLS and
DASH use output containers appropriate to the selected codecs. Apple HLS includes
codec and dynamic-range declarations, Dolby Vision signaling where applicable,
and valid initialization-segment URLs. A client that explicitly requests segmented
Matroska receives independent Matroska fragments; direct-play support for MKV
alone does not enable that transport. Hardware encoding is used when available;
software encoding remains a fallback.

HLS and DASH sessions advertise lower renditions sharing one segment timeline;
a lower rendition is encoded only when the client switches to it. Copy sessions
may open with short, quickly decoded segments and continue as stream copy after a
discontinuity. Bitrate-capped transcodes use constant-quality encoding bounded by
a VBV buffer sized from the delivery goodput measured when an encoder run
starts (at the opening, a seek, a rendition switch or a failover); a running
encoder is not re-steered. An optional governor can clamp
upcoming copy segments whose bitrate exceeds the client's measured goodput.

Offline downloads use the Plex download queue. A compact, mobile-compatible SDR
release of at most 1080p is stream-copied to a temporary faststart MP4, served
with byte ranges while the viewer may still see the title, and deleted when it is
released or expires. Codecs that cannot be copied fail the download decision;
there is no offline transcoding.

Missing or corrupt articles, stalled reads, and premature source termination can
trigger bounded failover attempts. HLS/DASH restart the input at the mapped media
time, giving the player new container headers. Non-spliceable direct-play media
such as MKV and MP4 prefer replacement candidates of the original file size;
size equality is a conservative filter and does not establish byte identity.
Arbitrary different encodes are not spliced into an existing byte-range response:
without a same-size candidate, the session switches release and the client's next
request is served as copy-only remux from the mapped time. Copy-only streaming is
preferred for supported on-demand clients. A source fetched slower than real time
during HLS or DASH playback can fail over to a lower-bitrate acceptable candidate;
the slow release is excluded for that session but not marked bad.

MP4 sync-sample tables, fragmented-MP4 segment indexes and Matroska cues provide
bounded keyframe time indexes when available. They are read in the background
after playback starts and kept with resume records. Without a usable index,
byte/time mapping is approximate. When subtitle timing is available for both
releases, failover and intro/credits markers align timestamps by dialogue,
accounting for offsets, PAL speed-up and inserted or removed scenes; otherwise
duration-based alignment applies. Credits markers end before a detected mid- or
post-credits scene. Different cuts without usable subtitle timing can still
produce a timestamp discontinuity.

## Sources, history and live television

Torrent releases from Torznab indexers and built-in torrent sites stream
through owner-configured debrid services over HTTPS range requests; Ether never
joins a swarm. Only torrents a service already holds are used. Ordinary streaming
fetches requested ranges; owners can separately opt into full-source movie and episode
preloading in Cache management. "Not cached" skips a release without marking it bad.
Torrents a service holds stay on the owner's account and are reused, and
detail-page preparation obtains the link and reads the file's head and tail, so
Play needs no provider request.

Indexers are Newznab or Torznab APIs, or built-in sites. A built-in site runs
from an owner-installed definition in the Cardigann YAML format (caps and
category mappings, settings, sign-in, search requests over HTML, JSON or XML
responses, row and field selectors, filters and download steps). Ether
bundles no definitions or definitions source; the owner uploads definitions
or names a source to install them from, and definitions are stored in the
database. Built-in sites share the Newznab indexers' daily budgets, result
cache, events, search order and download routing. Sign-in sessions are stored
per indexer in the database, versioned, and established by one sign-in at a
time, so a node reuses a session another node created. A site behind a
browser challenge is reached through an owner-configured FlareSolverr, or
reports that it needs one. Password-type settings are sealed and redacted
like API keys.

Source routing never trades quality for speed. Among candidates in the leading
quality tier whose scores are within a margin of the leader, the source with the
lower measured startup goes first: NZB or payload fetch plus a health check that
actually ran, averaged per source. Until both sources have enough samples,
popular titles prefer torrents known to be cached; an owner may instead always
prefer one source. The best candidate of the other source is validated alongside
and both remain failover alternates.

Ether does not index local media; owners who keep their own files run a Plex
Media Server alongside it. Owners may supply M3U playlists and XMLTV guides or assemble virtual channels.
Source URLs, channel access, availability and account entitlements remain the
owner's responsibility. A successful tune response alone does not establish that
a particular physical client has played the stream.

Plex and CSV imports are reviewed before application, preserve viewer identity,
and deduplicate detailed history. State, lists and import bookkeeping commit
atomically. After commit, ordinary state events update recommendations and
optional Trakt history synchronization. Imports do not simulate active playback
sessions. Repeating a completed import does not duplicate history events.

## Clustering and high availability

A standalone server stores its state in SQLite and needs nothing else. An owner
may instead run two or more Ether nodes on one network against an
owner-supplied PostgreSQL database. Ether ships no database and does not manage
PostgreSQL availability; a highly available PostgreSQL deployment is the owner's
responsibility.

The owner starts or joins a cluster through the Server Manager wizard. Ether
tests the connection without changing the database or consuming a token, takes
a portable backup, stops its writers, applies the migration or join and
restarts automatically. A configured server requires explicit confirmation
before adopting another cluster’s identity. Failed application restores the
previous local settings and credential key. No terminal commands are required.
The owner can adjust node weights and pause or resume admissions in the UI.

Every node shares one server identity: machine identifier, plex.tv
registration, plex.direct certificate and credential key. Plex apps see a
single server whose published connections are the union of the healthy nodes'
addresses. A node joins with a one-time token issued from an existing node; the
credential key travels in that token and is never stored in the database in the
clear. Node-specific settings such as bind address, port, cache location and
size, and hardware transcoding stay in that node's local configuration; all
other settings are shared.

PostgreSQL is the single source of shared state. Any node serves any request,
and every node streams, caches segments and transcodes locally. Shared state
includes accounts, tokens, watch state and history, playlists, play queues,
collections, policies, settings, live TV configuration, the indexer
result cache and active playback sessions. Segment caches, transcode output and
local scan state remain on the node. Writes that check and then modify shared
state are atomic across nodes; an in-process lock never guards a shared
invariant.

At most one node at a time holds leadership, through a PostgreSQL session lock.
Only the leader runs singleton work: plex.tv publication and certificate
renewal, catalogue synchronization, collection refresh, Trakt and watchlist
synchronization, scheduled backups and newsletters. Leadership passes to another
node within seconds of the holder losing its database session; the new leader
continues periodic work from its last recorded run. Queued work such
as notifications and imports is claimed with row locks and delivered once. A
node never closes or alters another live node's sessions at startup.

Nodes publish heartbeats with their addresses, capabilities and load. Session
lists, stream limits and termination apply across the cluster. Cache
invalidations, timeline updates and termination requests reach every node
promptly. Usenet connection limits, debrid provider concurrency and API
budgets, and indexer and metadata request budgets are account-wide, so the
cluster divides them among live nodes, in proportion to an optional per-node
weight, and redistributes a departed node's share. A rate-limit or
authentication back-off observed by one node applies to all. Adding a torrent to
a debrid account is claimed by one node, and a torrent is removed from the
account only when no node is streaming from it. Owners are warned when nodes
reach a provider from different public addresses. A node whose identity is in
use by another running node, such as a cloned machine, refuses to join. Each
node maps its own port forward. Every backup is recorded with the node that
holds it, and the owner may choose a backup directory that all nodes reach.

When a node fails, clients reconnect through another published connection.
Playback resumes from the last reported position; an interrupted response is
not spliced. Brief database outages do not end active streams; progress reports
are retained in memory and written once the database returns, and the owner's
status shows the database as unreachable meanwhile. A node that cannot reach
the database at startup waits for it. Nodes update one at a time under a
cluster-wide update lease: the updating node first drains — it leaves the
published connections, hands over leadership and admits no new sessions — and
installs once its own sessions end or a configurable drain time passes; the
next node follows after the new version has confirmed its health, and a release
that rolled back on one node is skipped by the others. A node joins the rotation
only after its schema migrations, applied under a cluster-wide lock, are
compatible with the running cluster: migrations are additive, so nodes still on
the previous release keep working, and an owner may instead have a node wait
until older nodes have updated.

## Component contracts and delivery

Ether runs as one Go process with an embedded browser UI and SQLite storage, or
PostgreSQL storage in a cluster.
Domain interfaces are defined in `internal/core`. Components own their schemas,
migrations and query APIs; orchestration wires dependencies without querying
another component's tables. Catalogue readers support batched local snapshots
and hydrated title reads without triggering remote requests.

The backend supports pure Go builds with `CGO_ENABLED=0`. Package tests run
offline using local fixtures and fake services; the same database tests can also
run against an owner-provided PostgreSQL instance when one is configured. Owners supply their own TMDB
API key in configuration (`catalog.tmdbKey`); an optional build-time fallback
symbol (`tmdb.BundledKey`) exists for private builds and is verified by a
linker smoke test, while public releases ship with no embedded third-party API
keys.

Packaging workflows cover macOS, Windows and Linux. Installer availability is
reported separately from packaging support; an unpublished build is not an
available download. The repository includes this specification, installation
instructions and the user guide. Private research and planning notes are excluded.
