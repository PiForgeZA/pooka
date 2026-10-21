---
date: 2026-10-21
from: Claude Fable 5.1
focus: Phase 3. A URL per environment with TLS, wake-on-request and idle stop. Design first, then build.
---

# 0005: Phase 3, URLs, wake-on-request and idle stop

## State

- `main` is at `7545e16`, tagged and released as v0.2.0:
  https://github.com/PiForgeZA/pooka/releases/tag/v0.2.0. Phases 1, 2 and 2b are
  done. PR #2 and PR #4 record what landed and how it was verified. Working tree
  clean, no open branches of mine.
- The host runs a pookad built from `c5a5586`, which is the v0.2.0 code without the
  GoReleaser change. Equivalent. Install the released `pookad` from the v0.2.0 Linux
  archive with `scripts/install-daemon.sh` if you want the version string to match.
- Open issue, independent of phase 3: https://github.com/PiForgeZA/pooka/issues/3,
  a process inside the container outlives a client disconnect. Another agent may be
  on it in parallel. Keep phase 3 out of `internal/daemon/exec.go`,
  `internal/host/local.go` and the `Backend` and `host.Host` interfaces so the
  branches do not collide. Attach-by-session (`sprite exec --no-stdin`) belongs with
  that issue too, not here.
- Read first: SPEC.md sections 16 and 17, docs/research.md section 1 (URL and sleep
  rows) and section 2 (URL and wake row), ADR 0004 and 0005,
  docs/sprites-api/README.md. Code: internal/daemon/sprites.go (`toSprite`,
  `updateSprite`), internal/daemon/server.go (routes, auth), cmd/pookad/main.go
  (config), scripts/install-daemon.sh.

## What phase 3 is

SPEC section 17: "a reverse proxy with a wildcard domain and TLS. A request to an
environment's URL starts it. Idle environments stop after a timeout." Sprites
semantics to copy (docs/research.md): `https://<name>-<org>.<domain>` routes to port
8080 inside the environment; org-only auth by default, `public` on request; a request
to a stopped environment wakes it; idle environments go to sleep.

What already exists on the API side:

- `toSprite` emits `url` as `http://<ip>:8080` while running and `url_settings.auth`
  as `sprite`. Phase 3 replaces that with the real URL and honours the setting.
- `PUT /v1/sprites/{name}` accepts `UrlSettingsRequest` and ignores it. The OpenAPI
  `UrlSettings` schema is `auth: sprite | public` plus `private_access: admins |
  organization`. Phase 3 must store the setting somewhere; the container description
  JSON that `create` already writes is the only state store the spec allows
  without a new ADR (SPEC section 3 and AGENTS.md ground rules).
- Status is `running` or `cold`. There is no `warm`; LXC stop and start replace it.

## Decisions to make, each needs an ADR

1. Where the proxy lives. Inside `pookad` on 443, or a second process. One binary is
   the project's habit.
2. Domain and certificates. The host is on a LAN. Options: a real domain with a
   wildcard cert through DNS-01 (needs a DNS provider API token on the host), a
   local CA with a wildcard cert the user trusts once, or plain HTTP on a wildcard
   `*.pooka.lan` name for a first cut. The ADR must say what the LAN trust boundary
   from ADR 0004 becomes when 443 is open.
3. Routing: host header `<name>-local.<domain>` to the container IP on 8080. Look the
   IP up the way `List` does, cached briefly; the cluster resources endpoint lags.
4. Wake: proxy sees a request for a stopped environment, calls `Backend.Start`, waits
   for the port, forwards. Start is 2.7 s. Decide the wait limit and the response
   while waiting.
5. Idle: define activity. Inputs available today: exec sessions in daemon memory,
   proxy requests. Decide the default timeout, whether `stop` on idle is `pct
   shutdown` like `pooka stop`, and how a user opts out per environment.
6. URL auth. `sprite` means only the token holder may reach it. A cookie or header
   check in the proxy is enough for one user. `public` skips it.

Use the grilling skill on these before writing code. The user tends to prefer the
simplest thing that keeps Proxmox as the only state.

## Facts needed to continue

- Dev box: LXC 111 on the host, no `/dev/kvm`, Go 1.27.3 at `~/.local/bin/go`.
  `go test -race ./...` needs no host. Build with `go build ./cmd/...`.
- Host `root@proxmox` is 192.168.68.200, PVE 8.4.1, SSH alias `proxmox`. pookad on
  7700, token `/etc/pooka/token`, config `/etc/pooka/pookad.yaml`, systemd unit
  `pookad`. Template 9000 on `local-lvm`. Environments get LAN addresses from
  `vmbr0` by DHCP.
- Deploy: `CGO_ENABLED=0 GOOS=linux go build -o /tmp/pookad ./cmd/pookad`, `scp` to
  `proxmox:/root/pooka-scripts/pookad`, then `ssh proxmox 'bash
  /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad'`. The permission
  mode may refuse the install step as a production deploy; ask the user.
- CLI against the daemon without touching `~/.config`: write a config file in a
  scratch directory with `daemon_url` and `token_file`, export `POOKA_CONFIG`.
- The official `sprite` CLI: install with `sh install.sh --bin-dir <dir>` from
  https://sprites.dev/install.sh under an isolated `HOME`. It needs
  `SPRITES_API_URL`, `SPRITE_TOKEN`, `SPRITE_ORG=local`. `sprite exec` needs
  `--stdin` or `--http-post`. Do not inspect the binary; work from
  docs/sprites-api.
- Scratch environments: name them `p3-*`, destroy them when done. Never touch
  containers without the `pooka` tag.
- Permission hooks block commands that read key or token files in a pipeline with
  a network tool. Fetch the token in its own step.
- Timings (docs/benchmarks.md): create 8 s, start and stop 2.7 s, checkpoint 1.6 s
  over SSH and 3.5 s over the daemon, restore 7 s.

## Next steps in order

1. Grill the six decisions above with the user. Write ADR 0006 (URLs, proxy and
   TLS) and ADR 0007 (idle stop) in docs/adr, update the index.
2. SPEC: a new section for phase 3, update sections 4, 16 and 17.
3. Daemon config and install script: domain, listen address for the proxy, cert
   source, idle timeout.
4. Proxy package with tests against the fake backend from
   internal/backend/backendtest, same pattern as internal/daemon/server_test.go.
5. `updateSprite` stores and returns URL settings; `toSprite` emits the real URL.
6. Idle stop as a loop in the daemon, tested with a fake clock.
7. Live check on the host, timings into docs/benchmarks.md, README section.
8. PR with a conventional title. Merge is the user's call.

## Suggested skills

- `grill-with-docs` or `grill-me` for the design decisions, then write the ADRs.
- `tdd` for the proxy and the idle loop.
- `code-review` before the PR.
