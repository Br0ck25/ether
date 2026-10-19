# Live time-to-playable benchmark

Measured 2026-10-19 against NZBgeek and the owner's configured Usenet providers
with `cmd/ether-livebench`. Credentials were read via `-ref` and were not
printed or stored in this repository. The persistent benchmark directory was
reused with `-data`. Budget counters in that directory recorded 2 indexer API
hits and 16 NZB grabs at the end of measurement; repeated runs used cached
search results and saved NZBs. No DOGnzb requests were made.

| Run | Profile / cache state | Resolve | Open | First 4 MiB | Seek | Probe | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| Supplied baseline | Plex Web, original baseline | 10.4 s | 0 s | 0.49 s | 0.30 s | 32 ms | Playable, ~10.9 s |
| First run after resolver parallelism | Plex Web, cold data | 8.38 s | 0 s | 537 ms | 324 ms | 26 ms | Playable, 8.94 s |
| Earlier persistent-data rerun | Plex Web, warm data | 5.368 s | 0 s | 3 ms | 2 ms | 38 ms | Playable, 5.41 s |
| NNTP batching, Apple TV attempt | 4K profile, cached search/NZBs | 5.32 s | — | — | — | — | No acceptable healthy candidate |
| NNTP batching, Plex Web retry | cached search/NZBs | 14.70 s | — | — | — | — | No acceptable healthy candidate |
| NNTP batching, Apple TV retry | cached search/NZBs | 42.75 s | — | — | — | — | No acceptable healthy candidate |

The earlier warm measurement met the target but predates the latest NNTP work.
The latest cached Apple TV attempt completed its two leading health checks but
rejected them; it cannot count as a successful time-to-playable measurement.
The later Plex Web attempt was slow because candidates continued to fail over.
Across the failed checks, archive-header BODY requests received NNTP `451`
responses even where sample STATs succeeded. This is a provider/content
availability failure path that correctly rejects those releases, and means
the ≤6 s cold / ≤4 s warm goals have not been demonstrated with the current
live results.

The detailed trace showed 1 ms cached search. In the slow Plex Web run the
health STAT wall time was 5.18 s for 12 spread samples; the largest provider
reported 25 STAT batches totaling 3.22 s, while the secondary provider
reported 20 batches totaling 12.38 s. Its aggregate TCP/TLS/greeting/auth
durations were 0.97/42.73/1.27/11.60 s over 29 dials, respectively (these
durations sum concurrent dials). Startup warming took 3.97 s in that run.
Provider TLS/auth setup and NNTP round trips remain important cold costs.

`ether-livebench -trace` reports per-indexer search timing, candidate NZB fetch
and health timing, NZB parse, archive plan/header time, STAT wall time/sample
count, per-provider TCP/TLS/greeting/auth totals and STAT batch count/time,
plus open/read/seek/probe phases. `-data` persists the database, NZB store and
stream cache. `-retry` bypasses a persisted unavailable marker while retaining
the search cache. `Fetcher.Warm(ctx, n)` warms up to `n` connections per
provider; the benchmark calls it before resolution and health checks also warm
the pool.

The resolver fetches and validates the top two candidates concurrently and
selects in score order. It continues to later candidates if both fail, while
preserving archive and article-availability rejection checks. STAT pipelining
uses up to four provider connections and batches unresolved IDs on subsequent
providers, preserving primary-first and backfill behavior.
