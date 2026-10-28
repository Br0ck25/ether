# Ether — multi-stage build.
#   docker build -t ether .
#   docker run --network host -v ether-config:/config -v ether-cache:/cache ether
# Host networking is recommended: Plex clients discover the server via GDM
# (UDP 32410-32414) and remote access uses UPnP, neither of which survive
# port-mapped bridge networking. Bridge mode works for Plex Web if you map
# -p 32400:32400. See docs/install.md.
#
# Bundled ffmpeg: the pinned jellyfin-ffmpeg portable build (GPL) staged by
# scripts/fetch-ffmpeg.sh into dist/ffmpeg/<os>-<arch>. Build with
#   scripts/fetch-ffmpeg.sh linux-$(go env GOARCH) && docker build .
# docker buildx per-arch builds pass TARGETARCH automatically.

# ---- build the server (pure Go) -------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG ETHER_UPDATE_PUBLIC_KEY=
ARG ETHER_TMDB_KEY=
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/braedonsaunders/ether/internal/updater.Version=${VERSION} \
        -X github.com/braedonsaunders/ether/internal/updater.Commit=${COMMIT} \
        -X github.com/braedonsaunders/ether/internal/updater.PublicKeyB64=${ETHER_UPDATE_PUBLIC_KEY} \
        -X github.com/braedonsaunders/ether/internal/tmdb.BundledAPIKey=${ETHER_TMDB_KEY}" \
      -o /out/ether ./cmd/ether

# ---- runtime ----------------------------------------------------------------
# Debian (not Alpine): the jellyfin-ffmpeg portable build and VAAPI/QSV
# userspace drivers are glibc-based.
FROM debian:bookworm-slim AS runtime
ARG TARGETARCH

# Optional hardware-transcode userspace. Intel QSV needs intel-media-va-driver
# from non-free; the open iHD-free fallback + Mesa VAAPI cover most iGPU/NVIDIA
# (via nvidia-container-toolkit) cases. Install is best-effort-small: comment
# out this block for a CPU-only image (~120 MB smaller).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates tzdata \
      libva2 vainfo mesa-va-drivers \
      ocl-icd-libopencl1 \
 && rm -rf /var/lib/apt/lists/*

RUN groupadd --system ether \
 && useradd --system --gid ether --home-dir /config --shell /usr/sbin/nologin ether \
 && mkdir -p /config /cache /usr/lib/ether \
 && chown -R ether:ether /config /cache

COPY --from=build /out/ether /usr/lib/ether/ether
# ffmpeg/ffprobe next to the binary, where internal/transcode looks first.
# Requires scripts/fetch-ffmpeg.sh to have staged dist/ffmpeg/ (build fails
# loudly otherwise — never ship an image that cannot transcode silently).
COPY dist/ffmpeg/linux-${TARGETARCH}/ /usr/lib/ether/
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/ether/

ENV ETHER_DATA_DIR=/config \
    XDG_CACHE_HOME=/cache

# Plex API + web UI. GDM discovery (32410-32414/udp) and DLNA-ish ports only
# work with host networking; EXPOSE is documentation here.
EXPOSE 32400/tcp 32410-32414/udp 8096/tcp 7359/udp
VOLUME ["/config", "/cache"]

USER ether
WORKDIR /config
ENTRYPOINT ["/usr/lib/ether/ether"]
