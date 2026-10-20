#!/usr/bin/env bash
# Runs once inside a new environment, as root, right after `pooka create`.
# pooka passes POOKA_NAME. Add your own personalisation below.
set -euo pipefail

MARKER=/etc/pooka/firstboot.done
[ -e "$MARKER" ] && exit 0

# Regenerate identity that the template build wiped.
systemd-machine-id-setup >/dev/null 2>&1 || true
if command -v ssh-keygen >/dev/null && [ -d /etc/ssh ]; then
  ssh-keygen -A >/dev/null 2>&1 || true
fi

# SSH access for the creator, if a public key was provided.
if [ -n "${POOKA_SSH_PUBKEY:-}" ]; then
  install -d -m 0700 -o pooka -g pooka /home/pooka/.ssh
  printf '%s\n' "$POOKA_SSH_PUBKEY" >>/home/pooka/.ssh/authorized_keys
  chmod 0600 /home/pooka/.ssh/authorized_keys
  chown pooka:pooka /home/pooka/.ssh/authorized_keys
  systemctl restart ssh >/dev/null 2>&1 || true
fi

# Per-environment git identity, if provided.
if [ -n "${GIT_USER_NAME:-}" ]; then
  sudo -u pooka git config --global user.name "$GIT_USER_NAME"
fi
if [ -n "${GIT_USER_EMAIL:-}" ]; then
  sudo -u pooka git config --global user.email "$GIT_USER_EMAIL"
fi

echo "${POOKA_NAME:-unknown} $(date -Is)" >"$MARKER"
