# Packaging

| Path | What |
| --- | --- |
| [../.goreleaser.yaml](../.goreleaser.yaml) | Cross-platform `ether` (+ win/linux tray) binaries, archives, checksums, `.deb`/`.rpm`, Docker image |
| [../Dockerfile](../Dockerfile), [docker/Dockerfile.goreleaser](docker/Dockerfile.goreleaser) | From-source and goreleaser Docker images |
| [linux/](linux/) | systemd unit (dedicated `ether` user, `/var/lib/ether`), deb/rpm maintainer scripts |
| [macos/](macos/) | `Ether.app` bundle (menu-bar tray + server + ffmpeg in Resources), DMG, codesign/notarise |
| [windows/](windows/) | NSIS installer (service, tray autostart, firewall), signtool signing |
| [ffmpeg.lock.json](ffmpeg.lock.json) | Pinned jellyfin-ffmpeg builds (version + SHA-256) staged by `scripts/fetch-ffmpeg.sh` |

Release flow is [`.github/workflows/release.yml`](../.github/workflows/release.yml):
tag → fetch ffmpeg → goreleaser → macOS DMG (sign/notarise) → Windows NSIS
(sign) → build + sign the update-feed manifests with
`cmd/ether-release-sign` → one GitHub release with everything attached.

Version/keys are injected with `-ldflags -X` (see `.goreleaser.yaml`):
`internal/updater.Version|Commit|Date|PublicKeyB64` and
`internal/tmdb.BundledAPIKey` (the tmdb package's bundled-key variable;
silently ignored until that package names it so).
