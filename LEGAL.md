# Legal

Ether is free, open-source software licensed under the GNU General
Public License v3.0. See [LICENSE](LICENSE).

## Ether ships no content

- Ether contains **no media files** and hosts none. Its library is
  metadata (titles, artwork, descriptions) from TMDB.
- Ether contains **no Usenet providers, no indexers and no accounts**.
  To play anything, the owner must supply their own Usenet provider
  subscription(s) and their own Newznab indexer access in the setup
  wizard. What those services return is between the owner and those
  services.
- Ether is a *client/server implementation*: it answers the HTTP API
  that Plex apps use to talk to a Plex Media Server. It contains no Plex
  code, assets or binaries.

## Owner responsibility

Owners are solely responsible for:

- the provider and indexer accounts they configure,
- what they access, stream, cache or share with it, and
- complying with the law where they live and with the terms of the
  services they connect Ether to (including plex.tv, TMDB, Trakt and
  OpenSubtitles).

The cached segments Ether writes to disk exist only so playback is
smooth and resumable; Ether does not offer downloads for keeping.

## Trademarks

"Plex" and the Plex logo are trademarks of Plex, Inc. Ether is an
independent open-source project; it is **not affiliated with, sponsored
or endorsed by Plex, Inc.** "TMDB"/"The Movie Database" is a trademark
of its respective owner; this product uses the TMDB API but is not
endorsed or certified by TMDB. Other names (Trakt, OpenSubtitles,
FFmpeg) belong to their owners and are used only to describe
interoperability.

## Bundled third-party software

Installers bundle an FFmpeg build (GPL) and the Go modules listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md), on their respective
licences. Corresponding source for GPL components is available from
their upstreams and, for Ether itself, in this repository — that is our
written source offer for binary distributions of Ether.
