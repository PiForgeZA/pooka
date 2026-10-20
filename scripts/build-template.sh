#!/usr/bin/env bash
# Build the pooka template container on a Proxmox host.
# Run this ON THE HOST as root:  bash build-template.sh
# See SPEC.md section 7.
set -euo pipefail

VMID="${POOKA_TEMPLATE_VMID:-9000}"
STORAGE="${POOKA_STORAGE:-local-lvm}"
BRIDGE="${POOKA_BRIDGE:-vmbr0}"
OSTEMPLATE="${POOKA_OSTEMPLATE:-local:vztmpl/ubuntu-24.04-standard_24.04-2_amd64.tar.zst}"
DISK_GB="${POOKA_TEMPLATE_DISK_GB:-16}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log() { printf '\n==> %s\n' "$*"; }

if pct status "$VMID" >/dev/null 2>&1; then
  echo "VMID $VMID already exists. Destroy it first or set POOKA_TEMPLATE_VMID." >&2
  exit 1
fi

stype="$(pvesm status | awk -v s="$STORAGE" '$1==s{print $2}')"
case "$stype" in
  lvmthin|zfspool) ;;
  *) echo "Storage $STORAGE is type '$stype'. Need lvmthin or zfspool." >&2; exit 1 ;;
esac

log "Creating container $VMID from $OSTEMPLATE on $STORAGE"
pct create "$VMID" "$OSTEMPLATE" \
  --hostname pooka-template \
  --unprivileged 1 \
  --features nesting=1,keyctl=1 \
  --cores 4 --memory 4096 --swap 512 \
  --rootfs "$STORAGE:$DISK_GB" \
  --net0 "name=eth0,bridge=$BRIDGE,ip=dhcp" \
  --ostype ubuntu \
  --tags pooka-template \
  --description "pooka template. Built $(date -Is) by scripts/build-template.sh"

log "Starting"
pct start "$VMID"

log "Waiting for network"
for _ in $(seq 1 30); do
  if pct exec "$VMID" -- sh -c 'getent hosts deb.debian.org archive.ubuntu.com >/dev/null 2>&1'; then break; fi
  sleep 1
done

log "Pushing provision and firstboot scripts"
pct exec "$VMID" -- mkdir -p /etc/pooka
pct push "$VMID" "$HERE/template/provision.sh" /etc/pooka/provision.sh --perms 0755
pct push "$VMID" "$HERE/template/firstboot.sh" /etc/pooka/firstboot.sh --perms 0755
pct push "$VMID" "$HERE/template/mise.toml" /etc/pooka/mise.toml --perms 0644

log "Provisioning (this takes a few minutes)"
pct exec "$VMID" -- /etc/pooka/provision.sh

log "Cleaning"
pct exec "$VMID" -- sh -c '
  apt-get clean
  rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*
  find /var/log -type f -exec truncate -s0 {} +
  rm -f /etc/ssh/ssh_host_*
  truncate -s0 /etc/machine-id
  rm -f /var/lib/dbus/machine-id
  rm -f /root/.bash_history /home/pooka/.bash_history
'

log "Stopping and converting to template"
pct shutdown "$VMID" --timeout 30 || pct stop "$VMID"
pct template "$VMID"

log "Done. Template VMID: $VMID"
pct config "$VMID"
