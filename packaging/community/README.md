# NAS and homelab installation

Ether ships no accounts, providers, indexers, or media. Open `http://SERVER:32400/ether/`
and finish setup. Select `/cache` for the segment cache (otherwise it defaults to
`/config/cache`, inside the configuration volume). After changing this folder,
restart the container to activate it. The Cache page shows the active directory;
downloaded media segments go under its `data` subfolder. Artwork remains under
`/config/cache/images`, `/config/cache/posters` and `/config/cache/overlays`, so
those directories can still grow when the media cache is set to `/cache`.
Existing cached media is not moved automatically. Image tags are produced
by the existing release workflow; pin a published version or digest for repeatable
upgrades. These templates are installable examples, not submissions to the
platforms' public marketplaces.

## Docker Compose and Unraid

Use [compose.yaml](compose.yaml) on Linux. Host networking permits Plex GDM
UDP discovery; reserve TCP 32400 and UDP 32410–32414 on the host. Bridge mode
requires publishing TCP 32400 and manually adding the server in clients.

Ether runs as `PUID`:`PGID` (default 999:999). Set them to the user that
should own the config and cache folders; on start the container hands both
folders to that user, adds the group of any passed-through `/dev/dri` node,
and drops root before Ether starts. `UMASK` is honoured.

```sh
mkdir -p /var/lib/ether /var/cache/ether
PUID=$(id -u) PGID=$(id -g) docker compose -f packaging/community/compose.yaml up -d
```

Import [unraid.xml](unraid.xml) as a local Docker template (copy it to
`/boot/config/plugins/dockerMan/templates-user/my-ether.xml`). Set the configuration
path to a local cache-pool dataset. The template uses Unraid's usual
`PUID=99`/`PGID=100` (nobody:users). Media is mounted read-only. Add `/media` on Ether's Libraries page.

## TrueNAS SCALE

For Docker-based SCALE (24.10+), create local datasets, adjust the paths and ACLs
in [truenas-compose.yaml](truenas-compose.yaml), and install it through
Apps → Discover Apps → Custom App → Install via YAML. Set `PUID`/`PGID` to the
dataset owner (TrueNAS's `apps` user is 568); media needs read access for it. Use host
networking and ensure another Plex instance is not binding 32400. Older
Kubernetes-based SCALE releases should use a Linux VM with Compose.

## Proxmox

Review [proxmox.sh](proxmox.sh), download a Debian 12 template with Proxmox's
`pveam`, then supply an unused `VMID` and the template volume as documented at
the top of the script. The script creates an unprivileged container, installs
Debian's Docker package and starts Ether. It refuses an existing CT and never
changes existing guests. Allow outbound DNS/HTTP/HTTPS and your provider ports.
Back up the CT and Ether's own snapshots. GPU passthrough requires
host-specific device permissions and is intentionally explicit.

## Synology DSM

Use Container Manager → Project with the Compose example. Replace paths with
`/volume1/docker/ether/config` and `/volume1/docker/ether/cache`; use local Btrfs
or ext4 for config. Set `PUID`/`PGID` to the DSM user that owns those folders
(`id <user>` over SSH, typically `1026`/`100`). Host networking
supports discovery; stop another Plex package first or configure a different
Ether port. Mount `/volume1/video:/media:ro` for hybrid libraries. Older DSM
systems without Container Manager can use Docker's equivalent host-network
container options. Enable `/dev/dri` only when present and accessible; CPU direct
play works without it.

## Low-power hardware

Two CPU cores and 2 GiB RAM are a starting allocation for direct play. Cache
space depends on concurrent bitrate and desired rewind window: a 20 Mbit/s
stream uses about 150 MB per minute. Use a local SSD for config and cache; keep
NAS media mounts read-only. Prefer direct-play quality profiles. Intel QuickSync
or another supported hardware encoder is useful when clients require transcoding;
software 4K transcoding is unsuitable for small NAS CPUs. Map `/dev/dri` for VAAPI/QSV; the
container adds the render node's group itself. The transcode settings page reports
available accelerators; installed hardware alone does not guarantee permissions.

## Migration and backups

The Adoption page previews PMS imports before committing local changes. Supply
a separate access token for each viewer; a PMS owner token does not grant access
to friends' private history. Link existing Ether viewers through plex.tv sharing
first. Source PMS requests are GET-only. Unmatched GUIDs are reported for review.

Backups contain credentials. Keep the backup directory private, encrypt copies
sent off-host, and download bundles over an authenticated connection. Scheduled
snapshots use SQLite `VACUUM INTO` plus an integrity check and include config.
Retention applies only to Ether-created bundle files. Restoring validates hashes,
config and database integrity, then stages a new data directory. Stop Ether and
point `ETHER_DATA_DIR` at that directory to activate it; the running database is
never overwritten. Keep the live SQLite database off NFS/SMB; copied
backup bundles can reside there.
