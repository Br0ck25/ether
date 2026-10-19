# macOS packaging

`Ether.app` is a menu-bar app (`LSUIElement` in
[Info.plist](Info.plist)): the bundle executable is the tray
(`Contents/MacOS/ether-tray`, the only CGO binary in the project). The
server and ffmpeg ride along as resources:

```
Ether.app/Contents/
  Info.plist
  MacOS/ether-tray                 tray (universal, CGO)
  Resources/ether                  server (universal, CGO_ENABLED=0)
  Resources/ffmpeg, Resources/ffprobe
  Resources/dev.ether-media.ether.plist   LaunchAgent template for the server
```

How the server starts at login: on first run the tray installs the
LaunchAgent template (substituting the app path) into
`~/Library/LaunchAgents/` and bootstraps it — `ether service install`
renders the equivalent unit via `internal/service`. The tray itself is a
login item through its own "Launch at login" toggle.

## Build

```sh
VERSION=1.2.0 packaging/macos/build-app.sh   # -> dist/macos/Ether.app
VERSION=1.2.0 packaging/macos/build-dmg.sh   # -> dist/Ether-1.2.0.dmg
```

## Signing + notarisation (CI)

With `SIGN_IDENTITY` set, `build-app.sh` signs every nested binary and
the bundle with the hardened runtime; `build-dmg.sh` signs the DMG and,
with `NOTARY_APPLE_ID`/`NOTARY_TEAM_ID`/`NOTARY_PASSWORD`, submits to
`notarytool` and staples the ticket. The release workflow imports the
Developer ID certificate (`MACOS_CERT_P12` secret) into a throwaway
keychain on the macos runner. Without the secrets the same scripts
produce working unsigned builds for development.
