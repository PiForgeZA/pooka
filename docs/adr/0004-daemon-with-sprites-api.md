# 0004: Phase 2 daemon speaks the Sprites API; official CLI compatibility confirmed

Status: accepted
Date: 2026-10-20

## Context

Phase 1 drives `pct` over root SSH. That is fine for one person and wrong for a second
person, a CI job or an agent on another machine. Phases 3 and 4 need a long-running
process on the host. ADR 0002 chose the Sprites vocabulary and left the API bet open.

A probe on 2026-10-20 (docs/sprites-api/README.md) showed that the official `sprite` CLI
works against a non-Fly server when `SPRITES_API_URL`, `SPRITE_TOKEN` and `SPRITE_ORG`
are set. List, create, info, destroy, checkpoint and restore all went to the stub with
bearer auth. Exec and console use a WebSocket with a documented frame protocol. This is
outcome 1 of handoff 0002.

## Decision

Phase 2 ships `pookad`, a daemon that runs on the Proxmox host and implements the
Sprites HTTP and WebSocket API for the endpoints listed in docs/sprites-api/README.md.
Other endpoints return 501 with a JSON error.

- One Go binary, same repo, same `Backend`. The LXC backend does not change. A new
  `host.Local` runs commands with `exec.Command` instead of SSH.
- Auth is a single bearer token read from a file on the host. One organization named
  `local`. `SPRITE_ORG` is accepted and ignored.
- Listens on a configurable address, default `0.0.0.0:7700`, plain HTTP. The host
  LAN is the trust boundary in phase 2. TLS arrives with the reverse proxy in phase 3.
- TTY exec runs `lxc-attach` under a pseudo-terminal on the host. No agent inside the
  container. `/.sprite/bin/sprite-console` maps to `bash -l`.
- Filesystem endpoints run as root inside the container, as Sprites does.
- Status: running containers are `running`, stopped ones are `cold`. No `warm`.
- The `pooka` CLI gains an HTTP backend selected by `daemon_url` and `token` in its
  config. SSH mode stays as the fallback and for template builds.
- Deployed by `scripts/install-daemon.sh` as a systemd unit, included in releases.

## Consequences

- Anyone with the token can use pooka with the official `sprite` CLI and SDKs by
  setting three environment variables. No pooka-specific client is required.
- pooka tracks the Sprites API version. docs/sprites-api holds the copy it follows.
- Exec sessions live in daemon memory. A daemon restart drops session bookkeeping but
  not the containers.
- Plain HTTP on the LAN means the token travels in clear text on that LAN. Phase 3
  must put TLS in front before anyone exposes the port further.
- Two new dependencies: a WebSocket library and a pty library.
