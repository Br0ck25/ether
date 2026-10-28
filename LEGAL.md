# Legal

Ether is free, open-source software licensed under the GNU General
Public License v3.0. See [LICENSE](LICENSE).

## Ether ships no content, indexers or credentials

- Ether contains **no media files** and hosts none. Its catalogue is
  metadata (titles, artwork, descriptions) fetched from TMDB using the
  owner's API key.
- Ether contains **no Usenet providers, no indexers, no site definitions,
  no default indexer URLs, no accounts, and no embedded third-party API
  keys**. To browse or play anything, the owner must supply their own
  TMDB API key, Usenet provider subscription(s), and indexer access in
  the setup wizard. What those services return is solely between the
  owner and those services.
- Ether is a clean-room *protocol implementation*: it answers the HTTP
  API that compatible client apps use to talk to a media server. It
  contains no proprietary Plex code, assets, or binaries.

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
