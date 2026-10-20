#!/usr/bin/env bash
# Runs inside the template container once, as root. Installs the tool set.
# Language runtimes and agents come from mise, driven by /etc/mise/config.toml
# (scripts/template/mise.toml in the repo). Edit that file to change the tool set.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "== base packages"
apt-get update
apt-get -y upgrade
apt-get -y install --no-install-recommends \
  ca-certificates curl wget gnupg sudo locales tzdata \
  git build-essential pkg-config \
  vim nano tmux htop jq ripgrep fd-find fzf unzip zip tree less \
  python3 openssh-client openssh-server rsync \
  libssl-dev zlib1g-dev libbz2-dev libreadline-dev libsqlite3-dev libffi-dev liblzma-dev

locale-gen en_US.UTF-8 >/dev/null
update-locale LANG=en_US.UTF-8
ln -sf /usr/bin/fdfind /usr/local/bin/fd

echo "== user pooka"
if ! id pooka >/dev/null 2>&1; then
  useradd -m -s /bin/bash -G sudo pooka
fi
echo 'pooka ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/pooka
chmod 0440 /etc/sudoers.d/pooka

echo "== sshd (keys only, no root)"
cat >/etc/ssh/sshd_config.d/pooka.conf <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
EOF
systemctl enable ssh >/dev/null 2>&1 || true
install -d -m 0700 -o pooka -g pooka /home/pooka/.ssh

echo "== mise"
curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh
mkdir -p /etc/mise
install -m 0644 /etc/pooka/mise.toml /etc/mise/config.toml
cat >/etc/profile.d/mise.sh <<'EOF'
# pooka: mise shims for non-interactive shells, activation for interactive ones.
export PATH="$HOME/.local/share/mise/shims:$PATH"
case $- in *i*) eval "$(mise activate bash)";; esac
EOF
cat >>/home/pooka/.bashrc <<'EOF'
export PATH="$HOME/.local/share/mise/shims:$PATH"
eval "$(mise activate bash)"
EOF
chown pooka:pooka /home/pooka/.bashrc

echo "== tools via mise (as pooka)"
sudo -iu pooka mise install --yes
sudo -iu pooka mise ls

echo "== tidy"
rm -rf /root/.npm /root/.cache /home/pooka/.npm /home/pooka/.cache
echo "provisioned $(date -Is)" >/etc/pooka/provisioned
