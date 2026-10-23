# Sprites API reference copies and compatibility notes

`openapi.json` (Sprites API 0.1.12) and `asyncapi.json` (WebSocket v0.0.1-rc48) are
verbatim copies from docs.fly.io, fetched 2026-10-20. They are the contract pooka's
daemon follows. Refresh them when Fly publishes a new version and diff.

## Compatibility probe, 2026-10-20

Question: can the official `sprite` CLI (version 2026-10-20-hotfix.1) target a server
that is not Fly's?

Method: a stub HTTP server on 127.0.0.1 that logged every request and returned minimal
JSON. The CLI ran with an isolated `HOME`.

Findings:

- `sprite login <url>` and `sprite org auth <url>` both demand a Fly.io OAuth token
  first. They cannot bootstrap against a third-party server.
- The binary honours three environment variables that bypass that flow entirely:
  `SPRITES_API_URL`, `SPRITE_TOKEN` (alias `SPRITE_API_TOKEN`) and `SPRITE_ORG`.
  With those set, every command went to the stub with `Authorization: Bearer <token>`.
- Requests observed:

| CLI command | Request |
|---|---|
| `sprite ls` | `GET /v1/sprites?max_results=100` |
| `sprite create demo` | `POST /v1/sprites` body `{"name":"demo"}` |
| `sprite info` | `GET /v1/sprites/demo` |
| `sprite destroy` | `DELETE /v1/sprites/demo` |
| `sprite checkpoint create --comment x` | `POST /v1/sprites/demo/checkpoint` body `{"comment":"x"}`, expects ndjson stream |
| `sprite checkpoint list` | `GET /v1/sprites/demo/checkpoints`, expects a JSON array |
| `sprite restore v3` | `POST /v1/sprites/demo/checkpoints/v3/restore`, expects ndjson stream |
| `sprite exec -- ls -la` | WebSocket `GET /v1/sprites/demo/exec?cmd=ls&cmd=-la&path=ls&stdin=false` |
| `sprite exec --tty -- bash` | WebSocket, plus `tty=true`, `stdin=true`, `env=TERM=...` repeated |
| `sprite console` | first `PUT /v1/sprites/demo/fs/write?path=/usr/share/terminfo/x/xterm-256color&mkdirParents=true&mode=0644&workingDir=/`, then WebSocket exec of `/.sprite/bin/sprite-console` with `tty=true` |
| `sprite exec --http-post -- echo hi` | `POST /v1/sprites/demo/exec?cmd=echo&cmd=hi&path=echo&stdin=true` |

- The user agent on REST calls is `(interactive=false; parent=other; agent=claude-code)`
  style metadata; on WebSocket calls it is Go's default.
- The CLI writes a terminfo entry into the sprite before a console. Note the query key
  is `mkdirParents`, not `mkdir` as the OpenAPI says. Accept both.
- `sprite console` runs `/.sprite/bin/sprite-console`, a Fly-specific binary. A
  compatible server maps that path to a login shell.

## WebSocket exec protocol (from asyncapi.json)

Binary frames carry a one-byte prefix: `0x00` stdin (client to server), `0x01` stdout,
`0x02` stderr, `0x03` exit code (one byte), `0x04` stdin EOF (client to server).
Text frames carry JSON control messages:

- client to server: `{"type":"resize","cols":180,"rows":50}`
- server to client: `{"type":"session_info","session_id":"1","command":"bash","created":1767609000,"cols":120,"rows":40,"is_owner":true,"tty":true}`
- server to client: `{"type":"exit","exit_code":0}`
- server to client: `{"type":"port_opened","port":3000,"address":"...","pid":1847}` and `port_closed`

Query parameters on the exec path: `cmd` (repeated), `path`, `stdin`, `tty`, `env`
(repeated `KEY=VALUE`), `dir`, `max_run_after_disconnect`.

## Endpoint map for pooka

| Sprites endpoint | pooka phase 2 | Backing |
|---|---|---|
| `GET /v1/sprites` | yes | `Backend.List` |
| `POST /v1/sprites` | yes | `Backend.Create` |
| `GET /v1/sprites/{name}` | yes | `Backend.Get` |
| `DELETE /v1/sprites/{name}` | yes | `Backend.Destroy` |
| `PUT /v1/sprites/{name}` | yes | `url_settings.auth` to `Backend.Update` (phase 3, ADR 0008) |
| `POST .../checkpoint` | yes, ndjson | `Backend.Checkpoint` |
| `GET .../checkpoints` | yes | `Backend.ListCheckpoints` |
| `GET .../checkpoints/{id}` | yes | from the list |
| `POST .../checkpoints/{id}/restore` | yes, ndjson | `Backend.Restore` |
| `GET .../exec` (WebSocket) | yes, TTY and non-TTY | `Backend.Exec` with a host PTY |
| `POST .../exec` | yes | `Backend.Exec`, stdout and stderr concatenated |
| `GET .../exec` (JSON list), `.../exec/{id}/kill` | yes, in-memory sessions | daemon |
| `GET .../fs/read`, `PUT .../fs/write`, `GET .../fs/list` | yes | `Backend.Exec` as root |
| `POST .../fs/chown` | yes; `uid` and `gid` take a number or a name | `Backend.Exec` as root |
| `fs/chmod`, `copy`, `delete`, `rename` | later | |
| `policy/*`, `services/*`, `oauth/*` | 501 | phases 3 and 4 |
| WebSocket `proxy`, `control`, `ports/watch`, `fs/watch`, `exec/{id}` attach | later | |

Verified live on 2026-10-21 with the official CLI against pookad: `ls`, `create`,
`info`, `exec --stdin`, `exec --tty`, `exec --http-post`, `checkpoint create`,
`checkpoint list`, `restore`, `destroy`.

Known limitation: `sprite exec --no-stdin` is the CLI's reconnectable mode. It
attaches to a session by id before sending a command. pooka keeps no attachable
sessions yet and answers 404 with a hint to use `--stdin` or `--http-post`. How the CLI
expects that session to come into being is not described by the public specs or the
SDK, so pooka does not guess.

## pooka extensions

The `pooka` CLI needs four operations the Sprites spec does not define. They live
under `/pooka/v1` so they can never collide with a future Sprites path (ADR 0005).
They use the same bearer token. The official `sprite` CLI never calls them.

| Endpoint | Body | Response |
|---|---|---|
| `POST /pooka/v1/sprites` | `{"name","cores","memory_mb","disk_gb","ssh_pubkey","idle_timeout"}`; a zero or empty value means the daemon default; `idle_timeout` is a Go duration such as `2h` or `0s` | 201, SpriteResponse |
| `POST /pooka/v1/sprites/{name}/start` | none | 204 |
| `POST /pooka/v1/sprites/{name}/stop` | none | 204 |
| `PUT /pooka/v1/sprites/{name}/settings` | `{"url_auth","idle_timeout"}`; an empty field keeps its value (ADR 0008, 0009) | 200, SpriteResponse |
| `DELETE /pooka/v1/sprites/{name}/checkpoints/{id}` | none | 204 |
| `GET` and `POST /pooka/v1/sprites/{name}/exec` | same protocol and query as the Sprites exec, plus `root=true` to run as root | as the Sprites exec |

Errors use the same `{"error": "..."}` shape and codes as the Sprites endpoints:
400, 401, 404, 409, 500.

How the `pooka` CLI maps onto the API (internal/backend/httpapi):

| `pooka` command | Request |
|---|---|
| `ls`, `use` | `GET /v1/sprites`, `GET /v1/sprites/{name}` |
| `create` | `POST /pooka/v1/sprites` |
| `start`, `stop` | pooka extensions |
| `destroy` | `DELETE /v1/sprites/{name}` |
| `exec`, `console` | WebSocket exec; `--root` uses the `/pooka/v1` exec route with `root=true` |
| `push` | `PUT .../fs/write`, then `POST .../fs/chown` to `pooka:pooka` |
| `pull` | `GET .../fs/read` |
| `checkpoint` | `POST .../checkpoint` stream, then `GET .../checkpoints` for the id |
| `checkpoint ls` | `GET .../checkpoints`, reversed to oldest first |
| `checkpoint rm` | pooka extension |
| `restore` | `POST .../checkpoints/{id}/restore` stream |

| `url --auth` | `PUT /v1/sprites/{name}` with `url_settings` |
| `idle` | `PUT /pooka/v1/sprites/{name}/settings` |
| `ip`, `ssh-proxy` | `GET /v1/sprites/{name}`; `ssh-proxy` adds `POST /pooka/v1/sprites/{name}/start` when stopped, then connects to port 22 itself |

Status mapping: a running container reports `running`; a stopped one reports `cold`.
pooka has no `warm` state. With a `domain` in pookad.yaml the `url` field is
`https://<name>-local.<domain>` whether the environment runs or not, because a
request wakes it (phase 3, ADR 0008). Without a domain it is `http://<ip>:8080`
while running and empty otherwise. `url_settings.auth` reports the stored value.

Every sprite response carries one extra object, `pooka`, with `ip` (empty when
stopped) and `idle_timeout` (a Go duration, or null for the daemon default). It
sits under one key so a future Sprites field cannot collide with it.

The proxy on the daemon's `proxy_listen` serves the API for the host name
`api.<domain>` over TLS, with the generated CA from `/etc/pooka/tls/ca.pem`. The
official CLI reaches it with `SPRITES_API_URL=https://api.<domain>` once that CA is
in the system trust store, or on plain HTTP at port 7700 as before.
