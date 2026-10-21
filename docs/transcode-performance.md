# Transcoding performance and validation

Evidence collected 2026-10-20 on an Apple M4 Pro Mac, a Windows RTX 4080 workstation, and a Linux Ryzen 7 2700X server. Results below are bounded experiments, not a universal claim that Ether outperforms every Plex configuration.

## Method

Plex Media Server 1.43.5.11029 was driven through its real decision, HLS start, playlist, segment, and stop APIs. Captured owned-process arguments prove the encoder used per session. Ether uses `cmd/ether-transcodebench`, exercising the actual Manager probe, session, and segment-serving implementation; its complete PMS frontend overhead is not included. Spike mode is not a transcode benchmark.

Each comparison uses identical source bytes on the same machine, sequential segment consumption, actual playlist media duration, exact output codec/dimension checks, and separate setup/delivery/verification timing. The comparison uses Ether setup plus delivery and Plex setup-inclusive delivery wall time. Concurrent throughput uses total delivered media divided by the wave wall time, never the sum of individual session durations. Verification and cleanup are outside delivery timing. Run each product alone and alternate order; the repeated measurements below contain two runs per condition.

Hardware results require authentic hardware encoder arguments for every session and reject silent software fallback. Windows cumulative Job Object CPU counters include exited children. Linux and Mac process snapshots cannot reliably account for exited Plex encoders; no CPU savings versus Plex are inferred there. Comparable whole-server peak-memory data are not available.

## Results

| Host / workload | Sessions | Ether wall seconds (two runs) | Plex wall seconds (two runs) | Aggregate throughput gain |
|---|---:|---|---|---:|
| Windows, synthetic 1080p24 → 720p, 63s media | 1 | 2.343 / 2.336 | 6.146 / 5.854 | 156% |
| Windows, same | 4 | 7.148 / 7.149 | 9.804 / 9.887 | 38% |
| Windows, same | 8 | 13.701 / 13.758 | 16.736 / 16.647 | 22% |
| Windows, natural 720p60 → 360p, 32s media | 8 | 9.111 / 9.067 | 12.083 / 11.959 | 32% |
| Linux, natural 720p60 → 360p, 32s media, software | 1 | 3.139 / 3.228 | 6.12 / 6.41 | 97% |
| Linux, same | 4 | 8.437 / 8.899 | 12.48 / 13.36 | 49% |
| Linux, same | 8 | 16.911 / 16.508 | 23.03 / 22.80 | 37% |
| Mac, synthetic 1080p24 → 720p, 63s media | 1 | 4.050 / 3.979 | 5.576 / 5.630 | 40% |
| Mac, same | 4 | 14.077 / 14.102 | 13.693 / 13.665 | −3% |

Windows eight-session HD CPU: Ether 11.531/12.062 CPU-seconds versus Plex 41.250/38.922, a 70.6% reduction. Ether's job scope includes startup/probing/verification; Plex's reported wave excludes server startup and output verification. This is not a memory-saving claim.

Quality checks use aligned decoded frames against the same scaled source. SSIM is one objective measure, not a perceptual guarantee:

- Windows HD: Ether 0.977762, Plex 0.977793; output size differs by 0.43%.
- Windows natural: Ether 0.988758, Plex 0.985322; Ether output is about 6% smaller.
- Linux natural: Ether CRF 18 gives 0.985091 versus Plex 0.984677, with 2.2% more bytes. The earlier CRF 21 comparison was faster at lower quality and is deliberately not used as the final quality-controlled result. This experiment motivated changing the software H.264 default to CRF 18.
- Mac HD: Ether 0.977810 versus Plex 0.953457, but Ether output is 36.6% larger. A separate single-session reduced-rate Ether run produced 19.93 MB versus Plex 20.75 MB, SSIM 0.977082, and 4.025s wall. The reduced-rate four-session comparison has not been repeated; the table is not a size-matched Mac claim.

Plex re-encodes compatible AAC in these profiles while Ether copies it. Actual video rates also differ slightly in the Windows HD comparison. Linux uses the same 1552 kbps video ceiling, but audio still differs. These are measured product choices, not identical encoder command lines.

## Concurrency limits

Both products delivered twelve simultaneous hardware sessions on the tested Windows workload. At twenty-four sessions, both used twelve hardware and twelve software encoders. Ether delivered all media, but its strict hardware-only benchmark correctly failed the hardware claim. The Plex twenty-four-session run also failed its immediate cleanup audit; a subsequent independent audit found no remaining processes. The driver now waits a bounded interval for Job Object termination to drain. Those runs do not establish higher maximum hardware capacity or a clean sustained-capacity win.

The Linux Radeon HD 7450 has decode-only VA-API and cannot hardware encode. Linux results are software-only. No QSV or modern VA-API device was available for live validation.

## Fixtures and reproduction

- Shared synthetic HD SHA-256: `61f108b757a2598bbf7425560fb76ef0b290b0aa94fdb792d2dd6ac23b1a8f35` (64.021s source; 63s delivered).
- Windows natural SHA-256: `bea2af0b7b672416785762d292be345e05d7f650c4e40a0784f43b78f66441e8`.
- Linux natural SHA-256: `440478a325a154eb32ada1b55d7a401f1f399d4f1213bb67a0eddbeb2bf4dffa`.

Natural fixtures were generated from the public FourPeople clip at https://media.xiph.org/video/derf/webm/FourPeople_1280x720_60.webm. Windows and Linux generated fixtures differ: comparisons are within each host, not across hosts.

Build `go build ./cmd/ether-transcodebench`. For the final Linux profile, use `-hwaccel none -codec h264 -max-height 360 -bitrate-kbps 1679 -duration 32s -segment-duration 8s`, with the natural fixture and concurrency 1, 4, and 8. For Windows HD, use NVENC, height720, bitrate3547, duration63s, segment3s; for Windows natural, height360, bitrate1744, duration32s, segment8s. Supply pinned FFmpeg/FFprobe paths, result JSON, and retained output directory. Inspect `-help` for exact argument names.

The authentic Plex drivers and offline tests are in `scripts/transcodebench/plex_bench{,_win,_mac}.py`. Use `--fixture` matching the library ratingKey; do not rely on a default filename. Lab servers must own their process trees, data directories, and credentials, and cleanly release their ports. Never run a baseline against production content or an occupied production port.

Condensed numeric evidence is in `docs/benchmarks/transcode-2026-10-20.json`. Full local raw evidence remains in the coordinating thread's `transcode-lab` directory; no account tokens or copyrighted films are included in this repository.

## Server-side enhancement

The NVIDIA path is genuine NGX Video Super Resolution through NVEnc, with NVDEC → VSR → NVENC in one process. It defaults off and has explicit eligibility, concurrency, fallback, and lifecycle status. It does not merely request browser-side enhancement. See `internal/transcode/RTX_VIDEO.md` for supported formats and limits. Driver/DLL detection alone does not count as runtime success.

## Remaining scope

Short repeated clips do not establish hours-long capacity, all content types, all client implementations, or universal superiority. Browser impairment and native client protocol tests are separate from throughput tests. No encoder can sustain uninterrupted playback through an outage longer than the buffered media. A client must select/abandon requests appropriately; the server must honor that selection promptly without changing timestamps or returning the wrong rendition.

## Native Plex quality changes

Plex documents [Automatically Adjust Quality](https://support.plex.tv/articles/115007570148-automatically-adjust-quality-when-streaming/) for supported transcoding clients and a separate [Quality Suggestions](https://support.plex.tv/articles/quality-suggestions/) feature. Container choice alone does not establish whether a player can adapt.

Ether previously reused a cached decision when a client sent a new universal start request at the same offset, ignoring changed bitrate, resolution, and audio selection. HLS and DASH handlers now reevaluate changed playback controls through the normal policy/decider and restart at the requested offset. Identical starts retain the session. Regression replay covers TS, segmented Matroska, and DASH, owner WAN caps, audio selection and budget adaptation. This is offline HTTP compatibility evidence, not a captured native-player session. The segmented-Matroska single-rendition safeguard remains while explicit start-time quality changes are honored.

## Browser adaptation evidence and limits

A bounded Chrome/hls.js test uses the real Manager, 2s HLS segments, software H.264, a 64s fixture, and CDP bandwidth changes: 8 Mbps/40ms → 1 Mbps/120ms after 12s → 8 Mbps after another 20s. It runs on an ephemeral loopback listener and records client playback events separately from server writes. Loopback socket timings are deliberately not treated as client bandwidth.

The test first exposed an eight-second rendition dwell that starved a player after its first two-second segment. The server now permits the initial player choice immediately and permits a full next-segment GET after completed delivery in either quality direction. Init-first players may make one switch after current-track delivery; further init probes cannot reuse the old track's evidence. HEAD, ranged, stale, canceled, and repeated-opening probes remain guarded. The client still controls rendition selection and request abandonment.

An fMP4 restart also exposed an 85.8ms copied-audio gap caused by x264 B-frame interleaving. A real packet regression fails without the correction and passes at 0.5ms gap with it. The scoped software-H.264/fMP4 audio interleave lead preserves compressed audio bytes and avoids changing video quality. Windows fMP4 init filenames are resolved against the rendition directory; POSIX keeps the relative name because its FFmpeg muxer already prefixes the output directory. Native regression covers both behaviors.

Final fMP4 browser run with the fixture's normal 12s/16s buffer settings started playback at 1.325s after navigation, moved through 360p/480p/720p and back down during the bandwidth drop, and recorded **zero post-start waiting events** over the 44s observation. One nonfatal `bufferAppendNoProgress` event occurred during later recovery. This is one client/fixture run, not universal stall-free proof.

The TS run with a deliberately tighter 6s/8s buffer still stalled for 1.68s under the same eightfold drop. A normal-buffer TS repeat stalled for 1.90s; the player redownloaded the same media slot on a lower rendition before fetching the next slot. Neither is omitted or labeled a pass. A server cannot force an unmodified client to abandon an oversized pending transfer sooner. The old segmented-Matroska single-rendition protection also remains. End-to-end native-client ABR certification remains unfinished.

Final native VSR transition proof covers TS/fMP4/DASH: enhanced 1080p → ordinary 480p/360p → genuine NGX recovery, unsupported-seek permanent fallback, initial-seek fallback, and replay after fallback. Lower segment readiness was 274–299ms at 480p and 723–794ms at 360p; native recovery was 1.85–1.89s. These are encoder readiness measurements, not client switch latency. Packet IDR, GOP, selected 880Hz audio, timestamps, manifest dimensions/codecs, permit release, and complete process-tree cleanup were checked.

A later 32-session overload test completed all 32 Ether streams with verified output, using automatic software fallback where hardware admission failed. It took roughly 106s to deliver 63s per session and therefore **did not sustain 32 real-time streams**. Plex's corresponding run timed out, with owned-process cleanup successful. This shows bounded completion behavior, not a sustained-concurrency victory.

Final combined validation includes native CGO-disabled build, vet, and full Go tests on macOS, Windows, and Linux; 220 UI tests; and 93 offline benchmark-driver tests. The final Windows RTX lifecycle rerun after the audio correction passed TS/fMP4/DASH in 23.3s. No live services or account claims are made by ordinary unit tests. Failed exploratory runs remain in the lab evidence and are excluded from successful performance claims.
