# Roadmap delivery

Ether retains official Plex apps as the viewer interface. The owner UI configures
sources, presentation, sharing and operations. Virtual channels and supplied IPTV
are optional; Ether ships no content, provider accounts or entitlement overrides.

| Area | Delivered | Why |
| --- | --- | --- |
| Playback | Device constraints and source ranking, video-copy audio processing, detail-page pre-resolution, bounded candidate racing, remote bitrate limits | Choose a compatible source before paying for a video transcode; move bounded work ahead of Play without exhausting indexer quotas. |
| Streams | Language defaults and per-show track memory, chapters and intro/credits metadata, optional cached-file detection, subtitle matching, normalization and timing corrections | Remove repeated language/track corrections and make subtitle timing and audio processing consistent across episodes. |
| Adoption | Read-only PMS preview/import, watch state and lists, local-first libraries, portable encrypted-config backups and staged restore, Trakt/Letterboxd CSV, community packaging templates | Preserve existing libraries and history so trying Ether does not require abandoning a working PMS setup. |
| Safety | Adult exclusion, unrated fail-closed rating limits, per-viewer genre/keyword/time/network/bitrate/stream policies, account source cleanup, expiring invitations, credential encryption, compatibility tracking and diagnostics | A broad catalogue needs restrictions at every endpoint, and sharing must preserve real Plex account boundaries. |
| Insights | Viewing history and analytics, owner termination, provider/indexer counters and cost inputs, notification delivery, scheduled opt-in SMTP newsletters, installable owner UI and hardware discovery | Expose observed playback and Usenet costs directly, reducing the need for separate monitoring and notification tools. |
| Discovery | Authorized Plex/Trakt watchlists, availability checks and optional notifications/prewarming, ratings, artwork overlays and variants, localization, optional LLM search/recaps, Rediscover, sticky dismissal and private Year in Review collections | Replace a request portal with watchlist/availability reminders, and deliver discovery through surfaces native Plex apps can render. |
| Sources | Native custom-format and quality-profile imports, Prowlarr discovery, verified Plex GUID mapping, presentation-only episode groups and specials preferences | Reuse owners’ existing indexer and quality preferences while keeping canonical media identity and watch history stable. |
| Live TV | Deterministic virtual channels, supplied M3U/XMLTV sources, guide, continuous HLS playout, seeking into current programmes, policy and shared stream-limit enforcement | Provide lean-back viewing from the same catalogue or supplied IPTV while sharing playback policy and stream limits. |
| Home Designer | Global template and per-viewer overrides, anchors, bounds, schedules and caps, draft preview/impact, migration and shared production placement | Let the owner establish a common Home while retaining personal recommendations and explicit viewer exceptions. |

## Research findings that changed the implementation

The earlier research ranked opportunities by reported user pain, reach, effort
and risk. It was qualitative research, not a measured survey. Implementation
then checked the relevant primary API documentation, source code and read-only
Plex responses. Several promising ideas needed a narrower implementation:

- **Markers:** TheIntroDB has a documented duration-aware read API. Ether uses
  that API and container chapters, with conservative cached-file detection as a
  fallback. The local detector uses pure-Go fingerprints rather than claiming
  Chromaprint compatibility. Supplying marker data does not establish that every
  Plex client shows a Skip button.
- **Plex identity and extras:** Read-only TMDB-to-Plex matching returned verified
  movie/show identities. Those mappings were adopted. The sampled detail calls
  returned no extras, so a valid GUID was not treated as a trailer source or as
  permission to reuse theme music or cloud watch-state APIs.
- **Plex notifications:** Reference logs revealed the notification endpoint but
  not its payload or supported authorization contract. Ether therefore uses
  configured SMTP, Discord, Slack, Telegram, ntfy, Gotify and webhook channels.
- **Client entitlements and clutter:** Account opt-outs use settings Plex
  advertises for an authorized account. Server access does not grant control of
  a friend's independent account, and server capability fields cannot grant
  paid client features.
- **OCR, downloads and benchmarks:** PGS OCR would require an OCR engine and
  trained data; native downloads require further client/protocol work. Neither
  is shipped here. Hardware discovery and startup optimizations are implemented,
  while physical-device throughput and startup targets remain unmeasured.

The feature-specific documents below retain primary references and describe the
implemented contracts. Release scoring follows native TRaSH/Radarr semantics;
episode groups change presentation while retaining canonical playback identity.
The owner UI reuses published AppKit components for page layout and navigation.

## Verification and practical limits

Offline tests cover the new packages, protocol representations, restricted access,
transactional imports, source budgets and scheduling. Integration checks construct
the real application and exercise authenticated owner API mounts. The complete
pure-Go build, vet and test commands and the full race suite passed, along with a
Windows cross-build and a reachable-vulnerability scan. Initial browser review
used an isolated unclaimed server and synthetic viewers. Follow-up read-only
checks used the owner's running local server without saving account or playback
preferences. Core interfaces and configuration changes are additive.

The web typecheck, 67 tests and embedded production build passed with public npm
releases of AppKit UI 0.4.0, tokens 0.2.0 and Dashboard 4.0.0. Final browser checks
covered Home, Home Designer, Live TV, stream preferences and Insights at 1280px
and 390px, with no console errors or horizontal overflow. Theme switching
persisted across reloads. Earlier browser checks also covered the remaining new
owner pages and saving a per-viewer Home override.
Follow-up checks exercised real mouse-wheel scrolling on the eight new settings
pages and Home Designer at desktop and phone widths, and verified the shared
AppKit underlined subtabs. Long content scrolls inside the shell; short pages
retain the same bounded page container.

Read-only protocol research verified modern Plex Web Live TV request shapes and
Plex metadata-service movie/show GUID matching. This does not certify playback or
Skip Intro/Credits buttons in every official app. Actual startup under five seconds
and hardware throughput remain dependent on the provider, release, client and
encoder, and were not established by offline fixtures. Account source cleanup is
limited to Plex's legitimate advertised opt-out settings. Native Plex push has no
verified public delivery contract; configured owner notification channels are used.

Optional OCR, a downloaded VAD model, trickplay generation, extras/theme acquisition,
offline downloads and PAR2 repair are research directions, not implemented features.
The cached-file marker detector and installed encoder capabilities require testing
against the owner's media and clients. User-supplied IPTV must already be accessible;
there is no DRM bypass or recording backend. PWA installation does not imply Web Push.

Feature-specific behavior and limitations are documented in
[playback-policy.md](playback-policy.md), [streams-features.md](streams-features.md),
[security.md](security.md), [discovery.md](discovery.md), [metadata.md](metadata.md),
[insights.md](insights.md), [livetv.md](livetv.md) and [personalization.md](personalization.md).
