# Ether — Design Doc

Oct 18, 2026 · @Braedon

## Overview

Ether is an open-source server that presents itself to Plex apps as a Plex Media Server, but holds no media. Its library is a filtered TMDB catalogue; when someone presses play, Ether finds a release on Usenet and streams it in about 5 seconds.

The point is a library you never manage. Nothing is added, downloaded or organised ahead of time. Collections, recommendations and watch history work as they do in Plex, because Ether generates them.

**Goals**

- Official Plex apps (Web, iOS, Apple TV) browse and play Ether with no client changes.
- Any title in the catalogue starts playing in about 5 seconds, with seeking.
- Native installers for Windows, macOS and Linux, set up through a web wizard in minutes.
- Shareable with Plex friends, each with their own history and recommendations.

**Non-goals for v1**

- Anime-specific catalogue behavior, music and DVR recording.
- Torrent or debrid backends.
- A hosted service run by the project. Ether is software people run themselves.
- Ether's own client apps, unless the Plex spike fails.

## Roadmap additions — October 19, 2026

Optional local libraries now supplement the TMDB catalogue and play before an
on-demand source. Owners can also build virtual 24/7 channels and supply their
own IPTV playlists and XMLTV guides. These extend the original catalogue-only
and Live TV exclusions. Ether continues to ship no content or provider accounts.
Official Plex apps remain the viewer interface, with real account entitlements.

The expanded playback, adoption, safety, discovery and operations features are
summarized in [roadmap-delivery.md](roadmap-delivery.md), with their protocol and
measurement limits recorded alongside each feature.

## Decisions

These were settled in the design interview on 18 Oct 2026.

| Area | Decision |
| --- | --- |
| Audience | Open source, self-hosted by others; never a hosted service |
| Licence | GPLv3 |
| Client strategy | Emulate Plex Media Server; fall back to own Flutter clients if the spike fails |
| Stack | Go, vendoring AltMount's MIT-licensed NNTP, yEnc, RAR and cache code |
| Platforms | Native installers for Windows, macOS, Linux; tray app with auto-update |
| Setup | Web wizard: plex.tv sign-in, Usenet providers, indexers |
| Media types | Movies and TV only |
| Catalogue | Released titles above a popularity threshold; search reaches all of TMDB |
| Unreleased titles | Shown, and playback is attempted |
| Cam/telesync releases | Owner setting, off by default |
| Missing titles | Checked lazily; failures hidden for a while, then rechecked |
| Cached vs uncached | Not distinguished in the UI |
| Users | Plex accounts shared via plex.tv; per-user history, recommendations, limits |
| Remote access | Plex-style: connections published to plex.tv, port forward |
| Plex Pass gating | Investigate in the spike |
| Usenet | BYO; multiple providers with priority and backfill; multiple Newznab indexers |
| Indexer usage | Cached results and per-indexer daily budgets |
| Play start | Resolve inline, stream in about 5 s |
| Quality | Client-aware profiles, owner-editable |
| Mid-stream failure | Swap to next release, resume at the same timestamp |
| Cache | Disk budget, LRU eviction, pinning |
| Prefetch | Next episode; keep in-progress titles warm |
| Transcoding | Bundled ffmpeg, hardware auto-detect, CPU fallback |
| Metadata | TMDB with a bundled key; owner can override |
| Collections | Franchises, trending rows, smart rules |
| Recommendations | TMDB similar and trending, optional Trakt, local history model, optional BYO LLM |
| Subtitles | Embedded first, OpenSubtitles when missing |
| Spike pass bar | Plex Web, iOS and Apple TV all browse and play |

## Architecture

Ether is one process with five parts. Plex apps only ever talk to the front end; Usenet and indexers sit behind the resolver and stream engine.

&#91;embedded content: Ether architecture · 5 internal components, 5 external services\]

A play request flows down: the front end asks the resolver for a release, the stream engine pulls its segments from Usenet, and the transcoder serves them back in whatever form the client can play.

## Plex front end

Ether speaks the Plex Media Server HTTP API well enough that official Plex apps treat it as a normal server. Plex published an OpenAPI 3.1 contract for PMS in September 2025, which is the reference.

**Registration**

1. The setup wizard runs the plex.tv sign-in and claim flow, the same as a fresh PMS install.
2. Ether stores the server token and publishes its connection URIs (LAN and WAN) to plex.tv.
3. Clients find Ether through the account's resources list and connect to it directly.
4. Open question: whether Ether can obtain a `*.plex.direct` HTTPS certificate. Clients strongly prefer HTTPS, so the spike must answer this first.

**Endpoints for v1**

| Group | Endpoints | Purpose |
| --- | --- | --- |
| Server | `/identity`, `/` | Identity and declared capabilities |
| Library | `/library/sections`, `/library/sections/{id}/all` | Movies and TV sections, paged with `X-Plex-Container-Start/Size` |
| Items | `/library/metadata/{id}`, `/children`, `/grandchildren` | Detail pages, seasons, episodes |
| Home | `/hubs`, `/hubs/sections/{id}` | Trending, recommendation and collection rows |
| Search | `/hubs/search` | Reaches the full TMDB catalogue |
| Collections, playlists | `/library/collections`, `/playlists` | Generated and user-created |
| Playback | `/video/:/transcode/universal/decision`, `start.m3u8`, direct file parts | Direct play, remux or transcode |
| State | `/:/timeline`, `/:/scrobble`, `/:/rate` | Progress, watched state, ratings |

**Item identity**

- Every catalogue title gets a stable `ratingKey` derived from its TMDB ID, so a title keeps the same key before and after anything is cached.
- Each item exposes one virtual Media/Part. Its codec, resolution and duration come from the release chosen for that client; before resolution they are estimates from the quality profile.

**Users and remote access**

- Shared Plex users authenticate with their own plex.tv tokens. Ether keeps history, ratings, recommendations and content-rating limits per user.
- The owner sets per-user stream limits, because each stream holds Usenet connections.
- Remote access works like PMS: Ether publishes WAN connections to plex.tv and relies on UPnP or a manual port forward.
- Plex Pass: since April 2026 official apps require Plex Pass for remote streaming. The spike records what the apps enforce against a third-party server, and Ether reports its features honestly either way.

## Usenet engine and resolver

The resolver turns a title into a healthy release; the engine turns that release into a seekable byte stream. Neither writes a full file to disk.

**Resolver: title to release**

1. Check the cache and the per-title search cache first.
2. Query Newznab indexers by TMDB/IMDb/TVDB ID, spread across indexers within each one's daily budget.
3. Filter: drop compressed or passworded RARs, obfuscated posts without usable names, and cams unless the owner allows them.
4. Rank against the client's quality profile (below), then by size sanity, age and indexer reputation.
5. Health-check the top candidate: NNTP `STAT` on a sample of segments across the file. A failure moves to the next candidate.
6. Hand the winning NZB to the engine. Keep the next two candidates for failover.

**Client-aware quality**

- Ether reads the client's declared capabilities from its Plex headers and profile.
- 4K HDR or Dolby Vision goes only to clients that can direct-play it; others get 1080p, so most playback needs no transcode.
- Owners edit the profiles: resolution caps, preferred sources (WEB-DL, Blu-ray, remux), size limits, audio preferences.

**Engine: release to stream**

- Built on AltMount's Go code for NNTP connection pools, yEnc decoding, and store-mode RAR and 7z reading.
- Builds a byte-offset index from the NZB segment list, so HTTP range requests map to exact segments. That gives seeking anywhere.
- Fetches segments in parallel across connections and reads ahead of the play position. Target: first frame in about 5 seconds.
- Multiple providers: primary first, block accounts backfill missing articles, each with its own connection limit.

**Failover mid-stream**

1. A segment missing on every provider triggers failover.
2. The engine switches to the next candidate release and maps the current play position by timestamp, not byte offset.
3. The client sees a brief rebuffer and resumes at the same moment.
4. The failed release is marked bad so no one else gets it.

**Lazy availability**

- A title is checked when someone opens it or prefetch touches it, never by bulk scanning.
- A title with no healthy release is hidden for a cooling-off period, then checked again.
- Unreleased titles stay visible. If no acceptable release exists, playback fails with a clear "not available yet" message instead of a spinner.

## Catalogue, collections and recommendations

The catalogue is metadata, not files. Ether keeps a local mirror of the TMDB slice it shows and fetches anything else on demand.

**Catalogue**

- Source: TMDB, through a bundled project API key that owners can replace with their own.
- Browsable set: released movies and shows above an owner-tunable popularity or vote threshold.
- Search reaches every TMDB title, and a searched title joins the browsable set.
- Artwork, cast, ratings and episode lists come from TMDB, refreshed on a schedule and when opened.

**Collections**

| Kind | Built from | Example |
| --- | --- | --- |
| Franchise | TMDB collections | All Mission: Impossible films |
| Trending rows | TMDB trending, top rated, new releases | Trending this week |
| Smart rules | Owner or user filters on genre, decade, rating, runtime, language | 80s sci-fi under 2 hours |

Collections are generated live from the catalogue, so they never go stale or have missing items.

**Recommendations** appear as Home rows through `/hubs`, per user:

- **TMDB similar and trending:** works with no setup.
- **Trakt (optional, per user):** syncs history and pulls personalised lists.
- **Local history model:** scores titles from each user's watches, completions and ratings.
- **LLM (optional, off by default):** any OpenAI-compatible endpoint the owner supplies, such as Ollama or a cloud API. Used for "more like this" and themed rows in natural language.

**Subtitles**

- Use subtitles embedded in the release first.
- When a user's language is missing, fetch from OpenSubtitles and serve as a sidecar track.
- Language preferences are per user.

## Cache, prefetch and transcoding

The cache exists to make rewatches, seeks and the next episode instant. It is a budget, not a library.

**Cache**

- Stores decoded segments on disk, keyed by release and byte range, so partial titles are useful.
- The owner sets a disk budget. Eviction is least-recently-used.
- Never evicted while protected: titles in progress for any user, and titles a user pinned.
- Cached and uncached titles look identical in Plex; both start in about 5 seconds.

**Prefetch**

- Next episode: start pulling the opening minutes when the current episode nears its end.
- Continue watching: keep the resume point of in-progress titles warm.
- Prefetch runs at low priority and yields connections to live streams.

**Transcoding**

- Ether ships its own ffmpeg build, so owners install nothing extra.
- At startup it detects hardware encoders: Intel QuickSync, NVIDIA NVENC, VAAPI on Linux, VideoToolbox on macOS. With none, it falls back to CPU.
- Decision order per playback: direct play, then remux into HLS (container change only), then full transcode.
- Client-aware release choice should make full transcodes rare, mostly for bandwidth limits on remote streams.

## Packaging, setup and updates

Ether installs and updates like Plex Media Server: one installer, a background service, a tray icon, and a web UI for everything else.

**Packaging**

| Platform | Package | Runs as |
| --- | --- | --- |
| Windows | Signed installer (.exe/.msi) | Service plus tray app |
| macOS | Signed, notarised .dmg | Login item plus menu-bar app |
| Linux | .deb, .rpm, Docker image | systemd service |

Each package is a single Go binary plus the bundled ffmpeg. Ether serves streams over HTTP itself, so no FUSE driver is needed on any platform.

**Setup wizard** (opens in the browser on first run)

1. Sign in to plex.tv and claim the server.
2. Add Usenet providers: host, port, SSL, credentials, connection count, priority.
3. Add Newznab indexers: URL, API key, daily limit.
4. Choose a cache folder and disk budget.
5. Optional: own TMDB key, Trakt, OpenSubtitles, LLM endpoint, cam setting.

The wizard tests each provider and indexer before moving on.

**Updates**

- The tray app checks a signed release feed and either prompts or auto-updates, per the owner's choice.
- Linux packages update through apt, dnf or a new Docker image.

**Licence**

GPLv3. AltMount's MIT-licensed code is vendored with its notice kept. The repository states that Ether ships no content and that owners are responsible for what they access.

## Spike plan and roadmap

Nothing else is built until the spike proves official Plex apps will accept Ether.

&#91;embedded content: Roadmap · 5 phases, 1 gate\]

**Spike scope**

- A minimal server that claims itself to a plex.tv account and serves one library with one local MP4.
- Passes when Plex Web, iOS and Apple TV all list the library and play the file with seeking.
- Also records: whether a `*.plex.direct` certificate is obtainable, what the apps enforce for Plex Pass on remote streams, and how the Apple TV app treats transcode decisions.
- A failed spike moves the project to its own Flutter clients; the engine, resolver and catalogue carry over unchanged.

## Risks and open questions

The largest risk is Plex itself: everything depends on official apps accepting a server Plex did not build.

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Plex changes its API, client checks or terms | Ether stops working, or accounts are flagged | Spike first; track client releases; Flutter clients as fallback |
| No `*.plex.direct` certificate for a third-party server | Clients refuse or degrade the connection | Answer in the spike before other work |
| Apple TV app rejects Ether's transcode decisions | Pass bar fails on the pickiest client | Mirror real PMS responses byte for byte; test early |
| Plex Pass gating blocks remote users | Sharing is limited to LAN or Pass holders | Measure in the spike; document clearly |
| Article takedowns | Releases die mid-stream | Health checks, backfill providers, failover by timestamp |
| Indexer API limits | Searches fail at peak | Result caching, daily budgets, lazy checks only |
| Unreleased titles shown in the UI | Users press play on titles that cannot play | Fail fast with a clear "not available yet" message |
| Bundled TMDB key abused or revoked | Metadata stops for all installs | Owner override; rate limiting per install |
| Legal exposure | Takedown or pressure on the project | No hosted service, no bundled content or providers; owner supplies all accounts |

**Open questions**

- [ ] Can Ether get a `*.plex.direct` certificate, and if not, how do clients behave over plain HTTP or a self-signed certificate?
- [ ] What exactly do the official apps enforce for Plex Pass against a third-party server?
- [ ] How should a title be presented before its release is resolved: estimated media info, or a generic Media entry?
- [ ] What popularity threshold gives a useful browsable catalogue: roughly how many titles?
- [ ] How should failover align timestamps between releases with different intros or cuts?
