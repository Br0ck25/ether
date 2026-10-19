# Contributing to Ether

Thanks for helping. Ether is GPLv3; by contributing you agree your work
is licensed under it.

## Ground rules

- **Read [AGENTS.md](AGENTS.md) and [docs/design.md](docs/design.md)
  first.** AGENTS.md is binding: package ownership, pure-Go builds
  (`CGO_ENABLED=0` must pass), additive-only changes to `internal/core`
  and `internal/config`, stdlib tests, `log/slog`, chi routing.
- **Stay in your package(s).** Components talk through `internal/core`
  interfaces. If you need a contract change, keep it additive and call
  it out in the PR description.
- **No content, no credentials.** Ether ships none and never will. No
  hard-coded provider/indexer/account details anywhere, including tests
  and fixtures.
- **Write our own code.** Reference projects (e.g. AltMount, MIT) are for
  concepts; adapted snippets keep their copyright line and land in
  [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Workflow

1. Branch from `main`; one work package/feature per PR.
2. `gofmt -l .` clean, then:
   `go build ./... && go vet ./... && go test -race ./...`
   (Windows/macOS/Linux run in CI; make sure cross-compiles work:
   `GOOS=windows go build ./...` etc. when you touch OS-specific code.)
3. Every package gets real offline unit tests — fakes/`httptest`, never
   live services, never the network.
4. Constructors take explicit dependencies; no globals beyond migration
   registration (`store.Register`). No new third-party modules unless
   clearly worth it, and only permissive/GPL-compatible licences — note
   them in THIRD_PARTY_NOTICES.md.
5. Commit messages: short imperative subject, body explains *why*.
   Mention any `core`/`config` change explicitly.

## Reporting bugs

Include: Ether version (`ether --version` output or the web UI footer),
OS/install type, Plex client + version, what you pressed, what happened,
and the relevant log lines (`journalctl -u ether`,
`~/Library/Logs/Ether`, or the Docker logs). For playback issues the
release name and the resolver/health log lines matter more than a
screenshot.

## Asking for features

Check [docs/design.md](docs/design.md) — if it contradicts the design,
make the case there first (an issue labelled `design`).
