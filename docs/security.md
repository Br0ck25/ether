# Security and account boundaries

Ether's Plex API authenticates viewers with their real Plex server access
credentials. Plex Pass and other account entitlements come from Plex; Ether
does not grant paid features to an account. The owner interface remains LAN
only and uses header credentials, explicit CSRF headers on mutations, bounded
sign-in requests, and private, noncached API responses. Invitation links use
random bearer tokens stored as hashes; sign-in handles are bound to the
invitation and account. Expiration, revocation and use limits are checked
transactionally before sharing.

Adult TMDB items are excluded. Restricted viewers cannot access unrated titles;
invalid rating caps fail closed. Library, content, genre and keyword policies
apply to Plex metadata, rows, search, artwork and playback. Playback policies
also support local-only access, bitrate caps and allowed hours. Account sharing
and device access still depend on Plex's account service.

Configuration credentials use AES-GCM with a random per-install key in
`.credentials-key` (mode 0600). Existing plaintext settings are migrated on the
next open. Complete backup bundles include this key, so they contain everything
needed to recover account credentials and must be kept private. Encryption does
not protect an unlocked machine from its administrator or software that can read
both the configuration and the key. Losing the key makes encrypted credentials
unrecoverable; Ether fails explicitly instead of silently resetting them.

Automatic Clean Home changes only online-source settings advertised by Plex for
the owner's account, including managed Home users. Shared-library credentials
cannot change a friend's independent account settings. Ether's responses include
its own libraries and channels. Plex controls its client navigation, so account
opt-outs do not guarantee that every app removes every navigation tab.

External images, indexers, IPTV URLs, SMTP relays and webhook destinations are
owner-selected network integrations. Only an authenticated owner can configure
them. Do not expose the owner API through an untrusted reverse proxy. Configure
remote playback separately from administration, and keep the data directory on
a local filesystem. Backups validate archive paths, checksums and SQLite
integrity and stage restoration into a new directory.

The compatibility version follows Plex's public release manifest unless the
owner disables tracking or supplies an override. This is an API compatibility
identifier, independent of Ether's actual software version. Release tracking is
not proof that a new Plex app or API remains compatible. Offline fixture tests
and real-client checks remain necessary.

Primary references: [Plex online-source account API implementation](https://python-plexapi.readthedocs.io/en/latest/_modules/plexapi/myplex.html),
[Plex public releases](https://plex.tv/api/downloads/5.json).
