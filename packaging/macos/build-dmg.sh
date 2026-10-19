#!/bin/sh
# build-dmg.sh — wrap Ether.app in a drag-to-Applications DMG and, when CI
# secrets are present, sign + notarise + staple it.
#
# Usage: VERSION=1.2.0 packaging/macos/build-dmg.sh [Ether.app path] [out.dmg]
# CI secrets (all optional for local builds):
#   SIGN_IDENTITY             "Developer ID Application: Name (TEAMID)"
#   NOTARY_APPLE_ID, NOTARY_TEAM_ID, NOTARY_PASSWORD  (app-specific password)
# Uses hdiutil only — no create-dmg dependency.
set -eu

# shellcheck disable=SC1007
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
VERSION=${VERSION:-dev}
APP=${1:-"$ROOT/dist/macos/Ether.app"}
DMG=${2:-"$ROOT/dist/Ether-$VERSION.dmg"}

[ -d "$APP" ] || { echo "build-dmg: $APP not found (run build-app.sh first)" >&2; exit 1; }

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT INT TERM
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
cp "$ROOT/LICENSE" "$STAGE/"

mkdir -p "$(dirname "$DMG")"
rm -f "$DMG"
echo "==> creating $DMG"
hdiutil create -volname "Ether" -srcfolder "$STAGE" -ov -format UDZO "$DMG" >/dev/null

if [ -n "${SIGN_IDENTITY:-}" ]; then
  echo "==> codesigning DMG"
  codesign --force --timestamp --sign "$SIGN_IDENTITY" "$DMG"
fi

if [ -n "${NOTARY_APPLE_ID:-}" ]; then
  echo "==> notarising (this takes a few minutes)"
  xcrun notarytool submit "$DMG" \
    --apple-id "$NOTARY_APPLE_ID" \
    --team-id "$NOTARY_TEAM_ID" \
    --password "$NOTARY_PASSWORD" \
    --wait
  xcrun stapler staple "$DMG"
  xcrun stapler validate "$DMG"
else
  echo "==> NOTARY_APPLE_ID unset: skipping notarisation (dev build)"
fi

echo "==> done: $DMG"
