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
# ~/.local/bin holds claude and rtk.
export PATH="$HOME/.local/bin:$HOME/.local/share/mise/shims:$PATH"
case $- in *i*) eval "$(mise activate bash)";; esac
EOF
cat >>/home/pooka/.bashrc <<'EOF'
export PATH="$HOME/.local/bin:$HOME/.local/share/mise/shims:$PATH"
eval "$(mise activate bash)"
EOF
chown pooka:pooka /home/pooka/.bashrc

echo "== tools via mise (as pooka)"
sudo -iu pooka mise install --yes
sudo -iu pooka mise ls

echo "== claude code (native installer, as pooka)"
sudo -iu pooka bash -c 'curl -fsSL https://claude.ai/install.sh | bash'
sudo -iu pooka bash -lc 'claude --version'

echo "== rtk (as pooka)"
# The install script checks the release sha256 and writes ~/.local/bin/rtk.
# init adds the Claude Code hook, RTK.md and CLAUDE.md, then the Codex AGENTS.md.
sudo -iu pooka bash -lc 'curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh'
sudo -iu pooka bash -lc 'rtk --version && rtk init -g --auto-patch && rtk init -g --codex'

echo "== agent plugins (as pooka)"
# Skills and ponytail for both agents. Plugins update themselves when the agent starts.
# Codex does not trust a plugin's hooks until the user accepts them once in /hooks.
sudo -iu pooka bash -lc '
  set -e
  claude plugin marketplace add anthropics/claude-plugins-official
  claude plugin install mattpocock-skills@claude-plugins-official
  claude plugin marketplace add DietrichGebert/ponytail
  claude plugin install ponytail@ponytail
  codex plugin marketplace add mattpocock/skills
  codex plugin add mattpocock-skills@mattpocock
  codex plugin marketplace add DietrichGebert/ponytail
  codex plugin add ponytail@ponytail
'

echo "== claude code security hooks (as pooka)"
# install.sh copies the hook scripts to ~/.claude/hooks and places canary files.
# It only prints the settings block, so merge the hooks from its example here.
# The permissions in the example are not merged.
sudo -iu pooka bash -lc '
  set -e
  src=$HOME/.local/share/claude-code-security-hooks
  rm -rf "$src"
  git clone -q --depth 1 https://github.com/slavaspitsyn/claude-code-security-hooks.git "$src"
  (cd "$src" && ./install.sh >/dev/null)
  (cd "$src" && ./test_hooks.sh) || echo "warning: security hook self-test failed; hooks installed anyway"
  jq -s ".[0] * {hooks: {PreToolUse: ((.[0].hooks.PreToolUse // []) + .[1].hooks.PreToolUse)}}" \
    "$HOME/.claude/settings.json" "$src/settings-example.json" >"$HOME/.claude/settings.json.new"
  mv "$HOME/.claude/settings.json.new" "$HOME/.claude/settings.json"
'

echo "== tidy"
rm -rf /root/.npm /root/.cache /home/pooka/.npm /home/pooka/.cache
echo "provisioned $(date -Is)" >/etc/pooka/provisioned
