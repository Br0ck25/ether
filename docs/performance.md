# Live time-to-playable benchmark

Measured 2026-10-19 against NZBgeek and the owner's configured Usenet providers
with `cmd/ether-livebench`. Credentials were read via `-ref` and were not
printed or stored in this repository. The benchmark directory was persisted
between the two runs with `-data`; NZB downloads/search results and stream
segments were consequently warm on the second run.

| Run | Resolve | Open | First 4 MiB | Mid-file 4 MiB seek | Probe | Total to playable |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Supplied baseline (The Matrix, Plex Web) | 10.4 s | 0 s | 0.49 s | 0.30 s | 32 ms | ~10.9 s |
| First run after parallel candidate validation (cold data) | 8.38 s | 0 s | 537 ms | 324 ms | 26 ms | 8.94 s |
| Persistent-data rerun (warm data) | 5.368 s | 0 s | 3 ms | 2 ms | 38 ms | 5.41 s |

The resolver's persistent result cache reduced the repeated search to 1 ms.
On the warm-data run, the selected candidate's NZB fetch took 2 ms and its
health check took 5.365 s; that health check includes the archive header read
and sampled article STATs. The first cold run showed 16 dials against the
primary provider, and provider telemetry showed command latency around
176 ms, so cold TLS/auth connection establishment and NNTP round trips remain
the main cold-start costs. The persistent rerun reached approximately the
five-second target, while the cold run did not.

`ether-livebench -trace` reports total resolution time, resolver search time,
each validated candidate's NZB fetch and health-check durations, and the
separate open/read/seek/probe phases. `-data` keeps the database, cached NZBs,
and stream cache between invocations so repeat measurements do not spend more
indexer API hits or NZB grabs.

Candidate fetches and full health checks now run concurrently for the top
three candidates. Winner selection still follows score order, and each
candidate still passes the archive and article-availability checks.
