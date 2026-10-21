# Using Ether

Open <http://localhost:32400/ether/> on the server to manage Ether. Use your
normal Plex apps to browse and watch.

## Set up

Sign in to Plex, add your Usenet providers and indexers (Newznab/Torznab URLs
or built-in sites), and choose a cache folder and budget. Configure quality profiles for your network and remote
connections. Ether supplies no media, provider accounts or indexer access.

You can also stream torrents that your own debrid account (Real-Debrid, TorBox,
AllDebrid or Premiumize) already holds.

## Stream torrents through debrid

1. Under **Sources › Indexers**, add a torrent source: a built-in site whose
   definition is a torrent site (see [Built-in indexers](#built-in-indexers)),
   or a Torznab URL with its protocol set to **Torrent (Torznab)**. If you
   already run Prowlarr you can import its indexers instead, but you don't
   need it.
2. Under **Sources › Debrid**, add your debrid account with its API key and use
   **Test** to check it. Lower priority numbers are tried first.

Without an enabled debrid account, torrent results are ignored. Ether streams
the file over HTTPS from your debrid service; it never joins a torrent swarm and
never downloads a whole file.

- **Cached torrents only** (on by default): a torrent is used only if your
  service can serve it now, so playback never waits for a download.
- **Remove uncached torrents** (on by default): torrents Ether added that turn
  out not to be cached are removed again. Cached torrents stay on your account,
  so the next play of that title starts straight away.
- **Season packs:** for an episode, Ether streams that episode's file from a
  torrent season pack. Pins, blocks and health checks apply to the episode.

Torrents whose video is inside RAR archives are skipped. A result that only
offers a `.torrent` link costs one indexer download to read its hash.

### Which source plays

Ether never trades quality for speed. Among releases of the same quality, it
puts the source that starts faster first. Set this under **Releases › Playback
start › Source for equal-quality releases**:

- **Fastest (automatic):** once both sources have enough plays, the one with
  the lower measured start time goes first. Until then, popular titles use
  cached debrid first. The measured times are shown beside the setting.
- **Prefer Usenet** or **Prefer debrid:** always try that source first.

The best release from the other source is checked at the same time, and both
remain available for failover. **Releases › Pinned** (when you find a title) and **History** show when
routing moved a release, and why.

The debrid integrations were built from each service's public API
documentation and tested against simulated services, not live accounts. If a
service misbehaves, check **Logs** and its status on **Sources › Debrid**.

## Built-in indexers

Ether can search sites itself, without a separate indexer manager. Each
built-in site runs from a *definition*: a YAML file in the Cardigann format
that Prowlarr and Jackett also use, describing the site's search pages,
sign-in, settings and categories. **Ether ships no definitions.** Install only
definitions for sites you have the right to use, and follow those sites'
rules.

### Install definitions

Under **Sources › Indexers › Built-in sites**:

- **Upload definition** takes a `.yml` file or pasted YAML. Ether checks it
  before installing and lists anything it does not support (for example a
  captcha sign-in or an unknown filter) as a warning beside the definition.
- **Definitions source** (optional) is a folder of definitions you trust: a
  GitHub folder URL (`https://github.com/<owner>/<repo>/tree/<branch>/<folder>`),
  the matching raw folder or GitHub contents API URL, or a URL that returns a
  plain list of definition files. Ether reads the list only when you add a
  site, and downloads a definition when you pick it.
- **Update definitions** re-downloads every definition that came from the
  source and installs the ones that changed. Uploaded definitions are left
  as they are. A definition that an indexer uses cannot be removed.

### Add a site

1. Choose **Add indexer › Built-in site…**, search, and pick an installed
   definition, or install one from your definitions source.
2. The drawer shows the settings the definition asks for, such as a username
   and password, a cookie, a passkey or a sort order. Passwords are stored
   encrypted and shown as `••••` afterwards; leave them to keep them.
3. **Site address** defaults to the definition's first address; alternative
   addresses are listed under it.
4. Set daily search and download budgets if the site limits them.
5. **Test** signs in and runs a sample search, then shows how many results it
   found and a few titles, or the site's error.

Built-in sites appear in the same list as Newznab and Torznab indexers, with
a **Built-in** badge, their protocol and their last test result. They share
the same search order, daily budgets, result cache and failover. Torrent sites
stream through your debrid account; a definition that serves NZB files
streams from your Usenet providers.

### Sites behind a browser challenge

Some sites put a Cloudflare challenge in front of their pages. Ether cannot
pass it alone and reports that the site needs FlareSolverr. If you run
[FlareSolverr](https://github.com/FlareSolverr/FlareSolverr), enter its
address (for example `http://localhost:8191`) under **FlareSolverr**. Ether
asks it to solve the challenge only when a site presents one, then reuses
the clearance it returns.

Sign-ins that need a captcha are not supported; if the definition offers
cookie sign-in, paste the cookie from a signed-in browser instead.

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

## Choose and fix releases

**Releases** explains and overrides which release plays for each title.

- **Pinned** starts with **Find a release**: find a movie, or a show then a
  season and episode, to list every release the indexers return. Each one
  shows its score, custom formats, size, age, indexer and Ether's verdict for
  the chosen quality profile, ranked exactly as playback ranks them, with the
  release that would play first marked. Choose **No profile limits** to see
  releases a profile would reject, or **Offline download** for the download
  profile. **Search again** forgets the cached results and any "unavailable"
  cool-off for the title. Each release's menu can pin it, block it with a
  reason, or check its health now.

  Below the search are the releases you pinned. A pin plays first for its title on
  every app, overriding quality-profile limits and title matching. Ether still
  skips a pin that is blocklisted, passworded, already tried during the current
  playback, a season pack for an episode, in a video codec the app cannot
  decode, or Dolby Vision without an HDR10 base on an app without Dolby Vision.
  A viewer's own **Play Version** choice wins over a pin, and offline downloads
  keep their own profile. When a pin fails its health check, Ether plays the
  next best release and the pin stays, showing why it failed.
- **Blocklist** lists releases Ether will not pick: ones that failed health
  checks or broke during playback, and ones you blocked. Filter by title or
  source; unblocking a release has it checked again the next time it is a
  candidate. Automatic entries expire 90 days after they last failed (a
  release still broken then fails one more check); releases you blocked stay
  blocked until you unblock them.
- **History** records each release decision: the winner, failovers, pin use
  and, for every candidate, why it won or lost. It keeps the newest 5,000
  decisions for up to 60 days.

Pins, blocks and **Search again** take effect on the next play, including for
titles a detail page has already prepared.

## Bring your library and history

Use **Libraries & backups** to add existing media or preview a Plex import. Map
each source viewer to the correct Ether viewer and review unmatched titles
before importing. Keep backups private: they contain credentials needed to
restore your server.

## Live TV

Open **Live TV & IPTV** to create a virtual channel or import an M3U source.
Supply a matching XMLTV guide for programme schedules. Search the channel list
or filter to favourites; select a channel to change its settings.
Ether includes no channel sources by default.

A channel can have several stream sources in priority order. Enable the inputs
you want and put the preferred one first. If it fails during tuning, Ether tries
a backup within the same startup budget. Failed inputs cool down briefly before
being retried. The channel keeps its number, favourites, sharing and programme
guide when its input changes. A running stream stays on its selected input;
retuning after an interruption can select a healthy backup.

Programme guides can come from separate XMLTV sources, including compressed
`.xml.gz` feeds. Map each channel to its matching guide. URL sources refresh
automatically and retain existing listings if a refresh fails; uploaded files
remain snapshots. Guide coverage shows channels that need a mapping or new data.

## Cluster

**Server › Cluster** shows how this server stores its data. A standalone
server uses its own SQLite database; choose **Create cluster** to test a
PostgreSQL connection, review the migration and let Ether back up, migrate and
restart automatically. Choose **Join cluster** to add this machine with a
one-time token from another node (see [Running a cluster](install.md#running-a-cluster)).

On a cluster node the page lists every node with its version, whether it leads
the cluster, whether it is online (or when it was last seen), its active
streams, hardware transcoding, node weight and public IP. A banner warns about
conditions to fix, such as nodes reaching the internet from different public
addresses, or this node losing touch with the database. The page also shows
the shared settings version and the credential key fingerprint, which match on
every node, and refreshes every 10 seconds.

To add a node, choose **Create join token**. The drawer shows the token
once. On the new machine, open **Cluster › Join cluster**, paste it and follow
the wizard. Fresh installs also offer **Join a cluster instead** during setup.
No terminal commands are required. Keep the token secret and revoke unused
tokens you no longer need. Expiration, use and revocation only prevent Ether
enrollment; the embedded database password and credential key remain
sensitive. If a token leaks, rotate the database password on every node and
any stored provider or Plex credentials the recipient could have read.
Revocation alone does not contain a leak.

**Manage this node** adjusts its provider capacity weight or pauses/resumes new
streams while existing streams keep playing. A pause ends after a restart.
Pausing the only available node prevents new playback until you resume it.
**Open manager** opens a peer node’s own settings; **Updates** handles rolling
cluster updates.

Elsewhere, **Backups & import** shows which node holds each backup and offers
a shared backup folder, the Usenet and Debrid pages show this node's share of
each account's connections, and **General** holds this node's own settings,
including its node weight.

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

## Cache management

**Server → Cache** controls media retention, opening and resume warming, and an
optional managed local movie shelf. Presets range from no persistent media
through small caches to multi-terabyte preloading. Every budget is adjustable;
selecting a preset only changes the draft until you save. See [Cache management](cache.md)
for retention, preload limits, pause behaviour, and storage accounting.
