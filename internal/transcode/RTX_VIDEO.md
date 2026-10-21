# NVIDIA RTX Video Super Resolution backend

Ether can launch an owner-installed **NVEncC 9.38 x64** package on native Windows.
The validated path is libavformat HTTP input → NVDEC → CUDA NGX VSR → NVENC →
libavformat HLS/DASH. Decoded frames never pass through Go or a raw-frame pipe.
The Go application still builds with `CGO_ENABLED=0`. Ether does not redistribute
NVEncC or NVIDIA DLLs with this implementation.

Install the complete public upstream package, including `NVEncNVSDKNGX.dll` and
`nvngx_vsr.dll`, in one directory. Set `transcode.rtxVideo.executablePath` to its
`NVEncC64.exe`, or put that executable on PATH. Restart after changing its path.
No driver installation, SDK account, service registration or NVIDIA license
acceptance is performed by Ether.

## Configuration and API

```json
{
  "transcode": {
    "rtxVideo": {
      "mode": "off",
      "executablePath": "D:\\Tools\\NVEnc\\NVEncC64.exe",
      "sourceMaxHeight": 720,
      "targetMaxHeight": 1080,
      "quality": 2,
      "maxConcurrent": 1
    }
  }
}
```

- `off` is the default. `auto` considers eligible LAN playback; `on` also
  considers remote playback. Neither overrides client capabilities or limits.
- Numeric zero values select the defaults shown. Source/target height ranges
  are 144–2160; actual output also respects client/request height, aspect ratio,
  maximum 3840 width, and validated H.264 level/frame-rate limits.
- Qualities are 1–4 and streaming modes 21/23. Streaming modes need compatible
  Ampere-or-newer hardware and the packaged recent NGX runtime. Quality 0 is
  never sent to NVEnc: its non-AI bicubic mode is not RTX VSR.
- Concurrency range is 1–8. Enhancement also consumes the ordinary transcode
  budget. Pending starts reserve an ordinary encoder slot to close the launch
  race. Enhancement slots are released only after native process cleanup.
- Settings are node-local. Changes apply to new session snapshots; executable
  path changes require a restart.

`Manager.EnhancementStatus() core.VideoEnhancementSupport` reports `backend`,
`available`, `reason`, `version`, `protocols`, and `active`. `available` is
installation/device detection, **not runtime success**. The supported executable
version is pinned intentionally: a future version needs transport validation.

`Decision.Enhancement` reports `backend`, `state`, `reason`, `width`, `height`, and
`quality`. `selected` is a plan; `starting` is an executing process awaiting
output. `running` requires the real NGX filter-chain log and produced segments
in this run. Completion/stop changes the state to `stopped`. A fallback reports
its reason and ordinary output dimensions; it never retains an RTX success flag.
Callers must set `DecideOptions.DisableVideoTranscode` when the client forbids
optional video transcoding. That veto also applies to otherwise direct-playable
media, which the enhancer can promote to a video transcode when eligible.

## Initial support and explicit fallbacks

Supported output is H.264 high profile, 8-bit SDR, HLS MPEG-TS or fMP4, and DASH.
Inputs must be probed finite HTTP(S) VOD with one video stream and usable
resolution/frame-rate metadata. Source codecs accepted for NVDEC are H.264,
HEVC, MPEG-2, VP9 and AV1; live proof in this slice used H.264 sources. Output
frame rates are bounded at 60 fps and H.264 level 5.1 macroblock limits. Known
interlaced or variable-rate probe hints are rejected. Rate metadata alone cannot
prove every frame of a misleading/VFR file has a stable clock.

The selected audio track is copied by its one-based audio ordinal; currently
AAC, AC-3, E-AC-3 and MP3 are accepted. Audio conversion, audio filters,
configuration changes, selected subtitles, HDR/10-bit, multiple video tracks,
segmented Matroska, and unsupported source/target constraints explicitly use the
ordinary transcoder. Enhancement has one steady rendition: adaptive ramps,
governor and extra ladder variants are disabled for these sessions. Run VBV
maxrate/buffer settings are honored, while a rate too low for the selected
resolution triggers ordinary fallback.

Input demuxer and protocol allowlists remain enforced. Header validation is
shared with the ordinary backend; URLs and header values are redacted from
backend logs. Arguments are passed directly with `os/exec`, never through a
shell. Network read timeouts, input analysis and keyframe-plan sizes are bounded.

Seeks use a nearby indexed source keyframe and a bounded frame trim, then restore
the actual first output frame's media time using `output_ts_offset`. Unknown
indices or more than 30 seconds of trim cause ordinary fallback. NVEnc disables
`--keyfile` with `--trim`: integer-frame GOP lengths therefore use a fixed exact
GOP, and fractional-rate trim seeks explicitly fall back. Fractional-rate starts
without trimming reconstruct exact standard NTSC rates from Ether’s rounded
probe field and use a bounded frame-index keyfile to prevent accumulated GOP
drift. At most 32768 future segment boundaries are planned. Fallback refreshes
manifest dimensions and adds an HLS discontinuity at an in-session change.

A startup native failure retries ordinary FFmpeg once. Source failures then use
Ether's existing bounded retry/input-failover handlers. Stop, seek, throttle,
replacement and idle cleanup use the existing session lifecycle. Windows starts
NVEnc suspended, assigns it to a private kill-on-close Job Object, then resumes
it. NGX's updater children are contained in that job (maximum 32 processes),
terminated on parent exit, and awaited before the native permit is released.
This is required: the SDK otherwise leaves updater processes and directory
handles alive after an encoder exits. No existing user process is assigned.

## Validation evidence (2026-10-20)

Native Windows 11, RTX 4080, driver 616.56, NVEncC 9.38 (r4176), Jellyfin FFmpeg
8.1.3. Public `nvngx_vsr.dll` SHA256:
`3ed97a8f5dd0afd5ceb9e98b3fa9a868a134f74103b3747e9765595606c3f509`.

The opt-in `TestRTXNativeLifecycle` generated a 720p source and served it through
a loopback HTTP server with range support, then used the real Manager/Session
engine. Checks covered genuine NGX filter execution, encoded 1920×1080 output,
keyframe-first four-second segments, an 8-second restart from a 6-second source
keyframe plus 48 trimmed frames, and restoration of absolute timestamps. A
second 880 Hz audio track was verified before and after that restart.

- Quality 23, 24 fps: HLS TS, HLS fMP4 and DASH passed, including restart and
  cleanup (13.41 seconds total).
- Quality 21, 24000/1001 fps: all three transports passed initial segment clock
  and resolution checks (7.01 seconds). Fractional trim restart is deliberately
  unsupported, not implied by this result.
- Quality 21 also passed integer-frame-rate HLS/DASH restart checks.
- A Windows offline subprocess test proved Stop terminates owned descendants
  (0.04 seconds). The final host audit found no remaining NVEnc, NGX updater or
  test-binary processes.
- Offline tests cover direct-play promotion, capability/bandwidth vetoes,
  argument safety/redaction, audio selection, exact clock plans, concurrency,
  initialization fallback, source failure, seek/failover revalidation and cleanup.

These checks establish functionality and transport correctness for these cases;
they do not establish subjective visual improvement, general VFR correctness,
or sustained multi-session performance. Benchmarks and physical Plex client
certification are separate work.

Opt in on a Windows lab host with `ETHER_RTX_TEST_EXECUTABLE` and
`ETHER_RTX_TEST_FFMPEG`. Optional `ETHER_RTX_TEST_QUALITY` selects a quality;
`ETHER_RTX_TEST_FRACTIONAL=1` tests 24000/1001 fps. Ordinary tests skip GPU execution
and never download anything. Run `TestRTXJobStopsOwnedDescendants` to exercise
Windows process-tree cleanup without a GPU.

Sources (inspected, no source snippets copied):
[public 9.38 release](https://github.com/rigaya/NVEnc/releases/tag/9.38),
[options](https://github.com/rigaya/NVEnc/blob/9.38/NVEncC_Options.en.md),
[NGX wrapper](https://github.com/rigaya/NVEnc/blob/9.38/NVEncNVSDKNGX/NVEncNVSDKNGX.cpp).
The startup warning about a bicubic *dynamic-resolution normalization* template
is not a replacement of the final NGX VSR filter; the validated final chain
explicitly reports `resize: ngx-vsr`.
