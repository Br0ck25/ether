# Native Usenet and impaired-network validation

Run date: October 20, 2026 (Toronto). These are controlled socket experiments,
not a claim of worldwide performance leadership or Plex play-to-first-frame
measurements. The architectural comparison with AIOStreams is in
[the companion review](usenet-architecture-comparison.md).

## What was actually run

Real SABnzbd **5.1.3**, source commit
`a647f73426cb70ad60ae7964b96c2d5e939b4727`, ran with Python 3.14.4 and
SABCTools 9.6.3. Its startup log confirmed the **AVX2 decoder was enabled**.
Its required par2 and unrar binaries were unpacked into the isolated lab;
no system services or package installations were changed.

Ether used Go 1.27.2, `CGO_ENABLED=0`, on the same Linux AMD Ryzen 7 2700X
host. The native client is [cmd/usenetbench](../cmd/usenetbench). The comparison
driver is [scripts/usenet-compare.py](../scripts/usenet-compare.py).

Each download received a fresh NNTP server, fresh client/cache state, unique
output directory, and identical deterministic 64 MiB data split into 768 KiB
yEnc articles. Four connections per provider were configured for both clients.
The Fetcher harness used 16 requesting workers; the final engine uses 16
foreground and eight read-ahead workers (the production defaults). SAB repair/unpack were disabled
(`pp=0`), since this comparison measures verified delivery of an unarchived file.
Every successful run verified the entire file with SHA-256. Both completion
timers include flushing the output file to disk; hash verification is separate.

There are three Ether measurements: direct production Fetcher with pipeline
depth one; the same Fetcher with depth four; and the production stream.Engine
reading sequential 4 MiB ranges with read-ahead and the production 256 MiB
hot-buffer / 64 MiB read-ahead limits. A separate stress configuration uses
16 MiB for both limits. These
are different workloads. The streaming-engine result, not the raw Fetcher, is
the relevant comparison for Ether's range reader.

Each scenario runs three times, reversing client order on alternating repeats.
Other coordinated CPU benchmarks were paused on this host. Existing unrelated
host services stayed running; this is not an exclusive bare-metal laboratory.

## Impairments and measurement boundaries

| Scenario | Applied condition |
| --- | --- |
| Clean | Loopback TCP, no added latency or rate cap |
| Latency | 15 ms propagation delay in each direction (30 ms added RTT) |
| Constrained | Same propagation delay, up to 5 ms downstream jitter, shared 8 MiB/s downstream budget across all connections/providers |
| Truncation | Every seventh article disconnects midway through its first BODY/ARTICLE attempt |
| Stall | Every seventh article pauses for 300 ms midway through its first attempt |
| Backfill | Every seventh primary article is missing; a second provider has the same complete fixture |

The Python proxy queues bounded chunks with delivery times, so propagation delay
does not artificially serialize commands at the NNTP server. It simulates
latency, jitter and bandwidth; truncation simulates disconnection. It does not
simulate kernel-level random packet loss, TCP retransmission dynamics, TLS,
geographic providers, or Internet routing. The fixture is generated data, not
commercial media or a valid playable movie.

SAB timing begins at the add-file API call on a ready daemon and ends when the
job is completed and its output is present and flushed. It includes queue,
assembly and postprocessing overhead, with up to approximately 50 ms polling
granularity. Ether times start inside its fresh download process. These results
compare file-delivery tasks, not isolated decoder throughput. SAB's integer
download-time field is retained in raw results but is too coarse for these
short downloads. Ether's `firstPartSeconds` means the first complete decoded
article in Fetcher mode or the first completed 4 MiB read in engine mode; it is
not socket time-to-first-byte or a decoded video frame. Provider wire bytes
measure network work; engine fetch counters can include multiple callers
sharing one native-pool transfer.

Memory and CPU observations have boundaries too: Ether's `/usr/bin/time`
includes process startup and final SHA-256 verification; SAB CPU is a daemon
delta across the download, and its peak RSS includes startup. They are useful
resource observations, not interchangeable decoder-efficiency scores.

## Verified file-delivery results

Median completion seconds, three 64 MiB runs per scenario/client; lower is
better. All 72 rows in the final comparison set passed complete SHA-256
verification. Direct Fetcher/SAB results come from the 72-run full sweep; the
18 engine rows were rerun after completion-driven read-ahead refill. The prior
engine measurements remain in the JSON for comparison.

| Scenario | Ether Fetcher depth 1 | Ether Fetcher depth 4 | Ether stream engine | SABnzbd 5.1.3 |
| --- | ---: | ---: | ---: | ---: |
| Clean | 0.324 | 0.310 | 0.363 | 0.602 |
| Latency | 1.100 | 0.705 | 1.085 | 1.294 |
| Constrained | 8.578 | 8.543 | 9.066 | 8.816 |
| Truncate | 0.331 | 0.336 | 0.378 | 16.235 |
| Stall | 1.524 | 1.495 | 1.883 | 1.731 |
| Backfill | 0.314 | 0.315 | 0.382 | 1.139 |

The stream engine was faster in clean, latency, truncation, and backfill cases.
SAB was faster in the constrained and repeated-stall cases. Completion-driven
refill reduced the engine's stall median from 2.438 to 1.883 seconds (about 23%),
while its constrained median rose from 8.897 to 9.066 seconds. These are measured
tradeoffs, not a universal performance victory. Truncation's large SAB delay is
specific to this first-attempt disconnect fixture and its retry behavior.

The decoded file SHA-256 was
`c28ffaa5941bae41bc09584562e708d3478a47c09cf8f3e0efe119b9a46643dc`.
A clean, once-only transfer uses about 69.25 MB of NNTP wire data for 64 MiB of
payload. Some engine runs still repeat articles when queued lanes race; active
sharing reduces duplication rather than guaranteeing exactly one transfer in
all schedules. Exact per-run wire counters are preserved in the
[machine-readable results](benchmarks/usenet-network-2026-10-20.json).

Across the preceding full sweep, median peak RSS was approximately 18.1 MiB
for depth-one Fetcher, 28.4 MiB for depth-four Fetcher, 79.6 MiB for the engine,
and 138.0 MiB for SAB. Median observed CPU was approximately 0.32, 0.34, 0.36,
and 0.35 seconds respectively. These cross-scenario summaries have the different
measurement boundaries described above and are not decoder efficiency scores;
the final engine rerun's per-process resources are also retained in the JSON.

### Small-cache stress result

The final constrained-link engine run with a 16 MiB hot buffer, configured
16 MiB read-ahead maximum, and 16 read-ahead workers passed all three full-file
hashes. Median completion was **9.056 s**; hot-cache usage stayed at the
16 MiB limit. Wire usage was 72.50–76.55 MB, showing that bounded memory and
correct output do not imply zero repeated network work.

## Severe-link buffering limitation

An initial 8 MiB, 1 Mb/s, 120 ms RTT pilot retained the main proxy's 16 × 64 KiB
queue per connection. At this rate, one queued MiB reserves more than eight
seconds of future link service and can starve other connections beyond Ether's
five-second inactivity deadline. **Ether failed that pilot; SAB timed out and
retried but completed with a correct hash in 82.63 seconds.** This is retained
as a bufferbloat/timeout-policy limitation, not silently counted as a pass.

The intended low-buffer severe-link scenario uses 4 KiB chunks, keeping each
16-chunk queue to 64 KiB. A two-client 2 MiB setup check then passed at
17.52/17.77 seconds. The final 8 MiB result was **75.988 s for Ether and 69.677 s for SAB**,
both fully hash-verified, with one sample each. This is not a three-run median.
Smaller buffering changes the network model; it does not prove Ether can recover
from the original oversized-queue condition.

## Corrections driven by the experiment

The initial four-deep Fetcher failed two of three truncation runs. Unread replies
behind other damaged articles consumed retries before their own response was
received. The fix replays such tails alone and preserves healthy idle sockets.
A bounded two-second recovery budget handles non-timeout breaker cooldowns only
after all providers have been tried. Healthy backup remains immediate; existing
inactivity-stall protection stays intact.

Another iteration exposed a priority-versus-bandwidth tradeoff: immediately
duplicating every live request that met background work increased wire traffic
by about 49% in the constrained case. The final reader briefly waits for a
background flight's decoded bytes, without waiting for disk persistence, then
uses an independent live lane if needed. Same-priority requests coalesce;
different lanes remain bounded. The native pool additionally shares an already
flushed BODY transfer across priorities, while queued work, socket acquisition,
and GROUP negotiation remain bypassable. A stranded shared tail replays at live
priority. The 250 ms hedge bounds waiting on queued speculation, not the
completion time of an active transfer: sharing an already-flushed BODY uses
the existing configured network deadlines (default five-second inactivity and
60-second command limits). A healthy 768 KiB article itself needs over six
seconds on a 1 Mb/s link. This is a bandwidth-versus-stall-latency tradeoff, not
a promise that every live read finishes within 250 ms. This distinction avoids
treating a healthy slow transfer as merely queued work. A connection is reserved against speculative
work when the provider has at least two connections. NNTP read-ahead pipeline batches are
limited to two articles; prefetch and recovery batches remain one.

A separate cache-pressure defect was also reproduced: read-ahead plus the
foreground request could exceed a small hot buffer and evict useful bytes before
consumption. Speculation now reserves foreground and article-boundary headroom
and uses at most half the hot buffer per cursor. This does not claim global
fairness across multiple simultaneous sources. The 256/64 MiB production limits
remain unchanged for ordinary reads. Earlier pilots used 16 read-ahead workers;
the final matrix uses the production eight, so differences between those pilots
and the final run cannot be attributed solely to one code change.

Repeated 300 ms stalls also exposed a scheduling gap: finished read-ahead
workers did not refill the window until another foreground `ReadAt`. A stalled
article could therefore leave other sockets idle. A single source-local pump
now refills on successful completion within the existing cursor window and
cache budget. Tests cover cancellation, close/join, seek eviction, and failed
archive verification without autonomous retry loops. The final engine-only
rerun is compared with the unchanged Fetcher and SAB measurements from the
preceding full sweep; both sets are preserved in the machine-readable results.

## Debrid and simultaneous workloads

[network_lab_test.go](../internal/debrid/network_lab_test.go) runs the production
debrid range engine against real HTTP sockets. It verifies complete 16 MiB +
731-byte downloads after truncated bodies, body stalls, header timeouts, 503s,
and 429 recovery. The 429 test advances the policy clock by 61 seconds; its
reported duration does not include waiting that minute in wall-clock time.

The mixed case concurrently downloads through the native Usenet engine and
debrid engine while reading the same bytes from a native file. Both network
paths share an 8 MiB/s receiver budget. This is application receive shaping,
distinct from the TCP proxy above. Native-file data is in the OS cache; it is
not a cold-disk measurement. All three outputs must match the same SHA-256.
SABnzbd is not a debrid client, so no SAB-versus-debrid equivalence is claimed.

The final mixed run completed in **2.695 s for native Usenet**, **4.764 s for
debrid**, and **0.011 s for the warm native file**, each verifying all
16,777,947 bytes against
`b48819fb2d49ad5198a07e57d92e665148dc51661ce584cd9e26daa9301393b8`.
These are simultaneous single-run observations, not latency percentiles. All
truncation, body-stall, header-timeout, 503, and clock-advanced 429 tests passed.

Run it with:

```sh
ETHER_NETWORK_LAB=1 GOMAXPROCS=2 CGO_ENABLED=0 go test -p 2 ./internal/debrid -run TestNetworkLab -count=1 -v
```

## Component evidence

A separate controlled one-connection microbenchmark with 32 queued 64 KiB
articles and 10 ms simulated RTT improved from approximately 355 ms at depth
one to 90 ms at depth four (3.93×). With zero added RTT, depth four had roughly
16% overhead. This demonstrates latency hiding, not a universal throughput gain.

On the Apple M4 Pro, paired full-article decoding including CRC improved by
1.09–1.35× over Ether's prior decoder across 384/768 KiB articles and 128/256-byte
lines. Background builds caused timing noise. Escape-free line microbenchmarks
can regress 15–20%; no universal decoder speedup is claimed. Approximately
2.08 million differential/boundedness fuzz executions and focused race tests
passed. The implementation remains portable native Go with word-at-a-time SWAR,
not architecture-specific vector SIMD or JavaScript payload decoding.

The 4,096-volume `SplitExtents` helper improved from approximately 3.4–3.9 ms
to 0.32–0.38 ms, with 14 allocations reduced to two. The production stream
layout already had binary search; its change removes repeated searches across
an extent and adds bounds checks. The helper's roughly 10× improvement is not
an end-to-end playback claim.

## Reproduce the NNTP comparison

Install the pinned SABnzbd release and its requirements in a separate environment,
with its required par2/unrar tools available on PATH. Then:

```sh
CGO_ENABLED=0 go build -o /tmp/usenetbench ./cmd/usenetbench
python3 scripts/usenet-compare.py \
  --ether /tmp/usenetbench --sab /path/to/sabnzbd \
  --sab-python /path/to/venv/bin/python \
  --output /path/to/new-results --size-mib 64 --connections 4 --repeats 3
```

The runner starts and joins its own fixture servers, proxies and SAB processes,
uses loopback endpoints, records failures instead of hiding them, and exits
nonzero if any download fails. Its per-run directories retain exact manifests,
NZBs, logs and measurements. A failed setup pilot with missing SAB dependencies
and an early runner bug that treated temporary `Queued` history as completion
are excluded from the final measurements.

## Repository validation and provenance

The final combined source includes the resolver, NAS, and Metadata integrations
through base `ce9db73f67913513cdc3aa0d12f5fd480f85581a`. Before committing,
`CGO_ENABLED=0` full-repository build, vet, and test gates passed with Go 1.27.2
and installed FFmpeg/ffprobe 8.1.3-Jellyfin on PATH. Final stream and Usenet race
suites passed, including ten repeated active-sharing/deadline regressions.
Focused playback, archive, and decoder tests and decoder fuzzing also passed
during implementation. No core/config contracts were renamed or removed.

The final Linux benchmark and committed native production Go files have matching
SHA-256 fingerprint
`e4e05e7992f04baded4e98a9344385e27f97a167705d1fdfca42e78a661750e4`;
the exact fingerprint procedure and individual observations are in the JSON.
The runner joined its fixture, proxy, and SAB processes; a final owned-process
inventory was empty. No real provider accounts or media were used in this lab.
