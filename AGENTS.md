# Ether — agent conventions

Ether is a Go server that presents itself to official Plex apps as a Plex Media
Server, backed by a TMDB catalogue and on-demand Usenet streaming. Read
`docs/design.md` first; it is the spec.

## Layout

| Path | Owner / purpose |
| --- | --- |
| `internal/core` | Domain types and cross-component interfaces. **Contract.** Edits must be additive; do not rename or remove fields. |
| `internal/config` | Owner settings (`config.json`), defaults. Add fields additively. |
| `internal/store` | SQLite (modernc.org/sqlite, pure Go). Components register their own migrations with `store.Register("<component>", n, sql)`; prefix table names with the component. |
| `internal/usenet` | NNTP connection pools, multi-provider priority/backfill, yEnc. Implements `core.ArticleFetcher`. |
| `internal/nzb`, `internal/stream`, `internal/archive`, `internal/segcache` | NZB parsing, byte-offset index, RAR/7z store-mode, segment disk cache. Implements `core.Engine`, `core.SegmentCache`. |
| `internal/newznab`, `internal/resolver`, `internal/releaseparse` | Indexers with budgets + result cache, release name parsing, ranking, health checks. Implements `core.Indexer`, `core.Resolver`. |
| `internal/tmdb`, `internal/catalog`, `internal/collections` | TMDB client, local mirror, browse/search, franchises, smart rules. Implements `core.Catalog`, `core.Collections`. |
| `internal/state`, `internal/recommend`, `internal/trakt`, `internal/llm`, `internal/subtitles` | Watch state, playlists, recommendations, Trakt, LLM rows, OpenSubtitles. Implements `core.State`, `core.Playlists`, `core.Recommender`. |
| `internal/plex` | The PMS HTTP API that official Plex apps talk to. |
| `internal/plextv`, `internal/auth`, `internal/remote` | plex.tv sign-in/claim, resources publishing, shared users, token auth, UPnP, plex.direct certs. Implements `core.Auth`. |
| `internal/transcode` | Bundled ffmpeg discovery, HW detection, probe, decision, HLS sessions. Implements `core.Transcoder`. |
| `internal/playback`, `internal/prefetch` | Session orchestration, stream limits, failover, prefetch. Implements `core.Playback`. |
| `internal/admin`, `web/` | Setup wizard + admin JSON API, React/Vite SPA embedded with `go:embed`. |
| `internal/updater`, `internal/service`, `cmd/ether-tray`, `packaging/` | Updates, OS service, tray app, installers, CI. |
| `internal/app`, `cmd/ether` | Wiring. Owned by the orchestrator. |

## Rules

- Go 1.27, module `github.com/ether-media/ether`. Logging: `log/slog`. Routing: `github.com/go-chi/chi/v5`. Tests: stdlib `testing` + `net/http/httptest` (no testify).
- Pure Go only (CGO_ENABLED=0 must build) so we can cross-compile installers. No FUSE.
- Stay inside the packages you own. If you need a change to `internal/core` or `internal/config`, make it additive and minimal and mention it in your report.
- Every package gets real unit tests that run offline: use fakes/httptest servers, never live services. `go build ./... && go vet ./... && go test ./...` must pass before you commit.
- Constructors take explicit dependencies (interfaces from `core`, `*sql.DB`, `config` snapshots, `*slog.Logger`); no globals beyond migration registration.
- Write our own code. `~/Documents/.ether-ref/altmount` (MIT) is a reference for concepts only; if you adapt any snippet, keep its copyright line in a comment and add it to `THIRD_PARTY_NOTICES.md`.
- Commit on your branch with clear messages. Do not push.
- Ether ships no content and no providers; never hard-code real provider, indexer or account credentials.
