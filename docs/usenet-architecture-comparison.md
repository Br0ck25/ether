# Ether and AIOStreams: native Usenet architecture

Review date: 2026-10-20. AIOStreams source is pinned to
[`01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0`](https://github.com/Viren070/AIOStreams/tree/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0).
Ether references describe this change set, including the native Usenet and hybrid
recovery changes. This is a source and correctness review, not proof of worldwide
performance leadership.

## Verdict

Ether is the better fit for serving official Plex applications with one portable
Go server. AIOStreams has broader Stremio aggregation and encrypted stored-archive
support. Both have substantial native Usenet engines; neither can be declared
universally faster or more reliable from architecture alone. AIOStreams also has
its own applications and Jellyfin compatibility, so describing it as merely an
addon wrapper would understate its scope. See its pinned [README][aio-readme].

## Capability comparison

| Area | Ether in this change | AIOStreams at the pinned revision | Assessment |
| --- | --- | --- | --- |
| Runtime | Portable native Go; no cgo dependency | TypeScript scheduling with native C++ yEnc | Both avoid a JavaScript payload decoder; no SIMD parity claim |
| NNTP throughput | Real bounded BODY pipelines, warm pools, priority admission | Long-lived provider workers and pipelined commands | Both have substantive engines; compare measured workloads, not language labels |
| Urgent playback | Reserved live capacity, bounded speculative depth, separate queued priorities | Priority classes with configurable background share | Ether favors foreground latency; AIO offers a fairness tradeoff |
| Provider failure | Missing/corrupt/transport/auth distinction, backfill, single half-open probe | Provider health, retries/backoff and backup order | Ether has tested fault regressions here; AIO's tests were not run |
| Decoded cache | Shared bounded hot LRU, persistent local/NAS cache, trusted-range metadata | Pinned arena, disk cache, shared fetches and negative misses | Neither memory model is universally superior; pressure and concurrent seeks matter |
| Stored archives | Indexed RAR4/RAR5/7z/split ranges with checked offsets | Stored archive readers including supported AES paths | AIO has the encrypted-archive capability advantage |
| Damaged sources | Bounded proven frame-payload concealment plus background alternate preparation | Hole policies and Matroska transformation | Different recovery policies; neither guarantees artifact-free playback |
| Product integration | Official Plex clients and PMS-compatible session orchestration | Stremio aggregation, its own apps and Jellyfin support | Ether fits this repository's Plex goal; AIO covers other clients |

## Execution and decoding

AIOStreams uses TypeScript for orchestration, but its article payload decoder
calls native `yencode.decodeTo`; it does **not** interpret every yEnc payload byte
in JavaScript. Its wrapper parses metadata and checks decoded ranges and optional
CRCs. The [yencode implementation](https://github.com/animetosho/node-yencode)
provides compiled C++/SIMD routines. See the pinned [decoder wrapper][aio-yenc].

Ether's [decoder](../internal/yenc/decode.go) compiles to native machine code and
uses word-at-a-time SWAR operations with escape handling. It validates declared
sizes and CRCs, bounds wire lines and preambles, and preserves `CGO_ENABLED=0`
portability. SWAR is not equivalent to architecture-specific vector SIMD. Native
Go satisfies the requirement to avoid a JavaScript per-byte hot path, but does
not establish decoder parity with yencode. Rust or WASM would introduce another
build boundary; any replacement should earn its complexity through measurements.

## Connections, scheduling, and recovery

AIOStreams' [provider workers][aio-workers] own long-lived connections, queue
priority classes, pipeline commands, send keepalives, reconnect with backoff,
throttle after connection-limit responses, and track provider health. Its
configurable priority share allows background work to progress under contention.
Its [segment fetcher][aio-fetcher] orders primary and backup providers and retries
article misses through that chain.

Ether's [pool](../internal/usenet/pool.go),
[BODY scheduler](../internal/usenet/body_pipeline.go), and
[wire pipeline](../internal/usenet/conn_pipeline.go) combine warm connections,
bounded admission, same-priority request deduplication, and real BODY commands
written before their replies arrive. Background batches remain shallow to limit
head-of-line blocking. [Priority semaphores](../internal/usenet/sem.go) favor live
reads over read-ahead and prefetch; prefetch also has a concurrency cap. Strict
priority protects viewers from queued work; active BODY sharing still follows
network inactivity/command deadlines. It can postpone background work indefinitely under
sustained load; AIOStreams makes a different fairness tradeoff.

Ether distinguishes missing articles, corruption, transient transport errors,
and authentication failures. Its [breaker](../internal/usenet/breaker.go) uses
bounded backoff and a single half-open recovery attempt; authentication has its
own recovery probing. Inactivity deadlines detect stalled transfers without
mistaking healthy progress for a stall. Individual cancellation does not cancel
another waiter sharing the same BODY request. These are concrete mechanisms,
not evidence that every provider or network failure is already covered.

## Memory, caching, and seeking

AIOStreams has an arena of pinned decoded segments, disk caching, shared fetches,
negative miss caching, and ordered delivery with backpressure. These are visible
in its [multi-provider coordinator][aio-multi] and [segment cache][aio-cache].

Ether separates provider miss caching from the stream
[hot buffer](../internal/stream/hotbuf.go) and persistent
[segment cache](../internal/segcache). Its
[file index](../internal/stream/fileindex.go) learns actual yEnc offsets, while
[plans](../internal/stream/plan.go) translate media ranges into posted volumes.
Cached metadata preserves whether byte ranges were actually verified. Synthetic
damage-concealment bytes never enter the segment cache. Neither design's tier
count proves lower memory consumption: resident memory, concurrent seeks, cache
retention, and duplicate work need measurement together.

## Archives and source validation

Both engines map stored archive contents to byte ranges instead of extracting an
entire movie first. Ether supports stored RAR4/RAR5, stored 7z, and split files;
its [archive readers](../internal/archive) reject encrypted or compressed media
members. It validates lazy volume assumptions and falls back to full inspection
when necessary. Checked offsets and indexed extent mapping address malformed
layouts and large packs.

AIOStreams has an explicit capability advantage for password-protected stored
archives: its [RAR reader][aio-rar] and [7z parser][aio-7z] support applicable AES
paths. That does not imply arbitrary compressed archives are seekable.

Ether's [health checks](../internal/stream/health.go) combine archive validation,
spread STAT samples, actual mid-file BODY header probes, and best-effort PAR2
metadata verification. Newly identified obfuscated candidates can begin sampling
while layout inspection continues. The selected candidate is rechecked after
fallthrough. A successful plan alone is not a healthy-release verdict; cold
startup still needs end-to-end measurement.

## Damage policy and quality evidence

AIOStreams has [bounded hole policies][aio-holes] and a
[Matroska hole transform][aio-transform]. Ether's
[stream recovery](../internal/stream/damage.go) is deliberately narrower:
confirmed missing articles may be concealed only inside proven, unlaced
Matroska/WebM frame payloads with trusted neighboring offsets. Headers,
CRC-protected structures, ambiguous ranges, and archive members are excluded.
[Playback recovery](../internal/playback/damage.go) starts alternate preparation
on the first missing-article or corruption notice and escalates when damage budgets are
exceeded. This reacts to detected source damage, not visual analysis of every
displayed frame, and cannot guarantee an invisible release switch.

Ether has executable offline regressions for wire pipelining, cancellation,
stalls, breaker recovery, decoder bounds, archive ranges, and damage handling.
During this review, the full Go build, vet, and test gate passed, together with
focused decoder, stream, playback, and Usenet race tests. AIOStreams' suite was not executed here; absence of observed test results
does not establish inferior quality. Comparable fault testing is required before
making a reliability ranking.

## Remaining evidence and capability gaps

This work does not establish “best in the world.” Ether still rejects encrypted
media archives; the native Go decoder has not been directly benchmarked against
AIOStreams' C++ decoder here; and some queued-priority races still cause repeated
article transfers. The impaired-network report includes a deep-buffer 1 Mb/s
case where Ether failed its inactivity policy while SAB eventually succeeded.
Those are concrete limits to a superiority claim. Further priority promotion
and a separately validated degraded-network recovery policy are more defensible
next steps than claiming a blanket win from the native implementation language.

## Performance evidence

See [network comparison results](usenet-network-results.md) for the controlled
SABnzbd/NNTP and HTTP experiments. They record identical inputs,
verified output bytes, connection counts, impairments, cold/warm state, CPU, and
memory. SABnzbd completion time is not Plex first-frame time, and an Ether versus
SABnzbd result does not establish an Ether versus AIOStreams result.

[aio-readme]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/README.md
[aio-yenc]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/pool/yenc.ts
[aio-workers]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/nntp/provider-worker-pool.ts
[aio-fetcher]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/nntp/segment-fetcher.ts
[aio-multi]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/pool/multi-provider-pool.ts
[aio-cache]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/pool/segment-cache.ts
[aio-rar]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/pool/archive/rar/reader.ts
[aio-7z]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/pool/archive/sevenzip/parse.ts
[aio-holes]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/holes.ts
[aio-transform]: https://github.com/Viren070/AIOStreams/blob/01dc8ca2de9e6aad567ab1bb40b2e251f466b0b0/packages/core/src/usenet/ebml/hole-fill-transform.ts
