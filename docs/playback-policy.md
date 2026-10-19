# Playback policy and source scoring

The decision path retains video whenever its codec, declared profile/level,
resolution, dynamic range and bandwidth fit. Audio incompatibility or a trusted
viewer audio filter produces audio encoding with video copy. Text subtitles
always stay separate even if a legacy client requests burn-in. Image subtitles
burn only when the client cannot render that codec or explicitly requests burn.
Output mapping keeps only the chosen video/audio streams, in that order, so
unselected TrueHD, DTS, commentary and subtitle tracks do not leak into HLS/DASH.

`transcode.ClientPolicyFor` is a conservative supplemental quirk matrix, not a
claim that every model in a platform family has the same display, receiver,
codec or Dolby Vision support. Codec/container declarations still come from the
Plex request. Explicit subtitle codecs, Dolby Vision profiles, video profiles
and levels override the supplemental rules. Apple DV profile 7 falls back to a
compatible base layer rather than being assumed fully supported. HDR10+ can use
its HDR10-compatible presentation; dynamic HDR10+ metadata is not advertised.
No account entitlement or Plex Pass feature is spoofed.

Remote ranking estimates bitrate from indexed bytes and title runtime, reserving
10% headroom. A release that fits the cap gets preference over a larger source
that would need a video encode. This remains an estimate until probing; the
transcoder makes the final decision from actual media information.

Detail-page warming is separate from live sessions: two titles server-wide,
one per user, six requests per minute per user, an eight-second timeout and at
most 2 MiB at background prefetch priority. It does not write playback history or
reserve a stream. Local adopted titles are skipped. Prepared resolutions expire
after a minute and are invalidated on configuration changes. Request completion
does not cancel warming; manager shutdown cancels and joins it.

Ranked validation starts two candidates together without waiting for a lower
ranked candidate when the first is healthy. Optional `healthRace` races up to
four and takes the first healthy answer; losing workers are cancelled and
joined. It can consume extra NZB grabs. Optional `optimisticStart` returns the top
NZB before health completes. A failed background check marks the release bad;
actual missing-article reads trigger existing timestamp-based failover. Call
`resolver.Service.Close` before closing its database to cancel/join background
checks. These settings default off. No live-provider latency measurements were
made for this change, and sub-five-second startup is not claimed.

TRaSH imports use the native JSON schema and bounded .NET-compatible regular
expressions, including lookaround. Rules group by implementation: all groups
must match, required conditions must pass, and at least one condition in each
group must match. Scored matches appear in candidate traces. Global and profile
score overrides and minimum scores are owner-editable. Quality imports retain
exact source/resolution gates and format IDs. Unsupported specifications or
native enum values are rejected explicitly; original-language and indexer-flag
conditions are not inferred from missing metadata. Definitions are not bundled
or copied from the guide; owners import their chosen JSON. Owner-selected JSON
can be cached atomically and reused when the network is unavailable.

Prowlarr discovery reads its indexer API, imports enabled Usenet sources only,
and uses the documented `/{id}/api` Newznab alias. Each source has a stable
instance-scoped ID and Ether's ordinary hit/grab budget, caching and reputation.
Discovery does not issue indexer searches or NZB grabs.

Primary references checked during implementation:

- [Prowlarr Newznab controller and route aliases](https://github.com/Prowlarr/Prowlarr/blob/develop/src/Prowlarr.Api.V1/Indexers/NewznabController.cs)
- [Radarr custom-format grouping semantics](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/CustomFormats/SpecificationMatchesGroup.cs)
- [Radarr native quality source values](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Qualities/QualitySource.cs)
- [TRaSH custom format definitions](https://github.com/TRaSH-Guides/Guides/tree/master/docs/json/radarr/cf)
- [TRaSH HD Bluray + WEB quality profile schema](https://github.com/TRaSH-Guides/Guides/blob/master/docs/json/radarr/quality-profiles/hd-bluray-web.json)
