// Package jellyfin is Ether's Jellyfin front end: the HTTP API that
// the official Jellyfin apps (and third-party clients such as
// Infuse, Streamyfin, Findroid and Swiftfin) talk to. It runs next to the
// Plex front end in the same process and reaches the same catalogue, watch
// state, resolver and transcoder through internal/core, so a title that is
// playable in Plex is playable here, and watch progress is shared.
//
// Paths match case-insensitively. The "X-Emby-*" header and query names are
// Jellyfin's own (it forked from Emby) and the apps still send them.
//
// Unlike Plex, these apps sign in with a user name and password, and Ether's
// viewers are Plex accounts. The owner therefore gives each viewer an access
// code in the admin UI; the viewer's Plex user name and that code sign them
// in. Sign-in yields a server-local token, never a plex.tv credential.
package jellyfin
