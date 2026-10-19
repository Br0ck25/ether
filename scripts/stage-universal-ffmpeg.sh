#!/bin/sh
# stage-universal-ffmpeg.sh — combine the staged darwin ffmpeg/ffprobe into
# universal binaries at dist/ffmpeg/darwin-universal/, matching the
# universal ether binary in the macOS tarball (.goreleaser.yaml archives).
# Needs `lipo` (Xcode CLT, macOS) or `llvm-lipo` (Linux CI: apt install llvm).
# No-op (with a message) when the darwin builds have not been fetched.
set -eu

# shellcheck disable=SC1007
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BASE=${FFMPEG_OUT:-"$ROOT/dist/ffmpeg"}

if [ ! -f "$BASE/darwin-amd64/ffmpeg" ] || [ ! -f "$BASE/darwin-arm64/ffmpeg" ]; then
  echo "stage-universal-ffmpeg: darwin ffmpeg not staged; run scripts/fetch-ffmpeg.sh darwin-amd64 darwin-arm64"
  exit 0
fi

if command -v lipo >/dev/null 2>&1; then
  LIPO=lipo
elif command -v llvm-lipo >/dev/null 2>&1; then
  LIPO=llvm-lipo
else
  echo "stage-universal-ffmpeg: need lipo or llvm-lipo (apt install llvm)" >&2
  exit 1
fi

mkdir -p "$BASE/darwin-universal"
for tool in ffmpeg ffprobe; do
  "$LIPO" -create "$BASE/darwin-amd64/$tool" "$BASE/darwin-arm64/$tool" \
    -output "$BASE/darwin-universal/$tool"
  chmod +x "$BASE/darwin-universal/$tool"
done
echo "stage-universal-ffmpeg: wrote $BASE/darwin-universal/{ffmpeg,ffprobe}"
