---
date: 2026-10-20
from: Claude Fable 5.1
focus: Start phase 2, a host daemon that speaks the Sprites HTTP API
---

# 0002: Phase 2, host daemon with the Sprites API

## State at the end of phase 1

- `v0.1.0` released from `main` (commit `3761ffb`). GoReleaser publishes Linux and macOS
  archives for amd64 and arm64 with `scripts/` inside. Verified: the Linux binary runs
  against the host.
- The CLI covers every SPEC section 10 command. Lifecycle verified end to end on the
  host. 23 unit tests on a fake host. CI: gofmt, vet, race tests, shellcheck,
  cross-compile matrix, goreleaser check.
- Repo rules: `main` protected, six required checks, squash merge only, PR title is the
  conventional commit, branches auto-delete. See AGENTS.md "Branches and merging".
- Template 9000 on `local-lvm`, 32 GB thin, Ubuntu 24.04, mise-managed Node 22, Python
  3.13, Go 1.27, Codex, herdr, Claude Code via native installer, sshd keys-only.
  Scripts live on the host at `/root/pooka-scripts`. Rebuild: `pct destroy 9000 --purge`
  then `bash /root/pooka-scripts/build-template.sh`.
- Numbers (docs/benchmarks.md): create 8.7 s, checkpoint 0.9 s, restore about 7 s,
  exec under 0.1 s. Remaining create fat: four `pct` calls at 0.55 s each, and about
  2.5 s waiting for systemd.

## Known gaps in phase 1

- `pooka console` was verified non-interactively only. Try it from a real terminal.
- No `ls --watch`, no idle sleep, no services.
- Environments get a DHCP address from `vmbr0`. Nothing records it; `ls` asks `lxc-info`.
- `push` and `pull` copy through `/tmp` on the host. Fine for files, not for trees.

## Phase 2 goal

A daemon on the Proxmox host with an HTTP API and bearer tokens. The `pooka` CLI becomes
an HTTP client of the daemon. The LXC backend code moves unchanged behind the daemon;
`Host` becomes a local exec instead of SSH. See ADR 0002 and SPEC section 16.

The daemon is the deliverable on its own: it removes the need for root SSH from every
client, gives agents and scripts a stable API, and is the long-running process that
phases 3 and 4 need.

Sprites API compatibility is an experiment inside phase 2, not its success condition.
The API follows the Sprites JSON shapes either way, because that costs nothing. ADR 0004
records one of three outcomes:

1. The official `sprite` CLI and the SDKs both work against pooka.
2. The SDKs work, the CLI does not. `pooka` stays the CLI.
3. Neither works cleanly. pooka has its own API modelled on Sprites, and the ADR says why.

## Progress, 2026-10-21

Steps 1 to 5 and 7 are done. `pookad` runs on the host as a systemd unit. The official
`sprite` CLI completes the full lifecycle against it: create, ls, info, exec with
stdin, TTY and http-post, checkpoint create and list, restore, destroy. ADR 0004 is
accepted with outcome 1. See docs/sprites-api/README.md for the endpoint map and the
one known limitation (attach by session id, used by `exec --no-stdin`).

Fixed on the way: the backend no longer reads the cluster resources endpoint, which
lags several seconds and made fresh environments "not found"; it reads `pct list` and
the config files instead. Proxmox stores the description as leading `#` lines.

Remaining from the original list: step 6 (HTTP backend in the `pooka` CLI) and step 8
(SPEC updates are done; keep them current).

## Next steps in order

1. Fetch and read the Sprites specs: https://docs.fly.io/sprites/api/openapi.json and
   https://docs.fly.io/sprites/api/asyncapi.json. Save them under `docs/sprites-api/`.
   List which endpoints map to the existing `Backend` and which need new work.
2. Write ADR 0004: daemon scope for phase 2. Decide the minimum endpoint set
   (sprites create/list/get/delete, exec over HTTP, checkpoints create/list/restore,
   filesystem read/write) and what is deferred (connectors, services, policies,
   websockets).
3. Verify the compatibility bet early: install the official `sprite` CLI, run
   `sprite login <pooka-url>` against a stub server, and record what the auth flow
   expects. If it cannot work, ADR 0004 says so and the SDKs become the target instead.
4. Add a `Host` implementation that runs commands locally (`exec.Command`). The daemon
   uses it; the LXC backend does not change.
5. Scaffold `cmd/pookad`: HTTP server, bearer token auth, the endpoint set from step 2,
   served on the host. Reuse the Sprites JSON shapes exactly.
6. Add an HTTP `Backend` client in the CLI, selected by a `daemon_url` config key. SSH
   mode stays as the fallback.
7. Deploy as a systemd unit on the host (`scripts/install-daemon.sh`). Add it to the
   GoReleaser archive.
8. Update SPEC.md to a phase 2 section, keep section 10 for SSH mode.

Optional, any time: cut create time by replacing the four `pct` calls with one
`pvesh` call where possible, and by masking unneeded systemd units in the template.

## Suggested skills

- `mattpocock-skills:research` for step 1.
- `grill-me` before writing ADR 0004.
- `tdd` for the daemon handlers with the fake host.
- `to-issues` to split steps 4 to 8 into tracker issues once ADR 0004 is accepted.
