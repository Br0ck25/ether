# Ether

Ether is an open-source server that presents itself to Plex apps as a
Plex Media Server, but holds no media. Its library is a filtered TMDB
catalogue; when someone presses play, Ether finds a release on Usenet
and streams it in about five seconds.

You use the official Plex apps you already have. Ether answers as the
server, and Plex thinks it is talking to PMS.

See [docs/design.md](docs/design.md) for the design.

## Features

- **Plex apps, unchanged** — Plex Web, iOS, Apple TV and friends talk to
  Ether as if it were Plex Media Server (libraries, hubs, Continue
  Watching, search, watch state, shared users via plex.tv).
- **A TMDB catalogue as the library** — popular/trending/top-rated rows,
  franchise collections, smart rules; search reaches all of TMDB.
- **Streaming from Usenet on play** — release search and ranking over
  your Newznab indexers, health checks (NNTP STAT sampling), NZB/RAR/7z
  unwrapping, byte-range streaming, backfill providers, failover to the
  next release mid-stream, and an LRU segment cache so resumes and
  re-watches are instant.
- **Quality profiles per client** — pick the best release the device can
  actually play (4K/HDR/Dolby Vision, codec and bitrate aware); bundled
  ffmpeg remuxes or transcodes (QuickSync/NVENC/VAAPI/VideoToolbox, CPU
  fallback) only when it must.
- **Prefetch** — next-episode and resume points kept warm at background
  priority.
- **Per-user everything** — stream limits, content-rating caps,
  subtitle/audio preferences, playlists, Trakt sync, OpenSubtitles,
  optional LLM-curated rows.
- **Installs like PMS** — one installer, background service, tray icon,
  browser setup wizard, signed auto-updates.

## Install

Per-platform instructions (Windows setup.exe, macOS DMG, Linux
`.deb`/`.rpm`, Docker) are in [docs/install.md](docs/install.md). Short
version: install, open <http://localhost:32400/ether/>, sign in to
plex.tv, add your Usenet provider(s) and indexer(s), done.

## Status

Ether is under active initial development. The gating milestone is the
**spike**: a minimal server official Plex apps accept end-to-end (the
design doc has the pass bar and the fallback plan). Component status is
tracked in the design doc and issues; expect rough edges until the
spike passes. Current tagged builds, once they exist, come from the
release workflow described in [docs/development.md](docs/development.md).

## Develop

```sh
make build && make test && make ffmpeg   # see docs/development.md
```

[CONTRIBUTING.md](CONTRIBUTING.md) has the rules (package ownership,
pure-Go builds, offline tests).

## Legal

Ether ships no content, no Usenet providers and no indexers. Owners supply
every account and are responsible for what they access. See
[LEGAL.md](LEGAL.md).

Licensed under the GNU General Public License v3.0. See [LICENSE](LICENSE).
Third-party attributions: [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
