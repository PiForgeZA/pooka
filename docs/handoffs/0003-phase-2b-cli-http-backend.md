---
date: 2026-10-21
from: Claude Fable 5.1
focus: Phase 2b. Make the daemon the default transport for the pooka CLI, keep SSH as a fallback for people who do not install pookad.
---

# 0003: Phase 2b, the pooka CLI talks to pookad

## State

- Branch `phase-2`, two commits ahead of `origin/phase-2`, not pushed, no PR yet.
  `git log --oneline main..phase-2` shows them. CI runs on push (branch pattern
  `phase-*`). `main` is at v0.1.0.
- `pookad` is installed and running on the host as a systemd unit, port 7700, token in
  `/etc/pooka/token`, config in `/etc/pooka/pookad.yaml`. Health: `curl
  http://192.168.68.200:7700/healthz`.
- The official `sprite` CLI completes the full lifecycle against pookad. ADR 0004 and
  docs/sprites-api/README.md record what is implemented, the request shapes observed,
  and the one known limitation (attach by session id, used by `sprite exec
  --no-stdin`).
- The `pooka` CLI still uses SSH only (SPEC section 12). The LXC backend and the
  daemon share the same `Backend` interface (SPEC section 13).
- 38 unit tests. `go test ./...` needs no host.

Read first: SPEC.md sections 8, 10, 12, 13, 16, 17. ADR 0004. docs/sprites-api/README.md.
Code: internal/backend/backend.go, internal/daemon/*.go, internal/cli/cli.go.

## Goal of phase 2b

`pooka` uses pookad over HTTP by default. SSH stays as a fallback and for template
builds. A user who does not want a daemon on the host keeps working with SSH only.

## Design, decided

- Config (SPEC section 8) gains `daemon_url` and `token`, plus `token_file` for people
  who do not want the token in the config. Selection rule: if `daemon_url` is set,
  use HTTP; otherwise SSH. Add `--transport ssh|daemon` to force one. Honour
  `SPRITES_API_URL` and `SPRITE_TOKEN` as overrides so the same shell works for both
  CLIs.
- New package `internal/backend/httpapi`: a `Backend` implementation that is a client
  of the Sprites API. One backend, one interface, the commands do not change.
- Mapping: List and Get are `GET /v1/sprites` and `GET /v1/sprites/{name}`; Create is
  `POST /v1/sprites`; Destroy is `DELETE`. Checkpoint create and Restore read the
  ndjson stream and print `info` lines. ListCheckpoints and DeleteCheckpoint are the
  checkpoints endpoints (delete is not in the Sprites spec; see below). Exec is the
  WebSocket protocol in docs/sprites-api/README.md with `github.com/coder/websocket`
  on the client side too. Push and Pull are `fs/write` and `fs/read`.
- TTY on the client: put the local terminal in raw mode with `golang.org/x/term`,
  send `resize` on SIGWINCH, restore on exit. `pooka console` is exec of `bash -l`
  with TTY, as today.
- The Sprites API has no start, stop or delete-checkpoint. Add pooka extensions under
  a separate prefix so they never collide with Fly's spec: `POST
  /pooka/v1/sprites/{name}/start`, `POST /pooka/v1/sprites/{name}/stop`, `DELETE
  /pooka/v1/sprites/{name}/checkpoints/{id}`. Document them in
  docs/sprites-api/README.md under "pooka extensions". Record the decision in ADR 0005.
- Errors: map 404 to `backend.ErrNotFound`, 409 to `backend.ErrExists`, 401 to a
  message that names the token setting. Show the server's `error` field verbatim.
- `pooka create` through the daemon uses the daemon's defaults for cores, memory,
  disk and SSH key. The CLI flags `--cores` and friends do not travel: the Sprites
  create request has only `name`. Either accept that for now and say so in help, or
  add the fields to the pooka extension create. Decide and write it down.

## Next steps in order

1. Push `phase-2`, open the PR to `main` with a conventional title, confirm CI green.
   Merge is the user's call. Phase 2b can continue on the same branch or a new
   `phase-2b` from `phase-2`.
2. ADR 0005: transport selection and the pooka extension endpoints.
3. Daemon: add the three extension endpoints with tests in
   internal/daemon/server_test.go. Redeploy with
   `scripts/install-daemon.sh` after `CGO_ENABLED=0 GOOS=linux go build ./cmd/pookad`
   and `scp` to `proxmox:/root/pooka-scripts/pookad`.
4. Client: internal/backend/httpapi with unit tests against an `httptest.Server`
   running the real `daemon.Server` on the fake backend from server_test.go. That
   tests both ends with no host.
5. CLI wiring in internal/cli/cli.go `NewApp`: choose the backend from config and
   flags. Keep `host.SSH` for `--transport ssh`.
6. Live check from this box: `pooka ls`, `create`, `console`, `checkpoint`, `restore`,
   `destroy` through the daemon, then the same with `--transport ssh`.
7. README quick start: daemon path first, SSH path as the alternative. Update SPEC
   sections 8 and 12.

## Facts needed to continue

- Dev box: LXC 111 on the host, no `/dev/kvm`, cannot host environments. Go 1.27.3 at
  `~/.local/bin/go`. Build with `go build -o pooka ./cmd/pooka`; the binary works from
  here against the host over SSH today.
- Host `root@proxmox` is 192.168.68.200, PVE 8.4.1. SSH alias `proxmox` works from
  the dev box. Template 9000 on `local-lvm`, 32 GB thin.
- To test with the official CLI, install it into a scratch directory with
  `sh install.sh --bin-dir <dir>` from https://sprites.dev/install.sh and run it with
  an isolated `HOME`, `SPRITES_API_URL=http://192.168.68.200:7700`,
  `SPRITE_TOKEN=$(ssh proxmox cat /etc/pooka/token)`, `SPRITE_ORG=local`. Names must
  be at least 3 characters. Pass `--stdin` or `--http-post` to `exec`.
- Do not examine the official CLI binary itself. Work from the public specs in
  docs/sprites-api, the MIT `sprites-go` SDK source, and requests observed by pookad.
- Timings: create 7.9 s through the daemon, 8.7 s over SSH. Remaining cost is pct
  startup per call and the systemd ready wait.

## Suggested skills

- `tdd` for the httpapi client and the extension endpoints.
- `code-review` on the phase-2 diff before opening the PR.
- `grill-me` on the open question of create options through the daemon.
