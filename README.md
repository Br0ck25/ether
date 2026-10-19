<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/ether-logo-dark.svg" />
    <img src=".github/assets/ether-logo.svg" alt="Ether" width="440" />
  </picture>
</p>

<p align="center">
  <strong>Your Plex. Beyond your library.</strong><br />
  A TMDB catalogue, on-demand streaming and a Home that learns your taste.
</p>


Ether is a self-hosted media server for your Plex apps. Browse movies and TV,
get recommendations that learn each viewer's taste, and stream through your
own Usenet providers and indexers.

## Features

- **Your Plex apps** — browse, search, watch and resume from the apps you use.
- **A broad catalogue** — TMDB artwork and metadata, franchise collections,
  smart collections and released movies and TV.
- **Personal recommendations** — separate history, ratings, playlists and Home
  rows for each viewer, with optional Trakt and AI integrations.
- **Playback that fits your devices** — quality profiles, hardware transcoding,
  audio and subtitle preferences, next-episode prefetch and resume caching.
- **Local libraries and migration** — include existing media, import Plex
  history and lists, and back up your configuration and watch state.
- **Live TV** — build virtual channels or add an M3U playlist and XMLTV guide.
- **Owner controls** — manage Home layouts, viewer restrictions, stream limits,
  notifications and activity from the browser.

## Get started

**Pre-release:** there are no published installer downloads yet. Packaging is
configured for Windows, macOS and Linux; available downloads will appear on the
[Releases page](https://github.com/braedonsaunders/ether/releases).

See [installation](docs/install.md), the [user guide](docs/user-guide.md), and the
[product and architecture specification](docs/design.md).
Once Ether is running, open <http://localhost:32400/ether/> to sign in to Plex
and configure your providers and indexers. Share the server through Plex to add
viewers.

## Support

For a bug report, include your Ether version, operating system, Plex app and
version, what you did and what happened. Remove credentials and personal details
from any logs you attach.

## Licence and services

Ether ships no media, providers, indexers or accounts. Owners supply their own
services and are responsible for what they access. Ether is independent of Plex
and TMDB. See [LEGAL.md](LEGAL.md).

Licensed under the [GNU General Public License v3.0](LICENSE).
[Third-party notices](THIRD_PARTY_NOTICES.md).
