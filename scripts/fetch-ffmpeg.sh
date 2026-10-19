#!/bin/sh
# fetch-ffmpeg.sh — download the pinned ffmpeg/ffprobe builds Ether bundles,
# verify them against packaging/ffmpeg.lock.json, and stage them at
# dist/ffmpeg/<goos>-<goarch>/{ffmpeg,ffprobe} (+ any bundled shared libs).
#
# internal/transcode finds ffmpeg next to the ether binary (then $PATH), so
# packaging copies this directory's contents beside the binary:
#   linux packages / Docker : /usr/lib/ether/ffmpeg (next to /usr/lib/ether/ether)
#   macOS                   : Ether.app/Contents/Resources/
#   Windows                 : the install directory (ffmpeg.exe)
#
# Usage:
#   scripts/fetch-ffmpeg.sh                    # host platform only
#   scripts/fetch-ffmpeg.sh linux-amd64 linux-arm64 windows-amd64
#   scripts/fetch-ffmpeg.sh all
# Env: FFMPEG_OUT (default dist/ffmpeg), FFMPEG_LOCK (default packaging/ffmpeg.lock.json)
#
# Requires: curl, tar (xz), unzip (windows targets), python3 or jq, shasum/sha256sum.
# The builds are GPL builds of FFmpeg from jellyfin/jellyfin-ffmpeg; Ether is
# GPLv3 so redistribution with source offer is fine — see THIRD_PARTY_NOTICES.md.
set -eu

# shellcheck disable=SC1007 # CDPATH= intentionally clears CDPATH for cd
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
LOCK=${FFMPEG_LOCK:-"$ROOT/packaging/ffmpeg.lock.json"}
OUT=${FFMPEG_OUT:-"$ROOT/dist/ffmpeg"}

host_platform() {
  os=$(uname -s)
  arch=$(uname -m)
  case "$os" in
    Linux) goos=linux ;;
    Darwin) goos=darwin ;;
    MINGW*|MSYS*|CYGWIN*) goos=windows ;;
    *) echo "fetch-ffmpeg: unsupported OS $os" >&2; exit 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) goarch=amd64 ;;
    arm64|aarch64) goarch=arm64 ;;
    *) echo "fetch-ffmpeg: unsupported architecture $arch" >&2; exit 1 ;;
  esac
  echo "$goos-$goarch"
}

# lock_field <platform> <url|sha256> — read from the lock JSON.
lock_field() {
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$LOCK" "$1" "$2" <<'PY'
import json, sys
lock, plat, field = sys.argv[1], sys.argv[2], sys.argv[3]
with open(lock) as f:
    data = json.load(f)
try:
    print(data["artifacts"][plat][field])
except KeyError:
    sys.exit(1)
PY
  elif command -v jq >/dev/null 2>&1; then
    jq -er --arg p "$1" --arg f "$2" '.artifacts[$p][$f]' "$LOCK"
  else
    echo "fetch-ffmpeg: need python3 or jq to read $LOCK" >&2
    exit 1
  fi
}

all_platforms() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys; print(" ".join(json.load(open(sys.argv[1]))["artifacts"]))' "$LOCK"
  else
    jq -r '.artifacts | keys | join(" ")' "$LOCK"
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

fetch_one() {
  plat=$1
  url=$(lock_field "$plat" url) || { echo "fetch-ffmpeg: no lock entry for $plat" >&2; exit 1; }
  want=$(lock_field "$plat" sha256)
  dest="$OUT/$plat"
  case "$plat" in
    windows-*) ff=ffmpeg.exe; fp=ffprobe.exe ;;
    *) ff=ffmpeg; fp=ffprobe ;;
  esac
  if [ -x "$dest/$ff" ] && [ -x "$dest/$fp" ] && [ -f "$dest/.sha256" ] && [ "$(cat "$dest/.sha256")" = "$want" ]; then
    echo "fetch-ffmpeg: $plat already staged (lock match)"
    return 0
  fi
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT INT TERM
  archive="$tmp/ffmpeg-archive"
  echo "fetch-ffmpeg: downloading $plat from $url"
  curl -fL --retry 3 -o "$archive" "$url"
  got=$(sha256_of "$archive")
  if [ "$got" != "$want" ]; then
    echo "fetch-ffmpeg: SHA-256 mismatch for $plat" >&2
    echo "  want $want" >&2
    echo "  got  $got" >&2
    echo "Refusing to use it. If the pin moved, update packaging/ffmpeg.lock.json." >&2
    exit 1
  fi
  mkdir -p "$tmp/unpacked" "$dest"
  case "$url" in
    *.tar.xz|*.txz) tar -xJf "$archive" -C "$tmp/unpacked" ;;
    *.tar.gz|*.tgz) tar -xzf "$archive" -C "$tmp/unpacked" ;;
    *.zip) unzip -q "$archive" -d "$tmp/unpacked" ;;
    *) echo "fetch-ffmpeg: unknown archive type: $url" >&2; exit 1 ;;
  esac
  # Find the binaries (top level in jellyfin-ffmpeg portable builds, but be
  # tolerant of a wrapping directory). Copy siblings too: the linux portable
  # build ships shared libs the binaries need.
  ffpath=$(find "$tmp/unpacked" -maxdepth 3 -name "$ff" -type f | head -n 1)
  fppath=$(find "$tmp/unpacked" -maxdepth 3 -name "$fp" -type f | head -n 1)
  if [ -z "$ffpath" ] || [ -z "$fppath" ]; then
    echo "fetch-ffmpeg: ffmpeg/ffprobe not found in $plat archive" >&2
    find "$tmp/unpacked" -maxdepth 3 | head -n 50 >&2
    exit 1
  fi
  bindir=$(dirname "$ffpath")
  cp -R "$bindir/." "$dest/"
  chmod +x "$dest/$ff" "$dest/$fp" 2>/dev/null || true
  echo "$want" > "$dest/.sha256"
  rm -rf "$tmp"
  trap - EXIT INT TERM
  echo "fetch-ffmpeg: staged $plat -> $dest ($ff, $fp)"
}

if [ "$#" -eq 0 ]; then
  set -- "$(host_platform)"
elif [ "$1" = "all" ]; then
  # shellcheck disable=SC2046
  set -- $(all_platforms)
fi

for plat in "$@"; do
  fetch_one "$plat"
done
echo "fetch-ffmpeg: done"
