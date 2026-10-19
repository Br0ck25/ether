# Ether — developer makefile. See docs/development.md.
GO ?= go
VERSION ?= dev
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -s -w \
	-X github.com/braedonsaunders/ether/internal/updater.Version=$(VERSION) \
	-X github.com/braedonsaunders/ether/internal/updater.Commit=$(COMMIT) \
	-X github.com/braedonsaunders/ether/internal/updater.PublicKeyB64=$(ETHER_UPDATE_PUBLIC_KEY) \
	-X github.com/braedonsaunders/ether/internal/tmdb.BundledAPIKey=$(ETHER_TMDB_KEY)

.PHONY: build test lint vet web spike run tray ffmpeg icons release-snapshot keys clean

## build: server + tray + release-sign tool (CGO-free server)
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/ether ./cmd/ether
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/ether-release-sign ./cmd/ether-release-sign
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/ether-tray ./cmd/ether-tray

## test: full suite with the race detector
test:
	$(GO) test -race ./...

## lint: gofmt + vet + shellcheck (golangci-lint if configured later)
lint: vet
	@test -z "$$($(GO)fmt -l .)" || { echo "gofmt needed:"; $(GO)fmt -l .; exit 1; }
	@command -v shellcheck >/dev/null && shellcheck scripts/*.sh packaging/linux/*.sh packaging/macos/*.sh || echo "shellcheck not installed — skipping"

vet:
	CGO_ENABLED=0 $(GO) vet ./...

## web: build the admin/setup SPA (embedded via go:embed)
web:
	@if [ -f web/package.json ]; then cd web && npm ci && npm run build; else echo "web/ not present yet"; fi

## spike: run the server in spike mode (local files library; see docs/development.md)
spike:
	CGO_ENABLED=0 $(GO) run ./cmd/ether

## run: run the server from source
run:
	CGO_ENABLED=0 $(GO) run ./cmd/ether

## tray: run the tray (headless under CGO_ENABLED=0)
tray:
	$(GO) run ./cmd/ether-tray

## ffmpeg: stage the pinned ffmpeg/ffprobe for the host platform
ffmpeg:
	scripts/fetch-ffmpeg.sh

## icons: regenerate the tray icons
icons:
	$(GO) run scripts/generate-tray-icons.go cmd/ether-tray/assets

## release-snapshot: goreleaser dry run (needs ffmpeg staged + goreleaser)
release-snapshot:
	scripts/fetch-ffmpeg.sh all
	goreleaser release --snapshot --clean --skip=docker,publish

## keys: generate an update-signing keypair (prints env lines; keep private!)
keys:
	$(GO) run ./cmd/ether-release-sign keygen

clean:
	rm -rf dist
