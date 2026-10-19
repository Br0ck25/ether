# Owner Live TV channels

Ether's Live TV module supports owner-created virtual channels and an owner's
HTTP(S) M3U playlist with an optional XMLTV guide. It ships no channels, playlist
URLs, accounts or programming. Live TV discovery and tuning are enabled only
when the server owner's subscription has been verified as Plex Pass by the
existing plex.tv registrar. No Plex account entitlement is synthesized.

## Owner setup

Open **Live TV** in the Ether owner interface. Create a channel from a catalogue
collection key, a movie genre/template, or explicit movie/episode catalogue keys
and runtimes. A collection's shows expand into episodes. Saving takes a stable
snapshot of the selected programming; save again to refresh its membership.
Templates supply filters only and do not add channels until saved.

Sequential channels repeat the snapshot continuously from their schedule anchor.
Shuffle uses the channel seed and programme identity to produce the same order
after a restart. Changing the seed changes the order. Daily time blocks specify
start/end **UTC minutes after midnight**, their own programme items and shuffle
choice. Blocks cannot overlap. Base programming fills the remaining hours. The
last item in a block is cut at the boundary so the next block starts on time.
Guide times are displayed in the browser's local timezone.

For IPTV, import an M3U and XMLTV using URLs or file uploads. URL imports are
fetched only when the owner imports or refreshes a source. Uploaded source data
is stored in SQLite so a later refresh still works. Both sources and generated
programming survive restart. Channel numbering, guide mapping, enabled state,
favourites and channel access rules survive a source refresh. A failed import
leaves the previous source intact. Editing, deleting or refreshing a channel's
source closes affected sessions so obsolete source permissions/URLs are not
retained in a live relay.

Use **Viewer access** to disable Live TV or select channels and an additional
rating limit per viewer. Existing viewer content ratings, section restrictions,
owner safety policy, viewing hours, remote access rules and stream limits also
apply. Unknown ratings fail closed for viewers with a rating limit. Guide
entries above a viewer's rating are omitted. Every playlist, segment, encryption
key and initialization map request is authenticated against its viewing session;
a second user's token cannot consume that session.

## Playout and exports

Virtual channels join the current programme on a four-second segment boundary.
Each programme has its own existing Ether playback/transcoder source; a channel
playlist inserts an HLS discontinuity when the programme changes. Sequence
numbers remain monotonic. A bounded window retains recent segments and prepares
upcoming programming. At most three programme source handles overlap within one
viewing group; the playback manager counts the group as one viewer stream.
Existing source failover finds the programme transcoder and preserves media time.
The application's pre-resolution hook primes programming up to three minutes
ahead. Actual first-frame latency depends on release and upstream availability.

IPTV HLS is proxied without changing encryption or discontinuity semantics.
Variant/rendition playlists, relative segment URLs, AES keys and initialization
maps resolve relative to the final upstream URL after redirects and are rewritten
to session-authorized Ether URLs. Requests forward byte ranges. Network/server
failures are retried with bounded delays; an individual segment is buffered
before publication so a truncated response can be retried safely. A media object
is limited to 64 MiB, a playlist to 4 MiB. Opaque resource IDs prevent a viewer
from supplying arbitrary upstream URLs. MPEG-TS and other non-HLS HTTP streams
use the existing ffmpeg HLS remux path with network reconnection. The channel's
**Transcode IPTV codecs** option uses the client's capabilities for normalization.
Unmodified HLS still requires codecs supported by that client.

`GET /livetv/guide.xml` exports the authenticated viewer's guide as XMLTV.
Owner preview/export APIs live below `/ether/api/livetv`. The ordinary API offers
channel and source CRUD, atomic imports/refresh, guide preview, draft schedule
preview, and persistent viewer policy. It must remain behind the admin owner's
authentication and CSRF middleware.

There is no recording scheduler or permanent archive. The rolling stream window
is for live playback; catch-up/record-to-cache remains an optional extension and
is not advertised through the recording subscription feature. Plex's own cloud
favourite-channel feature is managed by Plex; Ether stores owner and viewer
favourites separately.

## Plex protocol evidence and verification

The adapter follows the [official PMS OpenAPI 1.2.3](https://developer.plex.tv/pms/)
DVR, Devices, EPG and Live TV groups: DVR/device discovery, XMLTV provider channel
and grid routes, broadcast bounds on each programme's `Media`, tune/session
metadata, consumer HLS playlists and segments. It also exposes provider Home
hubs and an **On Now** row with Live TV channel identities.

Read-only inspection of the reference PMS's Plex Web **4.160.0** main bundle
confirmed additional requirements beyond the OpenAPI tune example:

- The DVR model reads `Dvr`, rather than the schema's inconsistent `DVR` casing.
- Provider `parentID` is used as the DVR ID in the tune URL.
- A tune must expose `MediaSubscription`, its `MediaGrabOperation.Metadata`, a
  programme `Timeline`, and `X-Plex-Activity`; the client explicitly rejects a
  response without the activity or media parts.
- The player's Live TV capability check requires a server `livetv` level of at
  least four. This is an API capability level, not a subscription entitlement.

Offline fixtures cover these structures in JSON, authentication/restrictions,
DVR → tune → universal decision/start → consumer playlist, HLS relative URLs,
redirects, maps/keys, byte ranges/retries, deterministic schedule windows and
UTC block continuity, and join/failover timestamps. These tests establish API
and playout behavior; they do not establish successful official-client playback.

No claimed live instance or real Plex DVR configuration was mutated during this
implementation. To verify official clients, use a copy of the Ether data dir on
another port for API inspection, and perform the following owner-controlled
checks on the claimed server when ready:

1. Create a short virtual channel from known playable catalogue items, and/or
   import an owner-authorized HLS source with a matching XMLTV channel ID.
2. In Plex Web, refresh the server's sources, open Live TV and confirm the guide,
   channel names/logos and currently airing programme.
3. Tune a channel and inspect the network flow: provider `parentID=1`, POST
   `/livetv/dvrs/1/channels/{id}/tune`, subscription/operation and activity,
   universal decision with `/livetv/sessions/{id}`, then its consumer HLS URL.
4. Join a virtual programme already in progress, wait across a programme
   boundary, and confirm an HLS discontinuity without jumping back to the start.
5. Stop the player and confirm the live subscription/operation is deleted or
   expires after two minutes without activity. Repeat with a restricted viewer
   and confirm blocked channels/programmes and cross-user session URLs fail.
6. Repeat on iOS/Apple TV. Record the app/server versions and any additional
   endpoint requirements; compatibility with those clients is not yet claimed.
