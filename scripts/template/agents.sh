#!/usr/bin/env bash
# Runs inside the template container once, as the pooka user, from provision.sh.
# Installs the agent toolchain next to Claude Code and Codex: rtk, the
# mattpocock/skills and ponytail plugins for both agents, and the Claude Code
# security hooks. This is a file, not an inline string, because `sudo -i` expands
# `$` and joins lines before the shell sees them.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"

echo "== rtk"
# The install script checks the release sha256 and writes ~/.local/bin/rtk.
# init adds the Claude Code hook, RTK.md and CLAUDE.md, then the Codex AGENTS.md.
curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh
rtk --version
rtk init -g --auto-patch
rtk init -g --codex

echo "== agent plugins"
# Skills and ponytail for both agents. Plugins update themselves when the agent starts.
# Codex does not trust a plugin's hooks until the user accepts them once in /hooks.
claude plugin marketplace add anthropics/claude-plugins-official
claude plugin install mattpocock-skills@claude-plugins-official
claude plugin marketplace add DietrichGebert/ponytail
claude plugin install ponytail@ponytail
codex plugin marketplace add mattpocock/skills
codex plugin add mattpocock-skills@mattpocock
codex plugin marketplace add DietrichGebert/ponytail
codex plugin add ponytail@ponytail

echo "== claude code security hooks"
# install.sh copies the hook scripts to ~/.claude/hooks and places canary files.
# It only prints the settings block, so merge the hooks from its example here.
# The permissions in the example are not merged.
src=$HOME/.local/share/claude-code-security-hooks
rm -rf "$src"
git clone -q --depth 1 https://github.com/slavaspitsyn/claude-code-security-hooks.git "$src"
(cd "$src" && ./install.sh >/dev/null)
(cd "$src" && ./test_hooks.sh) || echo "warning: security hook self-test failed; hooks installed anyway"
jq -s '.[0] * {hooks: {PreToolUse: ((.[0].hooks.PreToolUse // []) + .[1].hooks.PreToolUse)}}' \
  "$HOME/.claude/settings.json" "$src/settings-example.json" >"$HOME/.claude/settings.json.new"
mv "$HOME/.claude/settings.json.new" "$HOME/.claude/settings.json"

echo "== agent toolchain done"
