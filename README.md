<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/ether-logo-dark.svg" />
    <img src=".github/assets/ether-logo.svg" alt="Ether" width="440" />
  </picture>
</p>

<p align="center">
  <strong>Your Plex. Beyond your library.</strong><br />
  One binary that speaks native Plex Media Server—backed by a live TMDB catalogue, zero-download Usenet streaming, and a Home screen that learns every viewer's taste.
</p>

---

Stop babysitting a nine-container stack of scanners, symlink farms, FUSE mounts, request portals, and YAML overlay scripts.

**Ether** is a self-hosted media server written in pure Go (`CGO_ENABLED=0`) that presents itself directly to official Plex apps—Apple TV, iOS, Android TV, Smart TVs, and Plex Web—as a native Plex Media Server. Instead of indexing files on a hard drive, Ether serves the released movie and television catalogue from TMDB, shapes every row around the individual viewer, and resolves and streams titles on demand the instant someone presses **Play**.

No external request app. No waiting for a download queue or library scan. Open Plex, search or browse anything released, and play.

---

## Why Ether Feels Different

### 1. The Whole Catalogue Is Already in Plex
Traditional setups force viewers out of Plex into a separate request portal, wait for a downloader to finish, and trigger a filesystem scan before a title even exists. In Ether, released movies, shows, seasons, episodes, and franchises are natively browseable and searchable inside Plex from day one—complete with verified `plex://` cloud identities, `UltraBlurColors`, clearLogos, cast/crew, and optional IMDb, Rotten Tomatoes, Metacritic, and Letterboxd poster badges.

### 2. Zero-FUSE, Zero-Download Streaming Engine
When a viewer hits **Play**, Ether queries your Newznab/Prowlarr indexers, ranks candidates against your quality profiles and custom formats, verifies article health over NNTP (including mid-file `BODY` yEnc probes), and opens the stream in seconds:
- **Direct byte-offset indexing across Usenet:** Maps byte ranges straight to NNTP article IDs across plain posts and store-mode **RAR4, RAR5, and 7z** multi-volume archives without extracting to disk or mounting FUSE.
- **Instant 4K seeking:** Jumping to `01:00:00` reads the container's MP4 `stss`/`sidx` or Matroska `Cues` keyframe index and fetches only the exact articles for that timestamp over warm, priority-scheduled NNTP connection pools.
- **Self-healing mid-stream failover:** If articles are missing mid-movie, Ether automatically resolves an alternate release, aligns the playhead by timestamp (accounting for keyframe cues, subtitle dialogue timing, and 25 fps PAL speedup), and transitions the stream without dropping the viewer's session.

### 3. A Home Screen That Actually Learns Each Viewer
Real Plex servers show the same static shelves to everyone. Ether runs a Netflix-style contextual personalization engine behind `/hubs`:
- **Per-viewer taste models:** Learns signed affinities across genres, keywords, directors, actors, decades, franchises, and languages from completions, rewatches, early abandons, ratings, Trakt history, and impression fatigue.
- **Contextual bandit row ranking:** Uses Thompson sampling blended with device class, time of day, and weekday/weekend session habits (*Short on Time* lifts on weeknight phones; *4K Blockbusters* and *Finish the Franchise* lift on weekend evening TVs) with Maximal Marginal Relevance (MMR) diversity so one franchise never floods a row.
- **Home Designer & Collection Studio:** Pin, schedule, or restrict rows globally or per viewer with live preview, and build rule-driven or Trakt/Letterboxd/MDBList-backed collections that mix movies and TV in a single shelf.

### 4. Built for Every Device and Player Quirk
- **Play Version picker:** Detail pages expose distinct release tiers—*Automatic*, *4K Dolby Vision / HDR10*, *1080p High Compatibility*, or *Director's / Extended Cut*—right inside Plex's native **Play Version** menu.
- **Descriptive & natural search:** Search *"90s thrillers under 100 mins"* or *"cozy rainy-day sci-fi"* inside Plex's native search bar; Ether returns matching titles plus an instant generated collection you can save by rating it.
- **Adaptive HLS & MPEG-DASH transcoding:** Hardware-accelerated encoding (`VideoToolbox`, `NVENC`, `QuickSync`, `VAAPI`), on-demand ABR ladders, goodput-steered VBV reservoirs, and a VBR spike governor for copy streams.
- **Skip Intro, Skip Credits & Trickplay:** Container chapters, TheIntroDB lookup, pure-Go Goertzel acoustic intro matching, piecewise subtitle auto-sync, and progressive `.bif` scrubbing thumbnails generated on the fly.
- **Cinema Trailers & pre-warming:** While a trailer plays—or as an episode nears its credits—Ether resolves and pre-warms the feature or next episode in the background so playback starts in sub-second time.
- **Live TV & Virtual Channels:** Assemble 24/7 virtual broadcast channels from your collections and genres, or proxy M3U playlists and XMLTV guides directly into Plex's native Live TV grid.

---

## One Binary vs. The Old Stack

| Capability | Traditional Stack | Ether |
| :--- | :--- | :--- |
| **Media Server & Client API** | Plex Media Server | Built in (`internal/plex`, `internal/plextv`) |
| **Movie & TV Automation** | Radarr + Sonarr | Built in (`internal/resolver`, on-demand at Play) |
| **Indexer Management** | Prowlarr | Built in (`internal/newznab`, automatic service-limit handling) |
| **Usenet Downloader & Mount** | SABnzbd / NZBGet / AltMount + rclone FUSE | Built in (`internal/usenet`, `internal/stream`, `internal/archive`) |
| **Discovery & Requests** | Overseerr / Jellyseerr | Built in (entire released TMDB catalogue is native in Plex) |
| **Collections & Poster Overlays** | Kometa (PMM) | Built in (`internal/collections`, `internal/overlays`) |
| **Personalized Home Rows** | *Not possible in PMS* | Built in (`internal/personalize` taste & bandit engine) |
| **Virtual Live TV Channels** | ErsatzTV / Tunarr / Threadfin | Built in (`internal/livetv`) |
| **Analytics & Notifications** | Tautulli + Notifiarr | Built in (`internal/stats`, `internal/notify`) |

---

## Quick Start

**Pre-release:** there are no published installer downloads yet. Packaging is configured for Windows, macOS, Linux, and Docker; published builds will appear on the [Releases page](https://github.com/braedonsaunders/ether/releases).

### Docker

```sh
docker run -d --name ether --restart unless-stopped \
  --network host \
  -v /path/to/config:/config \
  -v /path/to/cache:/cache \
  ghcr.io/braedonsaunders/ether:latest
```

### Build from Source

```sh
CGO_ENABLED=0 go build -o ether ./cmd/ether
./ether
```

Once Ether is running, open **<http://localhost:32400/ether/>** to sign in with your Plex account, add your Usenet providers and Newznab indexers, and share the server with viewers through Plex. Ether automatically acquires a `*.plex.direct` TLS certificate or publishes your custom HTTPS domain.

See the [Installation Guide](docs/install.md), the [User Guide](docs/user-guide.md), and the [Product & Architecture Specification](docs/design.md).

---

## Support

For a bug report, include your Ether version, operating system, Plex app and version, what you did, and what happened. Remove credentials and personal details from any logs you attach.

## Licence and Services

Ether ships no media, providers, indexers, or accounts. Owners supply their own services and are responsible for what they access. Ether is independent of Plex and TMDB. See [LEGAL.md](LEGAL.md).

Licensed under the [GNU General Public License v3.0](LICENSE).
[Third-party notices](THIRD_PARTY_NOTICES.md).

