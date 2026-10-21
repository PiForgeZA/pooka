---
date: 2026-10-21
from: Claude Fable 5.1
focus: Phase 2b is deployed and verified live. Commit, push, open the PR.
---

# 0004: Phase 2b, deploy and live check

## State

- Phase 2b code and docs are complete and uncommitted on branch `phase-2`. `go test
  -race ./...` passes, 49 tests, no host needed. `gofmt` and `go vet` are clean.
- Done in this session, per handoff 0003:
  - ADR 0005: transport selection, pooka extensions under `/pooka/v1`, `fs/chown`
    for push ownership, checkpoint id from the list after the stream.
  - Daemon: `POST /pooka/v1/sprites` (create with options), `.../start`, `.../stop`,
    `DELETE .../checkpoints/{id}`, and Sprites `POST .../fs/chown`. Tests in
    internal/daemon/server_test.go.
  - Shared in-memory backend for tests: internal/backend/backendtest.
  - Client: internal/backend/httpapi, a `Backend` over the Sprites API plus the
    extensions. WebSocket exec with raw-mode TTY and SIGWINCH resize. Tests run the
    real daemon on the fake backend behind `httptest.Server`.
  - Config: `daemon_url`, `token`, `token_file`; `SPRITES_API_URL` and
    `SPRITE_TOKEN` override them. CLI: `--transport ssh|daemon`, backend opened in
    the root PersistentPreRun. Over the daemon, `create` sends only flags the user
    set.
  - Docs: SPEC sections 8, 10, 12, 16, 17; README quick start with the daemon first;
    docs/sprites-api/README.md gains "pooka extensions" and the CLI mapping.
  - go.mod: `golang.org/x/term` added, `go 1.24.0`.
- Live check, 2026-10-21, from the dev box:
  - SSH transport: create (8.3 s), exec with stdin, `--root`, push, pull,
    checkpoint, checkpoint ls, checkpoint rm, stop, start, restore, destroy. All pass.
  - Daemon transport against the daemon still running on the host (an older build):
    ls, exec with stdin, exit code, `--root` via sudo, TTY exec, checkpoint,
    checkpoint ls, pull, restore, destroy. All pass. create, start, stop, checkpoint
    rm and push fail as expected because that build has no `/pooka/v1` routes and no
    `fs/chown`. The client now says "this pookad has no such endpoint; upgrade it".
- Deployed and verified, 2026-10-21, same session, after the user asked for it:
  - pookad redeployed twice with `scripts/install-daemon.sh`; `/healthz` reports
    the phase 2b build.
  - Daemon transport, new endpoints: `create --cores 2` gives `cores: 2` in `pct
    config`; push leaves the file owned by `pooka pooka`; stop, start, checkpoint
    rm, restore, destroy all pass. Errors read well: not found, already exists,
    missing snapshot shows the `pct` stderr.
  - Official `sprite` CLI 2026-10-20-hotfix.1 against the new daemon: ls, create,
    exec `--stdin`, exec `--http-post`, checkpoint create and list, destroy. All pass.
  - Bug found and fixed during the check: `pvesh get .../snapshot` returns
    snapshots in hash order, so "newest by position" returned an older checkpoint
    id. The LXC backend now sorts by time; the client picks the newest by time.
    Test added. The daemon on the host has the fix.
  - Timings in docs/benchmarks.md. Checkpoint over the daemon is 3.5 s, 0.5 s over
    target; the cause and two fixes are noted there.

## Facts needed to continue

- Dev box: LXC 111 on the host, no `/dev/kvm`. Go 1.27.3 at `~/.local/bin/go`.
- Host `root@proxmox` is 192.168.68.200, PVE 8.4.1, template 9000 on `local-lvm`.
  pookad runs as a systemd unit, port 7700, token `/etc/pooka/token`, config
  `/etc/pooka/pookad.yaml`.
- Build and deploy the daemon:
  ```
  V=$(git describe --tags --always --dirty)
  CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.Version=$V" -o /tmp/pookad ./cmd/pookad
  scp /tmp/pookad proxmox:/root/pooka-scripts/pookad
  ssh proxmox 'bash /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad'
  curl -s http://192.168.68.200:7700/healthz
  ```
- Point the CLI at the daemon without touching `~/.config`: write a config in a
  scratch directory and export `POOKA_CONFIG=<that file>`:
  ```
  daemon_url: http://192.168.68.200:7700
  token_file: <scratch>/token      # ssh proxmox cat /etc/pooka/token > <scratch>/token
  ```
- A TTY check without a terminal: `script -qec "./pooka exec -t <name> -- tty" /dev/null`.
  `script` prints a literal `^@` first; that is not pooka output.

## Next steps in order

1. `pooka console` by hand from a real terminal over the daemon: check Ctrl-C,
   resize and exit code. Only the non-interactive TTY path was checked by script.
2. Commit on `phase-2` as one or more conventional commits, for example
   `feat: pooka CLI over the daemon with SSH fallback (phase 2b)`. Push, open the PR
   to `main` with a conventional title, confirm CI green. Merge is the user's call.
3. Optional follow-ups, each its own PR: checkpoint id on the stream to get under
   3 s; raise the 16 MB push limit by streaming `fs/write`; attachable exec sessions
   so `sprite exec --no-stdin` works.

## Suggested skills

- `code-review` on the diff before committing.
- `tdd` for the follow-ups.
