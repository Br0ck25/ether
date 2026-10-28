// Package jellyfin is Ether's Jellyfin and Emby front end: the HTTP API that
// the official Jellyfin and Emby apps (and third-party clients such as
// Infuse, Streamyfin, Findroid and Swiftfin) talk to. It runs next to the
// Plex front end in the same process and reaches the same catalogue, watch
// state, resolver and transcoder through internal/core, so a title that is
// playable in Plex is playable here, and watch progress is shared.
//
// Jellyfin forked from Emby and the two APIs share their shape: Emby serves
// everything under an optional /emby prefix, Jellyfin at the root, and both
// accept every route either way. The server answers both on one port.
//
// Unlike Plex, these apps sign in with a user name and password, and Ether's
// viewers are Plex accounts. The owner therefore gives each viewer an access
// code in the admin UI; the viewer's Plex user name and that code sign them
// in. Sign-in yields a server-local token, never a plex.tv credential.
package jellyfin
