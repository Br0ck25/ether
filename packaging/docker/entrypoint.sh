#!/bin/sh
# Ether container entrypoint: runs the server as PUID:PGID (default 999:999,
# the image's ether account), the NAS convention (Synology, Unraid, TrueNAS).
#
# Started as root (the default), it makes the config and cache volumes belong
# to that user (only when their owner differs, so an existing volume is not
# walked on every start), adds the groups owning any /dev/dri nodes so
# VAAPI/QuickSync work without group_add, applies UMASK, then drops root for
# good. Started with --user / user:, it runs Ether as that user unchanged.
set -eu

ETHER=/usr/lib/ether/ether

if [ "$(id -u)" != 0 ]; then
	exec "$ETHER" "$@"
fi

PUID=${PUID:-999}
PGID=${PGID:-999}
case "$PUID$PGID" in
*[!0-9]*)
	echo "ether: PUID and PGID must be numeric (got PUID=$PUID PGID=$PGID)" >&2
	exit 64
	;;
esac
if [ "$PUID" = 0 ]; then
	echo "ether: warning: PUID=0 runs Ether as root" >&2
fi

if [ -n "${UMASK:-}" ]; then
	umask "$UMASK"
fi

for dir in "${ETHER_DATA_DIR:-/config}" "${XDG_CACHE_HOME:-/cache}"; do
	mkdir -p "$dir"
	if [ "$(stat -c %u:%g "$dir")" != "$PUID:$PGID" ]; then
		echo "ether: giving $dir to $PUID:$PGID" >&2
		chown -R "$PUID:$PGID" "$dir"
	fi
done
chmod 700 "${ETHER_DATA_DIR:-/config}"

# Supplementary groups: whoever owns the passed-through GPU nodes.
groups=""
for dev in /dev/dri/renderD* /dev/dri/card*; do
	[ -e "$dev" ] || continue
	gid=$(stat -c %g "$dev")
	case ",$groups," in
	*",$gid,"*) ;;
	*) groups="${groups:+$groups,}$gid" ;;
	esac
done

export HOME="${ETHER_DATA_DIR:-/config}"
if [ -n "$groups" ]; then
	exec setpriv --reuid="$PUID" --regid="$PGID" --groups="$groups" "$ETHER" "$@"
fi
exec setpriv --reuid="$PUID" --regid="$PGID" --clear-groups "$ETHER" "$@"
