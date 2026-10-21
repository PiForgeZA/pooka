#!/usr/bin/env bash
# Install or upgrade pookad on a Proxmox host. Run ON THE HOST as root:
#   bash install-daemon.sh /path/to/pookad
# Creates /etc/pooka/pookad.yaml and /etc/pooka/token on first run, installs the
# binary to /usr/local/bin, and (re)starts the systemd unit.
set -euo pipefail

BIN="${1:-}"
if [ -z "$BIN" ] || [ ! -f "$BIN" ]; then
  echo "usage: $0 /path/to/pookad" >&2
  exit 1
fi

install -d -m 0750 /etc/pooka
install -m 0755 "$BIN" /usr/local/bin/pookad

if [ ! -s /etc/pooka/token ]; then
  umask 077
  head -c 32 /dev/urandom | base64 | tr -d '/+=\n' | head -c 40 > /etc/pooka/token
  echo >> /etc/pooka/token
  echo "generated /etc/pooka/token"
fi
chmod 0600 /etc/pooka/token

if [ ! -f /etc/pooka/pookad.yaml ]; then
  cat > /etc/pooka/pookad.yaml <<'EOF'
# pookad configuration. Every key is optional; these are the defaults.
listen: 0.0.0.0:7700
token_file: /etc/pooka/token
org: local
template: 9000
vmid_range: [9100, 9199]
disk_gb: 32
cores: 4
memory_mb: 8192
# ssh_pubkey: /root/.ssh/pooka-users.pub   # injected into every new environment
EOF
  echo "wrote /etc/pooka/pookad.yaml"
fi

cat > /etc/systemd/system/pookad.service <<'EOF'
[Unit]
Description=pooka daemon (Sprites-compatible API for Proxmox LXC environments)
After=network-online.target pve-cluster.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/pookad --config /etc/pooka/pookad.yaml
Restart=on-failure
RestartSec=2
# pct and lxc-attach need root.
User=root
NoNewPrivileges=no

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now pookad.service
systemctl restart pookad.service
sleep 1
systemctl --no-pager --lines=3 status pookad.service || true
echo
echo "pookad installed. Token: /etc/pooka/token"
echo "Clients: export SPRITES_API_URL=http://$(hostname -I | awk '{print $1}'):7700 SPRITE_TOKEN=\$(cat /etc/pooka/token) SPRITE_ORG=local"
