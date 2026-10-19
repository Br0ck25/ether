#!/bin/sh
# Ether package preremove: stop the service. Data in /var/lib/ether is kept
# (remove it manually to purge configuration, database and cache).
set -e

if [ -d /run/systemd/system ]; then
  # $1 is "remove"/"purge" (deb) or a count (rpm: 0 = final erase).
  case "${1:-}" in
    purge|remove|0|"")
      systemctl stop ether.service || true
      systemctl disable ether.service || true
      ;;
  esac
fi
exit 0
