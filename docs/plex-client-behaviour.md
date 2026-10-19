# Plex Web client behavior observed in the served Plex Web bundle

This note records static observations from the Plex Web JavaScript bundle
served by the reference PMS (`/web/index.html`). The bundle is not copied
into this repository. It was fetched read-only and searched locally; behavior
controlled by runtime feature flags can differ from the defaults below.

## Resource and connection selection

Plex Web models resource records with fields including `clientIdentifier`,
`provides`, `accessToken`, `publicAddressMatches`, `httpsRequired`,
`ownerId`, and a related `connections` collection. A connection candidate
must first pass its connection test. The client then ranks working
candidates pairwise: direct over relay, loopback over non-loopback, private
over public, and plain HTTP over HTTPS. The default comparator therefore
does not treat `plex.direct` as an unconditional preference.

The served bundle's feature defaults set
`allowHttpsMediaServerConnections: false`,
`allowMediaServerRelayConnections: false`, and
`allowIPv6ServerConnections: false`. These values may be overridden by the
embedded `window.PLEXWEB` configuration. The resources request adds
`includeHttps=1`, `includeRelay=1`, or `includeIPv6=1` only when the
corresponding flag is enabled. The best-connection selector filters out
HTTPS candidates unless HTTPS media-server connections are allowed (cloud
resources are exempt). This means the existence of a plex.direct URI in
the resource list alone does not guarantee Plex Web will use it.

For a resource whose server identifier is unknown, Plex Web calls that
connection's `/identity`, parses the XML `machineIdentifier`, and then
checks whether that identifier belongs to a known server or refreshes the
account resources. This is why `/identity` must return a parseable
`machineIdentifier`, even before Plex Web has associated the connection
with a server record.

## Server root and providers

The bundle models server resources using identifiers, product/version,
friendly name, connection data, access token, and feature flags from Plex's
resources records. This static inspection did not find a distinct Plex Web
parser contract for the PMS root `/`; keep its `MediaContainer` identity and
version fields consistent with `/identity` and the registered resource.

The `ProviderCollection` maps to `/media/providers`. It is fetched when the
server advertises media providers (or is a cloud server), and provider
records use `identifier` as their key. The served fixture shows fields
`identifier`, `baseURL`, `title`, `icon`, `sourceTitle`, and `token`.
Provider features/actions build on those records. A server with no provider
feature does not need to pretend to offer third-party providers; if it
advertises providers, the collection and referenced feature/action shapes
must be coherent.

## Playback errors

The bundle contains a `PlaybackErrorModal` whose title is “Playback Error”.
It renders the supplied playback error body, and can display an error code
and category. It offers Retry and Skip only when the playback state provides
those actions. Other visible error states use explicit titles/descriptions
such as “No content available” and “Something went wrong”; a pending request
with no error state can remain a spinner. Ether should return a terminal,
actionable PMS playback error for an unavailable release so the client can
render a message instead of remaining in a pending playback state.

## Evidence and limits

- Plex Web was fetched from the owner-approved reference PMS at
  `$PLEX_REF_URL/web/index.html`; only the served JavaScript bundle was
  analyzed, and no bundle or personalized data is included here.
- The connection comparator and feature defaults were found in the
  `ConnectionCollection` and resource collection modules of the served
  bundle. `/identity` parsing was found in the server connection test
  manager.
- The client does not expose a single fixed selection policy independent of
  runtime flags. iOS and Apple TV behavior was not analyzed in this pass.
