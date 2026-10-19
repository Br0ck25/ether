# Installing Ether

Ether is currently pre-release. **No desktop installers or release packages
have been published yet.** Check the [Releases page](https://github.com/braedonsaunders/ether/releases)
for available downloads before following the package instructions below.

The planned package formats are a Windows setup EXE, a macOS DMG, Linux DEB/RPM
and Docker images. Packaging configuration exists; this is not a claim that
these packages have been built, signed or tested on your device.

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
   (`~/Library/LaunchAgents/io.github.braedonsaunders.ether.plist`) that runs the
   server (`Ether.app/Contents/Resources/ether`) while you are logged in.
3. Data lives in `~/Library/Application Support/Ether`, logs in
   `~/Library/Logs/Ether`.

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
  headers intact and point *Custom connection URLs* at the proxy.

## Running a cluster

Two or more Ether nodes on one network can serve as one Plex server,
sharing an owner-supplied PostgreSQL database. A single server needs none
of this.

**Requirements**

- PostgreSQL 14 or newer, with a dedicated, empty database for Ether.
  Create it with the C collation (`CREATE DATABASE ether LC_COLLATE 'C'
  LC_CTYPE 'C' TEMPLATE template0`) so text sorts as on a single server.
- Ether does not make PostgreSQL highly available; replication and
  failover of the database are yours to run.
- Every node must reach the database (allow it through the firewall;
  prefer `sslmode=require` off a trusted LAN) and the nodes must be on
  the same LAN.
- For remote access each node needs its own port forward (a different
  external port per node, or UPnP), set on that node.

**Start a cluster from an existing server**

```sh
# Stop Ether first (service stop, or quit the tray app), then:
ether cluster init --database-url 'postgres://ether:PASSWORD@db.lan:5432/ether?sslmode=require'
# start Ether again
```

`init` copies the server's database into PostgreSQL (it refuses a
database that already holds data), shares its settings and identity from
there and points `config.json` at PostgreSQL. A brand-new install can
instead start with `ETHER_DATABASE_URL` set to the empty database; the
first node to start seeds the cluster. Take a backup beforehand.

**Add a node**

On a running node, create a one-time join token, valid for one hour:

```sh
ether cluster token          # or POST /ether/api/cluster/join-tokens
```

Install Ether on the new machine, stop it, then run:

```sh
ether cluster join 'ether-join-v1:...'
```

and start it. The token is a secret: it carries the database password and
the credential key that decrypts every stored credential, so treat it like
a password and use it promptly. A used or expired token is refused; create
another. `join` refuses to overwrite an already configured server unless
you pass `--force` (its old settings are kept as `*.pre-join-*` files). If
the new node reaches the database under a different address, add
`--database-url`. Joining from the web setup wizard is not available.

**What each node keeps for itself:** port and bind address, log level,
cache location and size, transcoding hardware, ffmpeg paths and
transcode limit, the public port/host and UPnP for its port forward,
custom certificate file paths and the database URL. Everything else
(providers, indexers, metadata keys, policies, the server identity and
plex.tv sign-in, update channel) is shared: change it on any node and
the others follow (within 30 seconds at most). A node's `config.json` keeps a copy of
the shared settings, which is replaced from the database at every start.
Local media folders are shared, so mount them at the same path on every
node.

**Remove a node:** stop and uninstall it, then delete its data directory
(it holds the cluster's credential key). Unused tokens can be revoked with
`DELETE /ether/api/cluster/join-tokens/{id}`. A node whose credential key
does not match the cluster's refuses to start; join it with a fresh token.

## Updates

The tray checks a signed update feed (stable/beta channel, chosen in the
web UI). Depending on your setting it prompts before updating or applies
updates automatically: a verified binary self-replace (with automatic
rollback copy) or the platform installer (DMG/setup.exe). Linux packages
update through your package manager or a new Docker image. Nothing is
ever installed without a valid ed25519 signature and SHA-256 match.
