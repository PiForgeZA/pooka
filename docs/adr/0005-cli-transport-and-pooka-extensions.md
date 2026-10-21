# 0005: The CLI selects SSH or daemon transport; pooka extensions live under /pooka/v1

Status: accepted
Date: 2026-10-21

## Context

ADR 0004 gave pooka a daemon that speaks the Sprites API. The `pooka` CLI still runs
`pct` over SSH. Phase 2b makes the daemon the default transport for the CLI and keeps
SSH for people who do not install `pookad` and for template builds.

The Sprites API does not cover three pooka commands: `start`, `stop` and
`checkpoint rm`. It also creates a sprite from a name only. The pooka CLI has
`--cores`, `--memory` and `--disk` flags and injects the user's SSH public key. Fly may
add fields with the same names and different meanings to its spec at any time.

`pooka push` must leave the file owned by the `pooka` user. The Sprites `fs/write`
endpoint runs as root.

## Decision

Transport selection in the CLI:

- The config file gains `daemon_url`, `token` and `token_file`. If `daemon_url` is
  set, the CLI uses HTTP. Otherwise it uses SSH.
- `SPRITES_API_URL` and `SPRITE_TOKEN` override the config, so one shell serves both
  the `pooka` and the `sprite` CLIs.
- A global `--transport ssh|daemon` flag forces one. `daemon` without a URL is an
  error that names the config key.
- The HTTP transport is a new `Backend` implementation in `internal/backend/httpapi`.
  Commands do not change. The `Backend` interface does not change.

pooka extensions on the daemon:

- Endpoints that the Sprites spec does not define live under the prefix `/pooka/v1`.
  They never share a path with the Sprites spec, so a future Sprites endpoint cannot
  collide with them.
- `POST /pooka/v1/sprites` creates an environment with options: `name`, `cores`,
  `memory_mb`, `disk_gb`, `ssh_pubkey`. A zero value means "use the daemon default".
  A key sent by the client replaces the daemon's configured key for that environment.
  `POST /v1/sprites` stays as the spec defines it, name only.
- `POST /pooka/v1/sprites/{name}/start` and `POST /pooka/v1/sprites/{name}/stop`
  return 204. They are not streamed. Both finish in seconds.
- `DELETE /pooka/v1/sprites/{name}/checkpoints/{id}` returns 204.
- The pooka CLI uses the extension endpoints. The official `sprite` CLI never sees
  them.

File ownership:

- The daemon implements the Sprites `POST /v1/sprites/{name}/fs/chown` endpoint.
  `pooka push` over HTTP calls `fs/write` and then `fs/chown` to `pooka:pooka`.
  `uid` and `gid` accept a number or a name. This stays inside the Sprites spec.

Checkpoint identity over the stream:

- The Sprites checkpoint stream reports success as a text message. The client
  lists checkpoints after the `complete` event and returns the newest one. No
  pooka-specific field is added to the stream.

## Consequences

- One `Backend` interface, two implementations on the client side. Unit tests run the
  real `daemon.Server` on an in-memory backend behind an `httptest.Server` and drive
  it with the real HTTP client. No host is needed.
- The daemon gains four endpoints under `/pooka/v1` and one Sprites endpoint,
  `fs/chown`. docs/sprites-api/README.md documents both groups.
- `pooka push` over HTTP is limited to 16 MB per file by the daemon's write limit.
  Use `--transport ssh` for larger files until the limit is raised.
- Two new client dependencies: `github.com/coder/websocket` becomes direct, and
  `golang.org/x/term` handles raw mode and terminal size for TTY exec.
- A user who sets `daemon_url` and keeps `host` can switch with `--transport ssh` at
  any time. Template builds always use SSH.
