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
| `github.com/ulikunitz/xz` | 7z header LZMA decoding, `internal/archive` | BSD-3-Clause |
| `golang.org/x/text` | Legacy subtitle code pages, `internal/subtitles` | BSD-3-Clause |
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

## Owner web UI (npm packages bundled into `internal/admin/dist`)

The `/ether/` owner UI (`web/`) is built with Vite and embedded in the
binary. The bundle includes these packages (exact versions are pinned in
[`web/pnpm-lock.yaml`](web/pnpm-lock.yaml)):

| Package | Used for | Licence |
| --- | --- | --- |
| `@braedonsaunders/appkit-ui`, `@braedonsaunders/appkit-tokens`, `@braedonsaunders/appkit-dashboard` (primitives entry) | Design system: components, semantic tokens, dashboard tiles | AGPL-3.0-or-later |
| `react`, `react-dom`, `react-router`, `react-router-dom` | UI runtime and routing | MIT |
| `@dnd-kit/core`, `@dnd-kit/sortable`, `@dnd-kit/utilities` | Accessible drag-to-reorder | MIT |
| `lucide-react` | Icons | ISC |
| `framer-motion` (+ `motion-dom`, `motion-utils`) | AppKit interaction motion | MIT |
| `clsx`, `tailwind-merge` | Class composition (via AppKit) | MIT |
| `class-variance-authority` | Component variants (via AppKit) | Apache-2.0 |
| `tailwindcss` (build-time; generated CSS ships) | Utility CSS | MIT |

AppKit is AGPL-3.0-or-later. GPLv3 section 13 expressly permits combining
GPLv3 code with AGPLv3 code; the AppKit parts keep their AGPL terms,
including the obligation to offer source to users interacting with them
over a network. Ether's complete source, including `web/`, is public at
the repository above, which satisfies that offer.

## AltMount (reference only)

Ether was designed with [AltMount](https://github.com/javi11/altmount)
as a reference for Usenet-streaming concepts (provider pooling, NZB
segment indexing). Ether's code is its own.

> MIT License — Copyright (c) 2025 Javier Blanco

If any AltMount snippet is ever adapted verbatim, the specific files
must be listed in this section with their copyright line kept in the
source, per AGENTS.md. No AltMount code is vendored; the stream engine's tests use AltMount's
MIT-licensed WinRAR RAR5 fixture files (see below). (Design doc: "AltMount's MIT-licensed code is vendored with
its notice kept" — this section is that notice's home when/if vendoring
lands in the stream-engine work package.)

## Stream engine components

## github.com/ulikunitz/xz

- Used by: `internal/archive` (LZMA/LZMA2 decoding of encoded 7z headers)
- Licence: BSD-3-Clause
- URL: https://github.com/ulikunitz/xz
- Copyright (c) 2014-2022 Ulrich Kunitz. All rights reserved.

### AltMount test fixtures

- Used by: `internal/archive/testdata/altmount/` (test data only, not shipped):
  `archive.rar`, `archive.part01.rar`, `archive.part02.rar`, `compressed.rar`,
  `encrypted.rar`, `solid.rar`, `multi.part1.rar`, `multi.part2.rar`,
  `multi.part3.rar`, `payload_a.bin`, `payload_b.bin`
- Source: AltMount `internal/importer/testdata/`
- Licence: MIT
- URL: https://github.com/kipsilabs/altmount
- Copyright (c) 2025 Javier Blanco

MIT License text:

> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.

## Data and API attributions

- Metadata and artwork come from **TMDB**. This product uses the TMDB
  API but is not endorsed or certified by TMDB.
- Watch sync (optional): **Trakt**. Subtitles (optional):
  **OpenSubtitles**. Both are off until the owner configures accounts.
