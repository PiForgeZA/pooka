# 0007: `pooka setup` provisions the host; the host scripts ship inside the binary

Status: accepted
Date: 2026-10-21

## Context

Getting from nothing to a working environment took five manual steps: build the
CLI, write a config file, copy `scripts/` to the host, run the template build, run
the daemon install, then copy the token back. Each step had a place to go wrong, and
the README was the only guide. Comparable tools install with one `curl | sh` line.

The host side needs choices a script cannot guess: the storage, the bridge, the
template VMID, the disk size, whether to run the daemon. A shell installer can ask
questions by reading `/dev/tty`, but the logic is better tested in Go, and the CLI
already has the SSH transport, the config loader and the defaults.

## Decision

- `install.sh` at the repository root installs only the CLI. It detects OS and
  architecture, downloads the release archive, verifies it against the release's
  `checksums.txt`, and puts `pooka` in `~/.local/bin`. It never needs root and
  never touches system directories. It ends by pointing at `pooka setup`.
- `pooka setup` does the rest, interactively. It connects over SSH, reads the
  facts it needs from the host (`pvesm status`, bridges, address, architecture),
  asks each question with a default, builds the template if the VMID does not
  exist, installs `pookad`, copies the token to `~/.config/pooka/token`, writes
  `~/.config/pooka/config.yaml`, and verifies with a list. Every answer is also a
  flag. `--yes` takes the flags and defaults without asking, for scripts and agents.
- The host scripts (`scripts/*.sh`, `scripts/template/*`) are embedded in the
  `pooka` binary with `go:embed` and copied to `/root/pooka-scripts` on the host.
  A user needs no checkout and no archive to provision a host. The scripts stay in
  the release archive too, for the manual route.
- `pookad` for the host comes from the GitHub release that matches the CLI version,
  downloaded on the host with `curl`. A dev build takes the latest release.
  `--pookad <file>` pushes a local binary instead, for development.
- `pooka setup` writes `/etc/pooka/pookad.yaml` with the chosen defaults only when
  the host has none. An existing file is kept. The daemon's own defaults are the
  host's truth, as ADR 0005 decided.

## Consequences

- The quick start is two commands: the install line and `pooka setup`. The README
  keeps the manual route under its own heading.
- The CLI binary grows by the size of the scripts, about 10 KB.
- `pooka setup` is the one command that runs without a config and without a
  backend; the root command's pre-run skips it.
- A new release of the CLI implies a matching `pookad` release, because `setup`
  downloads by version. The GoReleaser config already builds both from one tag.
- `shellcheck` in CI now covers `install.sh`.
