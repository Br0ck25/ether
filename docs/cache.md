# Cache management

Open **Server → Cache**. Storage settings apply to this node. Pick a preset,
adjust the controls, and save the policy; selecting a preset does not start a
job. Existing settings retain their previous behaviour until you change them.

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
Reads by a prefetch job never count as plays. The eviction policy compares whole
releases, so a large favourite does not lose merely because it has many segments.
The oldest segment of the least valuable release is evicted first.

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
The page always labels the active directory. Clusters fill and retain media on
each node independently; they do not pool disks or coordinate catalogue shards.
