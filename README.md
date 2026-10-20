<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/ether-logo-dark.svg" />
    <img src=".github/assets/ether-logo.svg" alt="Ether" width="360" />
  </picture>
</p>

<h3 align="center">The whole movie and TV catalogue, inside the Plex app your family already uses.</h3>

<p align="center">
  Press Play on any title. Ether finds a good release on <b>your Usenet provider</b> or <b>your debrid account</b><br />
  and streams it straight to the TV. Nothing to download first, nothing to request, nothing to clean up later.
</p>

<p align="center">
  <a href="https://github.com/braedonsaunders/ether/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/braedonsaunders/ether?label=release&color=1f6feb" /></a>
  <img alt="Platforms" src="https://img.shields.io/badge/runs%20on-macOS%20%7C%20Windows%20%7C%20Linux%20%7C%20Docker-555" />
  <a href="LICENSE"><img alt="GPL-3.0" src="https://img.shields.io/badge/license-GPL--3.0-555" /></a>
  <img alt="No telemetry" src="https://img.shields.io/badge/telemetry-none-2ea043" />
</p>

<p align="center">
  <a href="#install-in-five-minutes">Install</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#what-you-can-stop-running">What it replaces</a> ·
  <a href="#questions-youre-about-to-ask">FAQ</a> ·
  <a href="#where-it-stands-today">Status</a>
</p>

<!-- TODO: hero screenshot or 20s GIF: Apple TV Home rows → detail page → Play → Skip Intro. -->

---

Ether is a self-hosted server that the official Plex apps see as a normal Plex Media Server. There's no library folder behind it. Instead it shows the full TMDB catalogue, and when someone presses **Play** it searches *your* indexers and streams a release from *your* Usenet provider or debrid account (**Real-Debrid, TorBox, AllDebrid, Premiumize**), or from both. Seeking works too.

It runs as one process, set up in a web page. Native installers for macOS, Windows and Linux, plus a Docker image. No YAML, no FUSE, no rclone mounts, no 50 TB array.

| What your family sees | What you stop doing |
| :--- | :--- |
| The same Plex app on the same TV, with every title already there | Running Sonarr, Radarr, Prowlarr, SABnzbd and a mount, and keeping them all in sync |
| A Home screen that learns what *each* of them likes | Answering "can you add…?" texts and approving requests |
| Skip Intro and Skip Credits, subtitles in their language | Buying drives, then running cleanup tools to free space |
| A link from you, a Plex sign-in, and they're watching | Being tech support for a separate request portal |

---

## How it works

```
  Plex app (TV, phone, web)
        │  browse · search · Play
        ▼
  ┌──────────── Ether ─────────────────────────────────────────┐
  │ catalogue    TMDB mirror, artwork, collections, your rows   │
  │ resolver     searches your indexers, ranks by your profile  │
  │ health       checks articles / debrid cache before Play     │
  │ stream       byte ranges on demand: NNTP or HTTPS           │
  │ transcode    direct play, remux, or HW transcode (FFmpeg)   │
  └─────────────────────────────────────────────────────────────┘
        │                                   │
   your Usenet providers             your debrid accounts
   + Newznab indexers                + Torznab / built-in sites
```

1. **Browse.** Ether serves TMDB metadata, artwork, cast, collections and franchises in the shapes the Plex apps expect, so detail pages, posters and backdrops look like any other Plex library.
2. **Press Play.** Ether searches your indexers, ranks the results against your quality profile (4K, 1080p, 720p, custom formats), and checks that the release is really there. For Usenet it samples articles across providers. For debrid it uses only torrents your service already has cached.
3. **Watch.** Ether reads only the bytes the player asks for. Usenet posts are mapped straight to article ranges, including store-mode RAR4, RAR5 and 7z sets, with no extraction step. Debrid files stream over HTTPS range requests. Seeking jumps to the right keyframe and fetches just those bytes.
4. **If something breaks mid-movie**, Ether tries your other providers first. If that fails, it switches to the next good release at the same timestamp and carries on.

Already-watched and in-progress titles sit in a self-trimming disk cache (50 GiB by default). Resuming or rewatching doesn't fetch the same bytes again, and the next episode warms up while the credits roll.

---

## What you can stop running

Ether isn't a frontend over your existing stack. It replaces most of it with one process. Credit where it's due: these tools showed what people want, and several are why Ether exists.

| You run this today | In Ether |
| :--- | :--- |
| **Sonarr / Radarr** | Nothing to grab ahead of time. Quality profiles and custom formats apply the moment someone presses Play. |
| **Prowlarr** | Newznab and Torznab indexers built in, plus Cardigann-format site definitions you install yourself. Imports your Prowlarr indexers in one click. |
| **SABnzbd / NZBGet + rclone**, NzbDAV, AltMount | Usenet streaming built in. No download folder, no mount. |
| **zurg + rclone**, Decypharr, Riven, plex_debrid | Debrid streaming built in. No WebDAV mount, no symlink farm. |
| **Overseerr / Jellyseerr** | No request queue. Everything is already browsable. Optionally reads viewers' Plex and Trakt watchlists and tells them when a title is ready. |
| **Kometa** | Collections, franchises, rotating rows and poster overlays (4K, HDR, DV, Atmos, IMDb, RT, Letterboxd), all built in the web UI. |
| **Tautulli + Notifiarr** | Now playing, history, stats, provider usage. Alerts to Discord, Slack, Telegram, email, ntfy, Gotify and webhooks. |
| **Bazarr** | Missing subtitles fetched from OpenSubtitles and auto-synced to the release that's playing. |
| **Maintainerr / storage cleanup** | Nothing to delete. The cache evicts itself. |
| **ErsatzTV / Tunarr / Threadfin** | 24/7 virtual channels from your collections, plus M3U/XMLTV and Xtream sources, in Plex's own Live TV guide. |

**Keep your Plex Media Server for media you own on disk.** Ether doesn't index local files. Both servers sit side by side in the same Plex app.

---

## What's inside

<details open>
<summary><b>Sources: Usenet, debrid, or both</b></summary>

- **Usenet:** multiple providers with priorities and **backfill/block accounts** used only for missing articles. Warm connection pools, a circuit breaker per provider, and health checks that read real article bodies mid-file, not just `STAT`. PAR2 is used to name obfuscated posts and verify them, never for slow repair.
- **Debrid:** Real-Debrid, TorBox, AllDebrid and Premiumize. Cached torrents only (uncached ones are removed again), and only the episode's file from a season pack. Read-ahead adapts to your connection.
- **Both:** set a preference, or leave it on `auto`. Ether never trades quality for speed. Within the best quality tier, whichever source has measurably started faster for you goes first, and the other stays ready as a failover.
- **Indexers:** Newznab, Torznab, and built-in sites from Cardigann YAML definitions you supply (FlareSolverr optional). Search results are cached, and per-indexer daily API and grab caps are respected.
- **Release control:** pick, pin or block a release per title, see why each candidate won or lost, and choose from versions (4K HDR, 1080p high compatibility, Extended Cut) in Plex's own **Play Version** menu.

</details>

<details open>
<summary><b>Playback that behaves like a real Plex server</b></summary>

- Direct play, copy-only remux, or transcode to HLS / DASH, with **hardware encoding** on NVIDIA (NVENC), Intel (Quick Sync), VAAPI and Apple VideoToolbox. Each encoder is test-run first, with automatic fallback to CPU. FFmpeg is bundled.
- **Skip Intro and Skip Credits** from chapter marks and TheIntroDB, aligned to the exact release being played. Credits markers stop before mid- and post-credits scenes.
- Scrubbing thumbnails, Dolby Vision handling for Apple TV, per-viewer audio and subtitle language preferences, and offline downloads through the Plex app's own Download button.
- Remote streaming over `*.plex.direct` certificates, UPnP/NAT-PMP, or your own reverse proxy and domain.

</details>

<details open>
<summary><b>A Home screen that's different for each person</b></summary>

- Each viewer gets a taste profile learned from what they finish, rewatch, abandon and rate, not just what they clicked.
- Rows such as *Because you watched*, *Hidden gems*, *Finish the franchise*, *Short on time* and *Top 10*, re-ranked per viewer, device and time of day. Weeknight phone sessions get shorter picks; weekend TV evenings get the epics. No single franchise floods a row.
- **Home Designer:** pin, hide, schedule or limit rows for everyone or one person, with a live preview.
- **Search the way people talk:** "90s thrillers under 100 minutes" or "cozy rainy-day sci-fi" in the normal Plex search bar. This runs locally with no AI. An LLM (Ollama, LM Studio or any OpenAI-compatible endpoint) is **optional and off by default**.

</details>

<details open>
<summary><b>Sharing without the tech-support calls</b></summary>

- **Per-person rules:** stream limits, max content rating, blocked genres or keywords, bitrate caps, allowed hours, remote access on or off. One-click kids presets by age. Rules apply to every request, including artwork and search.
- Plex Home users and PINs work as normal. Revoking a share in Plex removes that person from Ether.
- **Moving over:** import watch state, history, ratings, collections and playlists from an existing Plex Media Server (read-only on the old server), plus Letterboxd and Trakt CSVs.

</details>

<details>
<summary><b>For the homelab crowd</b></summary>

- Pure Go, no CGO. Data lives in SQLite with scheduled integrity-checked backups (daily, keep 7).
- Optional **high availability**: two or more nodes on your own PostgreSQL show up in Plex as one server, with leader election and rolling updates.
- Signed (ed25519) updates on stable and beta channels. Auto-update is off by default.
- Community templates for Unraid, TrueNAS SCALE, Proxmox and Synology in [`packaging/community`](packaging/community/README.md).

</details>

---

## Install in five minutes

| Platform | Get it | Notes |
| :--- | :--- | :--- |
| **macOS 13+** | [`Ether-<version>.dmg`](https://github.com/braedonsaunders/ether/releases/latest) | Menu-bar app, Apple Silicon and Intel. Not notarised yet: right-click → Open the first time. |
| **Windows** (x64) | [`EtherSetup-<version>.exe`](https://github.com/braedonsaunders/ether/releases/latest) | Runs as a Windows service with a tray icon. Unsigned for now, so SmartScreen will ask. |
| **Linux** | [`.deb` / `.rpm`](https://github.com/braedonsaunders/ether/releases/latest) (amd64, arm64) | systemd service, runs as its own `ether` user. |
| **Docker** | `ghcr.io/braedonsaunders/ether` (amd64, arm64) | See below. Unraid/TrueNAS/Proxmox templates in [`packaging/community`](packaging/community/README.md). |

```yaml
services:
  ether:
    image: ghcr.io/braedonsaunders/ether:latest
    container_name: ether
    restart: unless-stopped
    network_mode: host            # needed for Plex apps to discover it on your LAN
    environment:
      TZ: Etc/UTC
    volumes:
      - ./config:/config
      - ./cache:/cache            # choose /cache as the cache folder during setup
    # devices: ["/dev/dri:/dev/dri"]   # Intel/AMD hardware transcoding
```

Then open **http://localhost:32400/ether/** and follow the setup wizard:

1. **Sign in with Plex** and claim the server. It shows up in your Plex apps right away.
2. **Add a source:** a Usenet provider plus a Newznab indexer, or skip those steps and add a debrid account under *Sources › Debrid* afterwards.
3. **Pick a cache folder and size.** An SSD is best; 50 GiB is plenty to start.
4. **Paste a free TMDB API key**, plus any optional extras (Trakt, OpenSubtitles, a local LLM).
5. **Share it** from Plex as usual.

**You'll need:** a Plex account, a [TMDB API key](https://www.themoviedb.org/settings/api) (free), and at least one Usenet provider + indexer *or* one debrid account + torrent indexer. Ether ships with none of these. For hardware, 2 CPU cores and 2 GB RAM is a reasonable start if your clients direct-play.

Full guides: [Installation](docs/install.md) · [User guide](docs/user-guide.md) · [How it's built (spec)](docs/design.md)

---

## Questions you're about to ask

<details>
<summary><b>Do I need Plex Pass?</b></summary>

At home, no. **To stream outside your home, yes.** Ether follows Plex's rules exactly as a real Plex Media Server does. Remote playback needs Plex Pass on the server owner's account or a Remote Watch Pass on the viewer's account, and there's no remote streaming without one.

Ether doesn't emulate, unlock or work around Plex Pass or any other paid Plex feature. Every viewer signs in with their own Plex account, sharing goes through plex.tv, and each account gets exactly the entitlements Plex has given it. Ether doesn't sell or provide Plex Pass. If you use the Plex apps, support Plex.
</details>

<details>
<summary><b>Which Plex apps work?</b></summary>

All of the native Plex apps: Apple TV, iPhone and iPad, Android and Android TV, Fire TV, Roku, smart TVs, consoles, and Plex Web in the browser. To the app, Ether is just another Plex server on your account, so there's nothing to install on the TV and nothing new for anyone to learn.
</details>

<details>
<summary><b>Does it download whole movies? How much disk does it use?</b></summary>

No. It fetches the byte ranges being watched, plus a bit of read-ahead, into a bounded cache that you size (50 GiB by default; a 20 Mbit/s stream uses about 150 MB a minute). Old data is evicted on its own. Nothing is ever offered as a file for keeping.
</details>

<details>
<summary><b>Won't this hammer my indexers and Usenet provider?</b></summary>

It's built not to. Indexers are searched when someone opens or plays a title, or when the next episode is coming up. If you turn on watchlist checks, watchlisted titles are re-checked every few hours (every 6 by default). Ether never crawls the catalogue. Results are cached, and per-indexer daily API and grab caps are respected. Health checks sample about a dozen articles, not the whole file. Resumes and rewatches come from the local cache. Connection counts are capped per provider, and backfill accounts are used only for articles your main provider is missing.

Some indexers forbid streaming use in their rules. **Read your indexer's terms.** Ether doesn't override them for you.
</details>

<details>
<summary><b>Will my debrid account get flagged for multiple IPs?</b></summary>

Every stream is fetched by your Ether server and relayed to the viewer. Your debrid service sees one IP, the server's, no matter where your family is watching from. If you run an HA cluster, keep the nodes behind one public IP.
</details>

<details>
<summary><b>Can I run it next to my existing Plex server?</b></summary>

Yes, and that's the intended setup if you have media on disk. Run them on different machines, or give Ether a different port (both default to 32400). Each shows up as its own server in the Plex app.
</details>

<details>
<summary><b>What does it phone home to?</b></summary>

There's no telemetry or analytics. Ether only talks to plex.tv (sign-in, claiming, sharing), TMDB, the providers, indexers and accounts *you* configure, any optional services you turn on, and GitHub's release feed when checking for updates. Viewer sign-in goes through plex.tv, the same as with a real Plex server.
</details>

<details>
<summary><b>Was this written with AI?</b></summary>

Yes, and here's how. Ether has one maintainer, who writes the spec ([`docs/design.md`](docs/design.md)) and directs AI coding agents (mostly Claude) that write much of the code, and the commits say so. What keeps it honest: about 1,600 unit tests that run offline against fake providers, indexers and debrid APIs. Protocol responses are diffed against recordings from a real Plex Media Server, and playback is checked on real Plex clients. You can judge it yourself: the whole repo is GPL-3.0.
</details>

---

## Where it stands today

Ether is **early (v0.2)**. It works, and it hasn't yet been battle-tested on thousands of servers.

| Area | Status |
| :--- | :--- |
| Catalogue, browse, search, Home rows, collections | Working |
| Usenet streaming, health checks, failover | Working |
| Native Plex apps (TV, mobile, web) | Working |
| Debrid (Real-Debrid, TorBox, AllDebrid, Premiumize) | **Experimental.** Built from the public API docs and tested against simulated services, not yet against live accounts |
| Live TV, virtual channels | Working, less mileage |
| HA cluster on PostgreSQL | Advanced, for people who already run PostgreSQL |

**Known limits:**
- Usenet archives must be store-mode; compressed or encrypted RARs are skipped.
- Torrents with the video inside RARs are skipped.
- No Plex Relay, so remote viewers need a direct connection (UPnP, a port forward or a reverse proxy).
- No local-file libraries; use Plex Media Server for those.
- No DVR.
- Cardigann sites that need a captcha to sign in aren't supported.

Found a bug? Open an issue with your Ether version, OS, the Plex app and version, what you did, and what happened. Remove credentials from logs first.

---

## Legal

Ether is an independent, clean-room implementation of the protocol the Plex apps use to talk to a media server. It contains no Plex code. It ships **no media, no Usenet providers, no indexers, no site definitions, no debrid accounts and no API keys**. You bring your own, and you're responsible for what you connect it to and for following the law where you live and the terms of the services you use. See [LEGAL.md](LEGAL.md).

"Plex" is a trademark of Plex, Inc. Ether is **not affiliated with, sponsored by or endorsed by Plex, Inc.** This product uses the TMDB API but is not endorsed or certified by TMDB.

[GPL-3.0](LICENSE) · [Third-party notices](THIRD_PARTY_NOTICES.md)
