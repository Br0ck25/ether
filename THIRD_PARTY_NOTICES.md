# Third-party notices

Ether is GPLv3 (see [LICENSE](LICENSE)). The components below are used
under their own licences, all of which are GPL-compatible or (FFmpeg)
GPL itself. Full licence texts ship with each upstream project; for Go
modules the exact versions are pinned in [go.mod](go.mod)/[go.sum](go.sum)
and their `LICENSE` files are in the module sources.

## Bundled binary: FFmpeg

Installers and the Docker image bundle `ffmpeg` and `ffprobe` from the
**jellyfin-ffmpeg** portable builds (pinned in
[`packaging/ffmpeg.lock.json`](packaging/ffmpeg.lock.json), currently
`8.1.3-1`), which are GPL builds of [FFmpeg](https://ffmpeg.org/).

- FFmpeg is © the FFmpeg developers, licensed under the GNU General
  Public License v2-or-later (the bundled build enables GPL components
  such as libx264/libx265, so the combined binary is GPL).
- Build scripts and corresponding source:
  <https://github.com/jellyfin/jellyfin-ffmpeg> and
  <https://ffmpeg.org/download.html>. This notice plus the Ether source
  repository constitute the source offer for these binaries.
- Ether is GPLv3, so distributing a GPL FFmpeg alongside it is intended
  and compatible. If you build Ether yourself you may equally point it
  at your system's ffmpeg (`transcode.ffmpegPath` in the config).

## Go modules (direct dependencies)

| Module | Used for | Licence |
| --- | --- | --- |
| `modernc.org/sqlite` (+ its modernc.org support modules) | Pure-Go SQLite store | BSD-3-Clause (module); SQLite itself is public domain |
| `github.com/kardianos/service` | OS service (systemd/launchd/Windows SCM), `internal/service` | Zlib-style (“as-is”, © Daniel Theophanes) |
| `github.com/go-chi/chi/v5` | HTTP routing (wired by other packages) | MIT |
| `fyne.io/systray` | Tray/menu-bar icon, `cmd/ether-tray` | Apache-2.0 |
| `github.com/godbus/dbus/v5` | Linux StatusNotifier (systray transport) | BSD-2-Clause |
| `github.com/google/uuid` | IDs (via modernc / components) | BSD-3-Clause |
| `github.com/dustin/go-humanize` | Human-readable sizes (indirect) | MIT |
| `github.com/mattn/go-isatty`, `github.com/ncruces/go-strftime`, `github.com/remyoudompheng/bigfft`, `github.com/hashicorp/golang-lru/v2` | modernc/sqlite internals (indirect) | MIT / MIT / BSD-3-Clause / MPL-2.0 respectively |
| `golang.org/x/sys` (and other `golang.org/x/*` in the build graph) | Platform primitives (indirect) | BSD-3-Clause |

Other work packages may add modules; they must extend this table in the
same PR (see CONTRIBUTING.md). Regenerate the ground truth any time
with `go list -m all` and each module's `LICENSE` file.

## AltMount (reference only)

Ether was designed with [AltMount](https://github.com/javi11/altmount)
as a reference for Usenet-streaming concepts (provider pooling, NZB
segment indexing). Ether's code is its own.

> MIT License — Copyright (c) 2025 Javier Blanco

If any AltMount snippet is ever adapted verbatim, the specific files
must be listed in this section with their copyright line kept in the
source, per AGENTS.md. As of this revision, no AltMount code is
vendored. (Design doc: "AltMount's MIT-licensed code is vendored with
its notice kept" — this section is that notice's home when/if vendoring
lands in the stream-engine work package.)

## Data and API attributions

- Metadata and artwork come from **TMDB**. This product uses the TMDB
  API but is not endorsed or certified by TMDB.
- Watch sync (optional): **Trakt**. Subtitles (optional):
  **OpenSubtitles**. Both are off until the owner configures accounts.
