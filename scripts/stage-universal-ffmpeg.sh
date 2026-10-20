#!/bin/sh
# stage-universal-ffmpeg.sh — combine the staged darwin ffmpeg/ffprobe into
# universal binaries at dist/ffmpeg/darwin-all/, matching the
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
  # Debian/Ubuntu LLVM packages expose a version-suffixed command.
  LIPO=
  for candidate in /usr/bin/llvm-lipo-* /usr/lib/llvm-*/bin/llvm-lipo; do
    if [ -x "$candidate" ]; then LIPO=$candidate; break; fi
  done
  if [ -z "$LIPO" ]; then
    echo "stage-universal-ffmpeg: need lipo or llvm-lipo (apt install llvm)" >&2
    exit 1
  fi
fi

mkdir -p "$BASE/darwin-all"
for tool in ffmpeg ffprobe; do
  "$LIPO" -create "$BASE/darwin-amd64/$tool" "$BASE/darwin-arm64/$tool" \
    -output "$BASE/darwin-all/$tool"
  chmod +x "$BASE/darwin-all/$tool"
done
echo "stage-universal-ffmpeg: wrote $BASE/darwin-all/{ffmpeg,ffprobe}"
