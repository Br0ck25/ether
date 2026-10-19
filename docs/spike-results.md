# Spike results

Status as of 19 Oct 2026. The design doc's gate is "Plex Web, iOS and Apple TV
all browse and play". This records what has been verified, against what, and
what is still open. Procedures are in [spike.md](spike.md).

## Plex Web: passes

Tested with the real Plex Web app (app.plex.tv, Plex Web 4.162.1, Chrome 155)
against a local Ether install claimed to a real Plex account, playing from
real Usenet providers and a real Newznab indexer.

| Check | Result |
| --- | --- |
| Server listed in the account (resources) | Yes: presence=true, https plex.direct connections |
| Browse library, hubs, artwork | Yes: Movies section, Recommended/Library/Collections/Playlists/Categories tabs |
| Detail page | Yes: poster, backdrop, rating, genres, cast, virtual 1080p media |
| Play | Yes: The Matrix (1999) remuxed to DASH (H.264 copied, E-AC-3 → AAC), 1920×800 |
| Seek | Yes: jump to 1:00:00 resumed playback at segment 720+ |
| Mid-stream failover | Exercised by real data: articles went missing at 2:55; Ether moved to another post of the same release at the same timestamp (a bug moving ffmpeg to the new input was found and fixed) |

Chrome 142+ asks the user to allow app.plex.tv to reach devices on the local
network ("Local Network Access"). That prompt also applies to a real PMS; the
user must allow it for LAN playback from app.plex.tv.

## iOS and Apple TV: not yet tested

They need the owner's devices. Ether's responses for both were matched to
recorded PMS 1.43.4 responses for their header profiles (`testdata/pms`,
`pmsdiff`), including transcode decisions, but no device has played from
Ether yet. Run the checklist in [spike.md](spike.md).

## Open questions from the design doc

**Can Ether get a `*.plex.direct` certificate?** Yes. plex.tv issued a
90-day Let's Encrypt wildcard `*.<hash>.plex.direct` to Ether's own device
through the same CSR flow PMS uses (details in
[plextv-notes.md](plextv-notes.md)). Clients connect over verified HTTPS.

**What do the apps enforce for Plex Pass?** Remote playback of personal media
requires Plex Pass on the owner or the viewer (or a Remote Watch Pass) since
29 April 2025, enforced by the apps and plex.tv from account entitlements,
not by the server. Ether reports the owner's real subscription. Not yet
measured against Ether with a remote, non-Pass viewer.

**How should a title look before its release is resolved?** Ether shows one
virtual Media/Part with media info estimated from the client's quality
profile (e.g. 1080p H.264, MKV). Plex Web accepted this for the detail page,
decision and playback; the real values replace it once a session exists.

**What popularity threshold gives a useful catalogue?** See
[catalog-threshold.md](catalog-threshold.md). The defaults (popularity ≥ 5,
votes ≥ 100) produced ~25k movies and a growing set of shows on first sync.

**How should failover align timestamps between releases?** Ether maps the
play position by time, not bytes ([failover.md](failover.md)). Different
cuts or intros remain approximate.

## Time to playable

Measured with `cmd/ether-livebench` on real providers (see
[performance.md](performance.md)):

| Case | Time |
| --- | --- |
| First play of a title (search, NZB, health check, open, probe) | ~6–8 s |
| Repeat play or resume (health check cached for 6 h) | ~0.4 s |
| Through the Plex API from Plex Web, cold | ~5.7 s to a decision |

Most of a first play is the NNTP health check. Starting playback before the
check finishes, relying on mid-stream failover, would bring it under 5 s at
the cost of occasionally starting a dead release.
