#!/bin/sh
# Ether package postinstall: create the ether user/dirs, enable the service.
set -e

if ! getent group ether >/dev/null 2>&1; then
  groupadd --system ether || addgroup --system ether
fi
if ! getent passwd ether >/dev/null 2>&1; then
  useradd --system --gid ether --home-dir /var/lib/ether \
    --shell /usr/sbin/nologin --comment "Ether media server" ether ||
    adduser --system --group --home /var/lib/ether \
      --shell /usr/sbin/nologin ether
fi
# GPU access for VAAPI/QSV transcoding where the groups exist.
for g in render video; do
  if getent group "$g" >/dev/null 2>&1; then
    usermod -a -G "$g" ether || true
  fi
done

mkdir -p /var/lib/ether /var/cache/ether
chown ether:ether /var/lib/ether /var/cache/ether
chmod 0750 /var/lib/ether /var/cache/ether

if [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
  systemctl enable ether.service || true
  # Start on fresh install; restart on upgrade.
  if systemctl is-active --quiet ether.service; then
    systemctl restart ether.service || true
  else
    systemctl start ether.service || true
  fi
fi

echo "Ether installed. Open http://localhost:32400/ether/ to finish setup."
