# Repair Ahead

Repair Ahead is opt-in PAR2 source recovery, implemented entirely in Go. Enable
it in Sources → Usenet → Repair Ahead. It repairs missing/corrupt posted bytes;
the NZB remains an unchanged manifest. Existing provider backfill runs first.

## Recovery and publication

The metadata parser retains Main-packet protected-file order and slice hashes.
The engine maps those files to the NZB, refuses ambiguous/incomplete layouts,
and scans recovery volumes incrementally. A missing parity article invalidates
only packets crossing its gap. Packet MD5, recovery-set identity, source-slice
MD5 and CRC32 are checked. No target bytes reach the ordinary segment cache
until every reconstructed target slice verifies.

The source path has two optimizations:

* **Selective interval recovery.** PAR2 equations combine the same column in
  every source slice. Recover only the damaged intervals, rounded to two-byte
  GF16 symbols, then verify complete target slices. This saves logical input
  when slices are much larger than damaged articles. NNTP still transfers whole
  articles; the planner switches to whole slices when article size makes small
  intervals wasteful.
* **Retained parity summaries.** Fully verified consumed slices contribute once
  to a bounded set of recovery equations. Coverage is explicit. Later repairs
  subtract those summaries instead of rereading covered bytes after cache
  eviction. Missing observations reduce efficiency, not correctness; summaries
  cannot be used with mismatched recovery equations. Adaptive mode (the default)
  starts at most two rows only after actual damage, using parity already fetched
  for that repair. Healthy playback fetches no extra parity for summaries. It
  requires meaningful verified coverage before using them and stops collecting
  if observed processing cost exceeds its five-percent transfer-time allowance.

Whole-slice solves also retain incidental reconstructed small slices, within a
32 MiB allowance per initialized release. Reaching another hole can reuse the
verified result. Ordinary disk caching avoids fetching healthy dependencies
again. These are complementary to the summaries, which work after raw bytes
have been evicted.

## Bounds and playback behavior

One reconstruction runs per engine. Defaults allow two seconds for a live
reader, 90 seconds for readahead, and 512 MiB of extra input per attempt.
Already in-flight requests can overshoot the byte budget. Repair uses a separate
bulk reader, independent of the playback cursor. It
pipelines parity in 16-article windows and queues up to 64 source-slice reads
within a 16 MiB logical pipeline allowance. Half-second bursts, separated by a
half-second ordinary interval during a repair, may borrow the idle live-reserved
connection and use the configured BODY pipeline depth. This never exceeds owner
connection caps. Live waiters win the next free socket; commands already sent
must finish or hit their existing timeout. Burst admission ends at its deadline,
not by aborting a healthy transmitted article. Ordinary speculative behavior is
unchanged outside explicit bursts. Repair-enabled startup initially reads ahead
up to 32 MiB, still bounded by the existing source/hot-cache limits.

Limits include a 1 MiB metadata index, 16 MiB slices, 32768 source slices,
64 recovery rows, 64 MiB each for targets and parity, and 128 MiB of retained
repair state across the engine. Partial observations, cached collateral repairs
and summaries count toward retained-state reservations. Inactive release state
can be evicted. Source close and engine close cancel work. Cursor eviction or
moving past the relevant range cancels obsolete repair dependencies; moving the
foreground reader into the damaged range preserves its useful background job.

The first missing or corrupt source article reports damage before waiting for
the shared repair slot. Playback immediately prepares one alternate NZB and
warms both its opening and an approximate range near the viewer. Original
playback continues. Repair plans count uncached wire articles and forecast
remaining time from measured transfer throughput, conservatively shared across
active sources. Stalled transfers worsen the forecast. A 250 ms monitor compares
it with contiguous published HLS/DASH media and the mapped damaged offset;
recent authenticated client telemetry improves the playback position estimate.
Discontinuous segment requests invalidate older telemetry. Seeking past damage
does not force an unnecessary source switch.

Playback can expand read-ahead within the configured source/cache limits and
adjust the useful repair deadline, bounded by the owner's maximum wait. It
starts a speculative failover when repair cannot beat the buffer with a safety
margin, or recovery fails. A completed repair cancels the pending switch before
publication. Failure to prepare an alternate leaves the original source intact.
For compatible equal timelines, proactive HLS/DASH replacement starts after the
contiguous published buffer, retaining that media and the viewer position.
Different timelines retain existing alignment and compatibility checks. Direct
play retains its representation-safe failure path; there is no proactive
byte-splicing promise for unrelated releases.

Missing metadata, inadequate parity, transport errors, exhausted limits and
missed deadlines preserve the ordinary source error/failover behavior. Health
sampling admits a sampled hole only after actually reconstructing and verifying
its bytes, within a 15-second overall health-check deadline. Recovery cannot make a release
with insufficient surviving data playable, and a repair needing most of a large
post can exceed the playback buffer. This is opportunistic repair, not a general
stall-free streaming guarantee. Published server media is not proof of a
client's received buffer; a slow or disconnected player still can stall.

## Reproducing live experiments

`cmd/ether-repairbench` is explicitly invoked live tooling. Ordinary Go tests
remain offline and use deterministic data plus PAR2 fixtures generated by
par2cmdline-turbo 1.5.0 as an independent oracle.

```
go build -o /tmp/ether-repairbench ./cmd/ether-repairbench
/tmp/ether-repairbench -mode sample -dir /private/experiment \
  -titles 12 -releases 2 -seed 20261021 -max-bytes 8589934592
/tmp/ether-repairbench -mode exercise -dir /private/experiment -sample 14
python3 scripts/repair-compare.py --ether /tmp/ether-repairbench \
  --sab /path/to/SABnzbd --dir /private/experiment --sample 12
```

The sampler reads existing owner configuration without changing it, queries
random TMDB titles through configured indexers, saves actual NZBs, and tests
random article BODY requests. `sampled-healthy` means only that sampled articles
were available; it does **not** establish completeness. Samples, credentials in
isolated SAB configuration, logs and downloaded content stay outside the repo
in the caller's private experiment directory.

`exercise` injects one known missing article after fetching its original bytes,
then compares reconstructed bytes with that reference. Selective, whole-slice
and summary variants use fresh engines and an 8 MiB hot cache; the summary case
first consumes 32 MiB. Each case has a three-minute deadline. Warming costs and
repair costs are recorded separately, with actual transfer totals and logical
summary savings. Natural additional missing articles are left in place.

The full comparison starts isolated SABnzbd, submits the exact saved NZB with
repair enabled and extraction disabled, and hashes completed posted files.
Ether uses the normal article/repair path with a cold 32 MiB hot buffer and cold
1 GiB segment cache, writes output files, and fsyncs them. Both clients have the
same four-connection cap per enabled provider and priority ordering. Ether uses
its separate 16 MiB bulk-reader window, cached header lookup and short bursts;
repair still has independent time/byte limits. Equal connection caps do not
mean identical scheduling. Results include elapsed time,
bytes, individual Ether 16 MiB read delays, anonymized provider counters, SAB
history, NZB SHA-256 and matching output hashes. Both clients must finish
successfully for a comparison to pass. `--order sab-first` reverses the sequence;
`--cpu-profile /private/experiment/cpu.pprof` profiles Ether for `go tool pprof`.

The script owns and reaps its SAB process tree; it never controls an existing
SAB installation or claims another Plex server. Tests do not alter watch history
or the owner's provider/indexer configuration. Wall-clock results on a shared
machine and live providers are observations, not controlled hardware benchmarks.
