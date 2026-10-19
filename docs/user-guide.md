# Using Ether

Open <http://localhost:32400/ether/> on the server to manage Ether. Use your
normal Plex apps to browse and watch.

## Set up

Sign in to Plex, add your Usenet providers and Newznab indexers, and choose a
cache folder and budget. Configure quality profiles for your network and remote
connections. Ether supplies no media, provider accounts or indexer access.

## Share with viewers

Use Plex library sharing to invite people or remove their access. They appear
in Ether's **Viewers** page after signing in. Open a viewer to see their taste,
activity and Home, or set content and playback limits. Each viewer keeps their
own history and recommendations.

## Organise your Home

Use **Home Designer** to choose the server's default rows and ordering. Select a
viewer to preview their Home and make personal exceptions. **Collections** lets
you build and edit themed lists. Recommendations improve as viewers watch and
rate titles.

A collection can mix movies and TV shows in one Home row. In its editor, choose
**Contains → Both**, add sources or individual titles, and enable its Home row
or place it in Home Designer. Each item opens its own movie or show page.
Viewer access and age limits still apply; Movies and TV library pages keep their
respective types.

Movies appear from their theatrical release date. Shows, seasons and episodes
appear from their air dates. Items with no known release date stay hidden.

## Edit settings

Pages use subtabs to group related settings. Use an **Edit** or **Create** action
to open a drawer, make changes and save. Audio and subtitle preferences are under
**Audio & subtitles**; quality profiles and client rules are under **Playback &
releases**.

## Versions, trailers, downloads and previews

Detail pages can list several versions of a title, such as 4K, a
high-compatibility 1080p release or an extended cut. Choose one with Plex's
**Play Version**; otherwise Ether picks the best version for the device. Search
also accepts descriptions such as "90s thrillers under 100 mins" and offers a
matching collection; rate it to keep it as a personal collection.

These settings have no dedicated pages yet. The owner UI lists them alongside
the server's general settings; they are stored in the server's configuration
file under these names:

- `downloads`: offline downloads, on for the owner. Set `allowFriends` to let
  viewers download.
- `extras`: trailers and Cinema Trailers, off until `enabled` is set. Configure
  `resolverCommand`, a yt-dlp-compatible tool you install.
- `personalize`: post-play ranking, detail-page headers and session-based Home
  changes.
- `transcode`: `fastStart`, `abrLadder`, `vbvReservoir` and `spikeGovernor`
  control adaptive streaming.
- `usenet.minIdle`: warm provider connections kept ready for playback.

Preview thumbnails appear while a title plays and fill in as playback
progresses.

## Bring your library and history

Use **Libraries & backups** to add existing media or preview a Plex import. Map
each source viewer to the correct Ether viewer and review unmatched titles
before importing. Keep backups private: they contain credentials needed to
restore your server.

## Live TV

Open **Live TV & IPTV** to create a virtual channel or import an M3U source.
Supply a matching XMLTV guide for programme schedules. Search the channel list
or filter to favourites; edit a channel in its drawer to change its settings.
Ether includes no channel sources by default.

## Monitor and troubleshoot

Use **Activity & notifications** for playback activity and alerts. **Logs** shows
newest entries first. If playback fails, check provider and indexer connectivity,
quality limits and the client's supported formats. Share redacted logs when
reporting an issue.

## Owner sign-in and playback access

After setup, sign in with the Plex account that owns Ether, including when you
open the owner UI on localhost. Viewers access shared libraries through their
own Plex accounts. Refreshing Plex sharing removes revoked viewers' access;
stream links stop working when the viewer is inactive or the session ends.

## Source interruptions

Ether tries other providers before changing a release. Copy-only HLS or DASH
can restart on another release at the current media time. Direct-play MKV and MP4
have stricter replacement limits because the player retains the original file's
headers. Provider outages and timeouts are retriable and do not permanently mark
a release bad. Different cuts of a title can still skip or repeat a short section
when Ether maps the playback timestamp.
