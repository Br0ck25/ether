# Cache management

Open **Server → Cache**. **Overview** is a compact storage and playback-readiness
view. **Explorer** is a searchable, filtered table of retained releases, paginated
20 at a time. **Preloading** shows active work and explains ranked selections.
Tabs have their own URLs and the overview fits the desktop viewport, following
the server Overview layout. **Edit policy** opens the settings drawer.
**Choose a cache profile** opens the preset cards, followed by a review step;
selecting a card does not change the policy or start a job. Save after reviewing
the controls. **Back to presets** is always available at the top of the policy
editor, including when editing an already saved policy.
Local storage settings apply to this node. Shared NAS policies apply to all
participating nodes; mount paths remain node-specific.

## Choose a footprint

- **Pass-through:** no new persistent media segments; useful for machines with
  almost no spare disk. Existing protected data remains until its pins end.
- **Feather / Everyday:** 256 MiB or 32 GiB, with small opening windows and
  time-limited retention.
- **Encore / Deep shelf:** 250 or 750 GiB, retaining fetched media and favouring
  repeat watches. These presets do not download entire movies automatically.
- **Cinema node / Edge library:** 2 or 20 TiB total, with 1.5 or 18 TiB dedicated
  to proactive movie and TV preloading. Saving these presets enables downloads.

All sizes are editable, including GiB/TiB entry. A zero budget means unlimited;
use Pass-through to disable persistent media. Budgets count segment payloads,
not filesystem metadata, posters, databases or transcode output. Live playback,
resume pins and owner pins can exceed the total budget. The free-space reserve
applies to managed preloading, not foreground playback.

## Retention and warm starts

Demand mode evicts the oldest unprotected segments. Adaptive mode considers
release recency and repeat playback admissions: each repeat earns the configured
recency credit, capped at eight repeats and removed after 30 days without a play.
Reads by a prefetch job never count as plays. Local eviction compares whole releases and then removes the oldest segment of
the least valuable release. Shared eviction ranks bounded batches of the oldest
unprotected segments with the same repeat-viewing credit, avoiding an unbounded
NAS scan or database transaction.

Unprotected releases expire after the configured number of inactive days; zero
means only the size budget expires them. The expiry pass runs every minute.
Owner pins survive restarts. Resume protection has independent age and budget
share limits. Unpinning returns bytes to ordinary retention.

Opening/resume warming is independently switchable. Detail pages can prepare
2–64 MiB, subject to the existing rate limits, two-job concurrency limit and
playback priority. Movie and episode details warm themselves; supported show
pages warm the On Deck episode. Next-episode windows, trigger position and
resume windows are configurable. Trailer and post-play warming also respect
the master warming switch and their own feature settings. Work already in flight
can finish its current request after a policy change. No warm job writes watch
history or reserves a playback slot. A warm start is an optimization, not a
promise that every source and device starts instantly.

## Managed local library

Full-title preloading is opt-in and has its own allocation **inside** the total
cache budget. Choose movies, TV episodes, or both. The title limit counts each
movie and individual episode, up to 5,000. Cinema node and Edge library use the
smart household blend by default; explicit popularity and newest-release modes
remain available.

### Household-aware selection

The default smart blend gives household demand 65% of the score and catalogue
popularity 35%. The slider supports 0–100% household influence. With no usable
history, selection falls back to catalogue popularity. Popularity is logarithmically
normalized separately for movies and shows so their different scales do not
force one format out.

Completed watches (including reviewed imports) contribute repeat frequency.
Start events are not counted again; current resume points contribute a smaller
signal. Recent viewing decays with a half-life of up to 30 days within the
configurable history window (90 days by default). Per-viewer influence saturates,
and daily per-title/show counts are capped, so a bulk season scrobble or one
prolific viewer cannot dominate the household. Independent viewers add support.
Genre affinity gives unseen titles a smaller discovery boost than direct viewing.
These are transparent heuristics, not predicted viewing probabilities.

By default learning uses the owner and active Plex Home members. **All active
shared viewers** is an explicit alternative. Learning reads at most 2,000 recent
history entries and 200 resume points per viewer, for up to 64 active viewers.
No history is sent to a model or external recommendation service. Normal catalogue
and source requests still use the configured services. The owner-only **Why these
titles?** panel shows the top candidates, separate catalogue and household scores,
and selection reasons without identifying individual viewers. It reflects the last
completed selection pass, not unsaved settings or guaranteed downloads.

### Rolling TV shelves

TV selection expands ranked shows into complete episode sources. Shows already
being watched begin at each viewer's in-progress episode or after their latest
completed episode. Different viewers' continuation windows are interleaved under
one per-show limit (3 episodes by default, configurable up to 24). Unstarted shows
begin with the first regular aired episode. Future episodes, unknown air dates,
and specials are excluded; seasons can be crossed in order.

As viewing advances, old episode reservations are released back to ordinary cache
retention and the window moves forward. This does not delete retained bytes.
Movie-only or TV-only changes also release reservations for the excluded type.
Show metadata is expanded only as needed, at most 500 shows and 12 season metadata
reads per show per pass; metadata requests follow the normal TMDB client limits.
A metadata failure preserves that show's existing reservations for the next pass.

One movie or episode fills at a time, using the owner's normal quality rules and configured
Usenet/debrid sources. Every 16 MiB window uses background-priority fetching and
verifies that its covering segments are retained. A reservation includes 5%
overhead, with at least 32 MiB reserved for container and neighbouring segments.
Oversized or unavailable sources are skipped. A failed durable write never marks
a title complete. Successful fills are pinned within the preload allocation and
remembered for the owner's playback. Different viewer/client quality profiles can
still select a different release.

The scheduler revisits the catalogue every 15 minutes. Higher-ranked candidates
may replace lower-ranked reservations once an appropriate source is found.
Released reservations become normal cached data; rotation does not forcibly
remove them. Lowering the allocation releases excess reservations, oldest first.
Disabling preloading releases its pins without deleting media. Pause preserves
the shelf and persists across restarts. Incomplete jobs retry from the beginning
of the source, reusing already retained segments; source payloads and credentials
are not stored in the preload manifest.

Preloading waits while viewers stream unless explicitly allowed. It observes an
average payload bandwidth cap in 16 MiB windows, and stops admitting windows
below the free-space reserve (5 GiB by default, plus two windows of headroom).
Source resolution, headers and segment boundaries add traffic beyond the cap.
It neither bypasses provider/indexer budgets nor purchases or supplies accounts.

A completed preload is a cached source, not a portable offline media file.
Container discovery, provider link renewal, another release, or lost/corrupt
segments can still require the configured source. This feature is not an
independent Plex library or a public CDN.

## Observe and tune

The page reports live retained bytes, protected bytes, per-release coverage and
play counts. Disk hit rate and served/reclaimed byte counters reset on restart
and exclude the engine's memory cache. The prefetch counters cover next-episode,
resume and post-play jobs; detail warming has its own playback budget. The
managed shelf separately reports current title, progress, reserved space,
completed sources and skipped/interrupted attempts.

Changing the cache directory requires a restart and does not move old data.
The page always labels the active directory. With local storage selected, cluster
nodes fill and retain media independently. Shared NAS storage coordinates one
pool as described below; it does not combine separate node disks.

## Shared NAS cache for a cluster

Choose **Edit policy → Where your cache lives → Shared NAS** on a PostgreSQL
cluster running a shared-cache-capable Ether release on each participating
node. The cluster shares one pool, with one budget, retention policy, preload
allocation and set of owner pins. A cluster may keep some nodes on local caches.
The Shared NAS choice is unavailable on a standalone SQLite server.

1. Mount an owner-managed NFS or SMB share through the operating system on every
   participating node. Ether uses mounted filesystem paths, not `smb://` URLs,
   and does not install mounts or store NAS credentials.
2. On the first node, choose an **empty, dedicated directory** on that mount.
   Give the Ether service account read/write access. Select the desired preset,
   budget and preload options, save, then restart this node.
3. On each other node, select Shared NAS and point to the **same directory on the
   same share**. Mount paths may differ between machines. The existing shared
   policy is offered when joining. Save and restart each node.
4. The Overview badge reads **Shared cluster pool** once active. Explorer lists
   the pool's releases and pins. Disk-hit metrics and opening-warmup activity
   describe the serving node; retained bytes and full-title preloading describe
   the shared pool.

The first node creates a pool identity marker. Later nodes must see that marker;
a different or missing mount is rejected instead of silently creating a private
cache. Mount the NAS before starting the service. A configured shared backend
whose mount cannot be verified prevents startup; a lost mount or database during
operation causes cache misses/write failures and a visible storage error, without
switching to another disk. Restore the mount/database to resume cache access.
Changing backend or path is a restart-required operation. Existing local bytes
are not migrated, merged or deleted. Shared metadata lives in PostgreSQL; do not
put Ether's SQLite database or transcode output on the shared cache folder.

Each published segment uses an immutable filename and a checksum. PostgreSQL
transactions serialize pin/accounting/eviction decisions across nodes. Live and
resume pins belong to their originating node and are renewed; abandoned pins
expire after three minutes. Owner and preload pins persist. NAS writes that are
interrupted before publication are journaled and reclaimed after 24 hours.
The share must support ordinary file creation, reads, deletion and durable flush;
its availability, permissions and network throughput remain owner-managed.

One renewable, fenced preload lease coordinates the shared shelf. Every node can
serve its retained media. A replacement worker reuses the stored manifest and
complete sources; stale workers cannot overwrite the manifest or remove the new
worker's pins. Followers check every 30 seconds and may take over after the
60-second lease expires. Successful catalogue passes remain 15 minutes apart.
Pause/resume applies to the whole pool. Preloading waits for playback on any
cluster node unless its policy explicitly permits concurrent downloads.
