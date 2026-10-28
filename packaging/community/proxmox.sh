#!/usr/bin/env bash
# Run on the Proxmox host after review. Requires an existing Debian 12 template.
# VMID=215 TEMPLATE=local:vztmpl/debian-12-standard_...tar.zst ./proxmox.sh
set -euo pipefail
: "${VMID:?Set an unused VMID}"
: "${TEMPLATE:?Set the Debian 12 template volume (pveam list local)}"
STORAGE=${STORAGE:-local-lvm}
BRIDGE=${BRIDGE:-vmbr0}
IMAGE=${IMAGE:-ghcr.io/braedonsaunders/ether:latest}
MEMORY=${MEMORY:-2048}
CORES=${CORES:-2}
command -v pct >/dev/null
if pct status "$VMID" >/dev/null 2>&1; then
  echo "VMID $VMID already exists; refusing to replace it" >&2
  exit 1
fi
pct create "$VMID" "$TEMPLATE" --hostname ether --unprivileged 1 \
  --features nesting=1,keyctl=1 --memory "$MEMORY" --cores "$CORES" \
  --rootfs "$STORAGE:16" --net0 "name=eth0,bridge=$BRIDGE,ip=dhcp,type=veth" \
  --onboot 1
pct start "$VMID"
pct exec "$VMID" -- bash -ceu 'apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io ca-certificates; systemctl enable --now docker; mkdir -p /var/lib/ether /var/cache/ether'
# Initialize volume ownership using the account shipped in the image.
pct exec "$VMID" -- docker run --rm --user root --entrypoint sh \
  -v /var/lib/ether:/config -v /var/cache/ether:/cache "$IMAGE" \
  -c 'chown ether:ether /config /cache; chmod 700 /config'
pct exec "$VMID" -- docker run -d --name ether --network host \
  --restart unless-stopped -e ETHER_DATA_DIR=/config \
  -v /var/lib/ether:/config -v /var/cache/ether:/cache "$IMAGE"
echo "Ether is running in CT $VMID. Open http://<container-ip>:32400/ether/."
