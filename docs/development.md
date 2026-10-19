# Development

Read [AGENTS.md](../AGENTS.md) (binding conventions) and
[design.md](design.md) (the spec) first.

## Toolchain

- Go 1.27 (`go.mod` pins it). The server is **pure Go**:
  `CGO_ENABLED=0 go build ./...` must always pass — installers
  cross-compile. The only CGO binary is the macOS tray.
- Node + npm for `web/` (admin UI, built into the binary via go:embed).
- Optional release tooling: `goreleaser` v2, `shellcheck`, `llvm`
  (universal ffmpeg on Linux), NSIS (Windows installer), Xcode CLT (macOS
  app/DMG).

## Everyday commands

```sh
make build        # dist/ether, dist/ether-tray, dist/ether-release-sign
make test         # go test -race ./...
make lint         # gofmt + go vet + shellcheck
make web          # build web/ SPA
make ffmpeg       # stage pinned ffmpeg for the host (dist/ffmpeg/)
make run          # run the server from source
make tray         # tray (headless when CGO is off: logs status)
```

## Repo layout

`internal/core` is the contract between components; `internal/config` is
the owner settings schema. Everything else is one package per component
— see the table in AGENTS.md. This work package owns:

- `internal/service` — OS service registration (systemd/launchd/SCM via
  kardianos/service); `ether service install|start|stop|status`.
- `internal/updater` — ed25519-signed JSON release feed; `Check`,
  `Download` (sha256-verified), `Apply` (atomic binary replace with
  `.old` rollback, or launch an installer).
- `cmd/ether-tray` — systray app (status from `/identity` +
  `/ether/api/status`, service control, update checks, launch at login).
  GUI file is build-tagged (`windows || linux || (darwin && cgo)`); other
  builds run a headless status logger, so plain `go test` works
  everywhere.
- `cmd/ether-release-sign` — maintainer tool: `keygen`, `manifest`,
  `sign`, `verify` for the update feed.
- `packaging/`, `.goreleaser.yaml`, `Dockerfile`, `.github/workflows/`.

## The spike

The spike proves official Plex apps accept Ether before anything else is
relyed on (design doc §Spike plan):

1. `make build && make ffmpeg`, copy `dist/ffmpeg/<host>/*` next to
   `dist/ether` (transcode looks beside the binary first).
2. Point the config's `spike` section at a local MP4 (`spike.enabled`,
   `spike.localFiles`) in `$ETHER_DATA_DIR/config.json`, or use the setup
   wizard.
3. Run `./dist/ether`, claim the server in the wizard, and check Plex
   Web, iOS and Apple TV list the library and play with seeking.

Record the spike answers (plex.direct certificate, Plex Pass remote
behaviour, Apple TV transcode decisions) in the PR/issue that tracks it.

## Version injection

Release builds set these with `-ldflags -X` (see `.goreleaser.yaml`):

| Variable | Meaning |
| --- | --- |
| `internal/updater.Version` / `Commit` / `Date` | build version (`dev` otherwise) |
| `internal/updater.PublicKeyB64` | ed25519 public key for the update feed |
| `internal/tmdb.BundledAPIKey` | bundled TMDB key (owner key in config wins) |

Without a public key the updater refuses to run — dev builds never
self-update.

## Releasing

1. Generate keys once: `make keys` → CI secrets `ETHER_UPDATE_PRIVATE_KEY`
   (secret) and `ETHER_UPDATE_PUBLIC_KEY`.
2. Tag `vX.Y.Z` (stable) or `vX.Y.Z-beta.N` (beta channel). The release
   workflow builds everything, signs/notarises when secrets exist,
   publishes the signed `<channel>.json` feed on the release, and pushes
   the Docker image to GHCR.
3. Dry-run locally any time: `make release-snapshot`.

## ffmpeg pin

`packaging/ffmpeg.lock.json` pins jellyfin-ffmpeg portable builds
(version + SHA-256 per platform). `scripts/fetch-ffmpeg.sh` verifies
before staging; bump the version and hashes together (hashes are the
GitHub release asset digests).

## Tests

Offline only: fakes and `httptest` servers, stdlib `testing` (no
testify). Updater tests run a real signed feed over httptest; service
tests use a fake backend; tray logic (status parsing, autostart files,
update policy) is tested without a GUI.
