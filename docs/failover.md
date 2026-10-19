# Mid-stream failover

How `internal/playback` keeps a stream going when a release's articles
disappear while someone is watching it. The design doc fixes the outline: swap
to the next release, resume at the same timestamp, mark the failed release bad.
This note covers how the timestamp is carried across and where that breaks.

## When it triggers

- **Direct play** (`Playback.ServeRange`): a read from the session's
  `MediaSource` returns `core.ErrArticleMissing` (the engine already tried
  every provider and backfill account).
- **HLS** (remux/transcode): ffmpeg reads the source from the loopback server
  (`InputURL`). A read error there ends that response and starts a background
  failover. The front end can also call `Playback.Failover(sessionID, at)`
  itself when it knows the player position.
- **Prepare**: a release that fails to open, or whose header is missing while
  it is being probed, is skipped before the session starts.

Concurrent readers that hit the same broken release share one failover. The
first one does the swap. The others wait for it and then use the new release.

## Choosing the next release

1. `Resolver.MarkBad(release, reason)` so nobody else gets it.
2. If `Deps.FetchNZB` is wired, try the resolution's `Alternates` in order.
   Otherwise call `Resolver.Resolve` again with every release this session
   has tried in `Exclude`. The resolver keeps its search results cached, so
   this does not cost another indexer query.
3. Open it with `Engine.Open`, then probe it with `Transcoder.Probe` through
   the loopback server to get its real duration and streams. If the probe
   fails, Ether assumes the new release has the old one's duration.
4. Swap atomically. The old source is retired: new reads go to the new
   source, and the old one is closed once its in-flight reads finish. Its
   loopback URL starts returning `410 Gone`. Live pins move to the new
   release, and the resume record is updated so a later resume opens it.

At most `MaxAttempts` candidates are tried per failover and `MaxFailovers`
failovers per session. After that the client gets an error rather than an
endless loop of retries.

## Mapping the position by timestamp

Two releases of a title almost never have the same byte layout, so a byte
offset is useless across them. The position goes through time instead:

```
offset (old release) --TimeAt--> t --AlignTime--> t' --OffsetAt--> offset (new release)
```

`TimeMap{Size, Duration, Points}` does the two conversions.

- **No index (the default).** Assume a constant bitrate:
  `t = offset / size * duration`, and the reverse. Modern encodes are VBR, but
  across a whole feature the error is usually a few seconds, and it shrinks
  as the file gets larger.
- **With an index.** `Points` holds (offset, time) pairs, such as Matroska cues
  or MP4 sync samples. The mapping interpolates linearly between the points on
  either side. The transcoder contract doesn't expose an index yet. If it
  does, filling `Points` makes the mapping accurate to the nearest keyframe.

`AlignTime(t, oldDuration, newDuration)` keeps `t` unchanged for releases of
the same cut, and clamps it to the new duration. It handles one systematic
difference that the durations alone reveal: **PAL speedup**. A 25 fps release
of 23.976 fps film runs 4.3% shorter, so when the duration ratio is within
0.4% of 25/23.976 (either way), `t` is scaled by that ratio.

## Direct play: continue or end the response

The client was told one file size (the first release's), and it keeps using
that file's byte coordinates for every later request. The session therefore
keeps that first layout as its **virtual file**:

- Every response keeps the first release's size, ETag and Last-Modified, so
  the client's `If-Range` and cached `Content-Length` stay valid after a swap.
- After a failover, each request offset is mapped virtual → time → current
  release, using the formula above.

When the failure happens partway through a response, there are two cases:

- **MPEG-TS** (`mpegts`, `.ts`, `.m2ts`, `.mts`): the response *continues* from
  the new release. The mapped offset is rounded down to a 188-byte packet
  boundary. TS packets are self-contained and carry codec parameters in band,
  so the client's demuxer resyncs on the next sync byte. The response still
  delivers exactly the `Content-Length` it promised. If the new release runs
  out first, the response ends early.
- **Everything else** (Matroska, MP4, AVI): the response *ends early*. Go's
  HTTP server closes the connection because the body is shorter than the
  declared length. The player sees a broken connection and re-requests from
  the byte it reached. That request is mapped into the new release. The
  viewer sees a brief rebuffer.

## Limits

These are known and deliberate. They are the design doc's open question
about how to align timestamps across releases with different cuts or intros.

- **Different cuts.** An extended edition, a director's cut, or a release
  with extra or missing scenes shifts every timestamp after the change. The
  resolver can't tell cuts apart from durations alone. A swap can land
  minutes off. Suggested mitigation, on the resolver side: rank
  candidates whose size and name suggest the same edition first. When the duration differs by more than a few percent, and it isn't
  the PAL ratio, treat the result as approximate.
- **Different intros.** A studio logo or recap that one release has and the
  other doesn't offsets everything by that length (typically 5–60 s). Fixing
  this needs content alignment, such as audio fingerprints or scene-change
  matching around the swap point. That is out of scope for v1.
- **Constant-bitrate assumption.** Without an index, a scene much busier or
  calmer than average puts the mapped point a few seconds off. HLS restarts at
  the exact time requested, so this only affects direct play.
- **Matroska/MP4 headers.** After a direct-play swap, the client still has
  the *old* file's header: track codec private data (SPS/PPS), cues, and for
  MP4 the whole `moov` sample table. Bytes from a different encode can fail to
  decode, or MP4 seeks can land in the wrong place. Players built on ffmpeg
  usually resync on the next Matroska cluster and keyframe. Strict players
  stop with a playback error, and the user presses play again. The swapped
  session and its resume record point at the new release, so the retry
  starts cleanly. The robust path is HLS, where ffmpeg restarts on the new
  input (`HLSSession.SwapInput`) at the mapped time. A front end that sees a
  direct-play client fail after a failover can switch to a remux.
- **M2TS with 192-byte packets** is spliced at 188-byte boundaries. The
  demuxer still resyncs, but it may drop one packet.
- **Precision.** The position carried over is where Ether's reads failed,
  which is at or a little ahead of what the viewer sees, because of the
  player's buffer. Expect up to a few seconds of skip or repeat.
