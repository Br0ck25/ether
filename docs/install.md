# Installing Ether

Download a macOS DMG, Windows setup EXE, Linux DEB/RPM, or standalone
archive from the [Releases page](https://github.com/braedonsaunders/ether/releases/latest).
Docker images are available at `ghcr.io/braedonsaunders/ether`. Packaged
installers and archives bundle FFmpeg and FFprobe.

The v0.2.1 macOS app is ad-hoc signed and **not notarised**; macOS may require
**System Settings › Privacy & Security › Open Anyway** after you attempt to
open it. The Windows installer is unsigned and may trigger SmartScreen.
The automatic-update manifest is signed separately with Ether's release key.

If you already have an Ether executable, run it and open
<http://localhost:32400/ether/>. The setup wizard walks you through Plex sign-in,
provider and indexer configuration, and cache settings. A standalone executable
needs access to FFmpeg and FFprobe for media processing.

After any install, open the web UI and finish the setup wizard (sign in
to plex.tv, add your Usenet providers and Newznab indexers, pick a cache
folder). Ether ships no content, providers or indexers; see
[LEGAL.md](../LEGAL.md).

## Windows

1. Download `EtherSetup-<version>.exe` from the releases page and run it
   (administrator approval is needed to register the service).
2. Ether installs to `C:\Program Files\Ether`, registers the **Ether**
   Windows service (delayed automatic start; restarted automatically if
   it ever stops), adds a firewall rule for TCP 32400, and puts the Ether
   tray icon in your login items.
3. Settings, database, cache and logs live in `%ProgramData%\Ether`.
   The folder holds credentials, so it is readable only by SYSTEM and
   Administrators. Uninstalling keeps this data; delete the folder to
   purge.

Manage the service with `ether.exe service start|stop|restart|status`
from an elevated prompt, or from the tray menu.

## macOS

1. Download `Ether-<version>.dmg`, open it, drag **Ether** to
   Applications, and launch it. Ether appears as a menu-bar icon (no
   Dock icon).
2. On first launch the tray installs a per-user LaunchAgent
   (`~/Library/LaunchAgents/ether.plist`) that runs the
   server (`Ether.app/Contents/Resources/ether`) while you are logged in.
3. Data lives in `~/Library/Application Support/Ether`, logs in
   `~/Library/Application Support/Ether/logs`.

Requires macOS 13+. Signing and notarisation depend on the published package;
check its release notes before installing.

## Linux (.deb / .rpm)

```sh
sudo apt install ./ether_<version>_linux_amd64.deb    # Debian/Ubuntu
sudo dnf install ./ether_<version>_linux_amd64.rpm    # Fedora/RHEL
```

The package creates a dedicated `ether` system user, installs to
`/usr/lib/ether`, enables and starts `ether.service` (systemd), and
keeps data in `/var/lib/ether` and cache in `/var/cache/ether`.
`systemctl status ether` shows state; `journalctl -u ether` shows logs.
Upgrades are just installing the new package (or a new Docker image) —
Linux packages do not self-update.

For hardware transcoding, the `ether` user is added to the `render` and
`video` groups when they exist (VAAPI/QuickSync). NVIDIA needs the
proprietary driver visible to ffmpeg as usual.

## Docker

```sh
cp docker-compose.example.yml docker-compose.yml
docker compose up -d
```

or plain Docker:

```sh
docker run -d --name ether --restart unless-stopped \
  --network host \
  -v /path/to/config:/config -v /path/to/cache:/cache \
  ghcr.io/braedonsaunders/ether:latest
```

**Host networking is recommended.** Plex apps discover servers on the
LAN with GDM (UDP 32410–32414) and Ether's remote-access setup talks to
your router over UPnP; both need the host network namespace. Bridge mode
with `-p 32400:32400` works for Plex Web and for claimed/remote
connections, but LAN auto-discovery will not find the server.

Volumes: `/config` (config, database, identity — back this up) and
`/cache` (segment cache; budget is set in the web UI). VAAPI/QSV: pass
`--device /dev/dri:/dev/dri`. NVENC: use the NVIDIA container toolkit
flags shown in `docker-compose.example.yml`.

## From a tarball/zip (any OS)

Release archives contain `ether`, the tray (Linux/Windows), `ffmpeg` and
`ffprobe` (must stay next to the `ether` binary — that's where it looks
first). Unpack anywhere, then either run `./ether` directly or register
the service:

```sh
sudo ./ether service install   # systemd / launchd / Windows SCM
sudo ./ether service start
./ether service status
```

`ETHER_DATA_DIR` (or `--data-dir`, which `ether service install
--data-dir DIR` bakes into the service) overrides the data directory
(defaults: `/var/lib/ether` as root on Linux,
`~/Library/Application Support/Ether` on macOS, `%ProgramData%\Ether`
on Windows). Logs are written to `<data dir>/logs/ether.log` (rotated)
and fatal crash traces to `<data dir>/logs/crash.log`.

## Remote access / port forwarding

Ether uses **TCP 32400** for playback. For remote access
outside your LAN:

- Easiest: enable *Remote access* in the web UI — Ether asks your router
  for a UPnP mapping and publishes the address to plex.tv, for Plex clients.
- Manual: forward one external TCP port (e.g. 32400) to the Ether host's
  32400, then set that public port in the web UI.
- LAN discovery additionally uses UDP 32410–32414 (GDM) — only relevant
  inside your network, never forward it.
- Behind a reverse proxy/TLS terminator, keep the path and `X-Plex-*`
  headers intact and point *Custom connection URLs* (or *Public host* on
  port 443 with *Secure connections* set to *Required*) at your `https://`
  domain; Ether publishes that HTTPS endpoint directly to plex.tv without
  requiring a `plex.direct` certificate.

## Running a cluster

A cluster is two or more Ether servers ("nodes") on one network that share a
PostgreSQL database and appear in Plex as a single server. Viewers can be
served by any node; if one stops, their apps carry on through the others. A
single server needs none of this: it keeps everything in its own SQLite
database.

The **Cluster** page in the web UI (under *Server*) shows whether a server is
standalone or a node, lists the nodes and their health, and creates the join
tokens new nodes use.

### What you need

- **PostgreSQL 14 or newer**, with a dedicated, empty database for Ether.
  Create it with the C collation so text sorts exactly as on a single server:

  ```sql
  CREATE DATABASE ether LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0;
  ```

- **A database that stays up.** Ether does not install, replicate or fail over
  PostgreSQL; if the database goes away, so does the cluster's shared state.
  Run it highly available yourself, for example with Patroni, CloudNativePG
  on Kubernetes, or a managed PostgreSQL service. Ether tolerates brief
  database outages without ending streams, but not long ones.
- **Nodes on the same LAN.** Every node must reach the database (allow it
  through the firewall; use `sslmode=require` unless the network is trusted).
- **A port forward per node** for remote access: each node needs its own
  external port (for example 32400 to the first node, 32401 to the second),
  or UPnP. Set the public port on each node's *Plex & remote access* page.
- **One public IP for debrid.** Debrid services may flag one account used
  from several addresses at once. Keep every node behind the same internet
  connection; the Cluster page warns you when nodes reach the internet from
  different public addresses.

### Start a cluster from an existing server

Take a backup first (*Backups & import › Create snapshot*). Then, on the
server:

```sh
# 1. Stop Ether (stop the service, or quit the tray app).
# 2. Copy this server into PostgreSQL and make it the first node:
ether cluster init --database-url 'postgres://ether:PASSWORD@db.lan:5432/ether?sslmode=require'
# 3. Start Ether again.
```

`init` copies the server's data into PostgreSQL (it refuses a database that
already holds data), moves its settings and identity there and points
`config.json` at PostgreSQL. Your viewers, history, playlists and Plex sign-in
come along; Plex apps keep seeing the same server. Open the Cluster page to
confirm the server now shows as a node and the leader.

A brand-new install can instead start with `ETHER_DATABASE_URL` set to the
empty database; the first node to start seeds the cluster.

### Add a node

1. On the **Cluster** page of any running node, choose **Create join token**,
   pick how long it stays valid (at most an hour) and create it. The token is
   shown **once**, with the command to run. (On the command line:
   `ether cluster token`.)
2. Install Ether on the new machine and stop it.
3. Run the command from the drawer:

   ```sh
   ether cluster join 'ether-join-v1:...'
   ```

4. Start Ether on the new machine and forward its own port. It appears on the
   Cluster page within a few seconds.

The token is a secret: it carries the database password and the credential
key that decrypts every stored credential. Treat it like a password, use it
promptly and don't paste it into chats or tickets. A token works once; a used
or expired one is refused, so create another. Unused tokens can be revoked
from the Cluster page.

`join` refuses to overwrite an already configured server unless you pass
`--force` (its old settings are kept as `*.pre-join-*` files). If the new
machine reaches the database at a different address, add
`--database-url 'postgres://…'`. Joining from the web setup wizard is not
available.

### Settings: shared and per node

Almost everything is shared: providers, indexers, metadata keys, policies,
viewers, the server identity and plex.tv sign-in, the update channel. Change
it on any node and the others follow within 30 seconds.

Each node keeps its own port and listen address, log level, cache location
and size, transcoding hardware, ffmpeg paths and transcode limit, public
port/host and UPnP for its port forward, custom certificate file paths, the
database URL and its node weight. Edit these on that node's own pages (or in
its `config.json`). A node's `config.json` also keeps a copy of the shared
settings, which is replaced from the database at every start.

### Node weight

Usenet connection counts, debrid API limits and TMDB request rates are
account-wide, so the live nodes divide them between them; the provider pages
show each node's part ("this node: 7 of 20 connections"). When a node leaves,
its share returns to the others within about 30 seconds.

There is nothing extra to configure. Enter each provider's connection limit
once, as you would for a single server, and the cluster divides it. Every live
node gets at least one connection per provider. The warm connections a node
keeps open for fast starts count towards its own share and never exceed it.
Debrid API calls (adding torrents, creating links) are limited per account and
divided in the same way. The number of connections a node uses to read one
debrid file is a per-node setting and is not divided: it only changes how
quickly a single stream fills. Indexer daily limits are counted in the shared
database, so together the nodes never exceed an indexer's quota.

To give a node a larger share, raise its **Node weight** on that node's
*General* page (`server.clusterWeight` in its `config.json`). A node at `2`
gets twice the share of a node at the default of `1`. Weights are listed on
the Cluster page.

### Backups

Scheduled backups run on whichever node leads and are saved on that node.
The backup list on any node shows every node's backups and which node holds
each one; download a backup from the node that holds it. To keep them in one
place, set a **Shared backup folder** on *Backups & import*: a folder every
node reaches at the same path, such as a network share.

### When a node fails

Plex apps know every node's address. When a node stops or loses its network,
apps reconnect through another node and playback resumes from the last
reported position, usually after a brief pause. A stream that was cut off
mid-response restarts rather than continuing seamlessly. If the node that
failed was the leader, another node takes over plex.tv publishing and
scheduled work within seconds.

The Cluster page shows a stopped node as offline with when it was last seen.
A cleanly stopped node leaves the list immediately; one that crashed stays
listed as offline for up to a day.

### Remove a node

Stop Ether on it and uninstall it, then delete its data directory (it holds
the cluster's credential key). Its share of provider connections returns to
the other nodes. Revoke any join tokens you no longer need.

**Never copy a node's data directory** to make another node: two nodes with
the same `node.json` would overwrite each other, so the copy refuses to start.
If that happens, stop the copy, delete `node.json` from its data directory and
start it again; it joins as a new node. A node whose credential key does not
match the cluster's also refuses to start; join it with a fresh token.

### Updating a cluster

Nodes update one at a time during the maintenance window, so the others keep
serving viewers while each one restarts. A node rejoins only once its database
changes are compatible with the rest of the cluster. If you update by hand
(for example with your package manager or a new Docker image), do the same:
update and restart one node, wait until it shows as online on the Cluster
page, then move on to the next.

## Updates

The tray checks a signed update feed (stable/beta channel, chosen in the
web UI). Depending on your setting it prompts before updating or applies
updates automatically: a verified binary self-replace (with automatic
rollback copy) or the platform installer (DMG/setup.exe). Linux packages
update through your package manager or a new Docker image. Nothing is
ever installed without a valid ed25519 signature and SHA-256 match.
