# 0012: pooka installs SSH keys; dotfiles carry configuration, not access

Status: accepted
Date: 2026-11-19

## Context

An environment gets one public key: the contents of the `ssh_pubkey` file on the
machine that ran `create`. Firstboot appends it once. A second machine with a
different key cannot log in. The user hit this with an SSH-driven tool on a laptop
against an environment made from a desktop VM, and had been adding keys by hand.

A dotfiles mechanism is still an open decision (chezmoi or stow). A dotfiles repo
could carry `authorized_keys`.

## Decision

pooka installs SSH keys. The `ssh_pubkey` file may hold several keys, one per line.
Firstboot appends them all. The user keeps the same multi-key file on every machine.
An existing environment takes a new key through `pooka exec --root`.

Dotfiles, when they arrive, carry configuration: shell, editor, git identity. They
do not carry keys or any other credential.

## Consequences

- No code change. The README and SPEC section 8 document the multi-key file.
- Access stays visible in pooka's own config and in `authorized_keys` inside the
  environment, with no dependency on a repo clone at firstboot.
- A private dotfiles repo would need a deploy key, which is the same problem again.
  Keeping keys out of dotfiles avoids that loop.
- The host-side `ssh_pubkey` in `pookad.yaml` stays a fallback for a client that
  sends no key. Merging it with the client's keys is a possible later change.
