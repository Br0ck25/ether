# Roadmap delivery

Ether retains official Plex apps as the viewer interface. The owner UI configures
sources, presentation, sharing and operations. Virtual channels and supplied IPTV
are optional; Ether ships no content, provider accounts or entitlement overrides.

| Area | Delivered |
| --- | --- |
| Playback | Device constraints and source ranking, video-copy audio processing, detail-page pre-resolution, bounded candidate racing, remote bitrate limits |
| Streams | Language defaults and per-show track memory, chapters and intro/credits metadata, optional cached-file detection, subtitle matching, normalization and timing corrections |
| Adoption | Read-only PMS preview/import, watch state and lists, local-first libraries, portable encrypted-config backups and staged restore, Trakt/Letterboxd CSV, community packaging templates |
| Safety | Adult exclusion, unrated fail-closed rating limits, per-viewer genre/keyword/time/network/bitrate/stream policies, account source cleanup, expiring invitations, credential encryption, compatibility tracking and diagnostics |
| Insights | Viewing history and analytics, owner termination, provider/indexer counters and cost inputs, notification delivery, scheduled opt-in SMTP newsletters, installable owner UI and hardware discovery |
| Discovery | Authorized Plex/Trakt watchlists, availability checks and optional notifications/prewarming, ratings, artwork overlays and variants, localization, optional LLM search/recaps, Rediscover, sticky dismissal and private Year in Review collections |
| Sources | Native custom-format and quality-profile imports, Prowlarr discovery, verified Plex GUID mapping, presentation-only episode groups and specials preferences |
| Live TV | Deterministic virtual channels, supplied M3U/XMLTV sources, guide, continuous HLS playout, seeking into current programmes, policy and shared stream-limit enforcement |
| Home Designer | Global template and per-viewer overrides, anchors, bounds, schedules and caps, draft preview/impact, migration and shared production placement |

## Verification and practical limits

Offline tests cover the new packages, protocol representations, restricted access,
transactional imports, source budgets and scheduling. Integration checks construct
the real application and exercise authenticated owner API mounts. The complete
pure-Go build, vet and test commands and the full race suite passed, along with a
Windows cross-build and a reachable-vulnerability scan. Browser review uses an
isolated unclaimed server and synthetic viewers; it does not modify a real Plex
account or start provider downloads. Core interfaces and configuration changes
are additive.

The web typecheck, 67 tests and embedded production build passed with public npm
releases of AppKit UI 0.4.0, tokens 0.2.0 and Dashboard 4.0.0. Final browser checks
covered Home, Home Designer, Live TV, stream preferences and Insights at 1280px
and 390px, with no console errors or horizontal overflow. Theme switching
persisted across reloads. Earlier browser checks also covered the remaining new
owner pages and saving a per-viewer Home override.

Read-only protocol research verified modern Plex Web Live TV request shapes and
Plex metadata-service movie/show GUID matching. This does not certify playback or
Skip Intro/Credits buttons in every official app. Actual startup under five seconds
and hardware throughput remain dependent on the provider, release, client and
encoder, and were not established by offline fixtures. Account source cleanup is
limited to Plex's legitimate advertised opt-out settings. Native Plex push has no
verified public delivery contract; configured owner notification channels are used.

Optional OCR, a downloaded VAD model, trickplay generation, extras acquisition,
offline downloads and PAR2 repair are research directions, not implemented features.
The cached-file marker detector and installed encoder capabilities require testing
against the owner's media and clients. User-supplied IPTV must already be accessible;
there is no DRM bypass or recording backend. PWA installation does not imply Web Push.

Feature-specific behavior and limitations are documented in
[playback-policy.md](playback-policy.md), [streams-features.md](streams-features.md),
[security.md](security.md), [discovery.md](discovery.md), [metadata.md](metadata.md),
[insights.md](insights.md), [livetv.md](livetv.md) and [personalization.md](personalization.md).
