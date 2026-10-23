---
date: 2026-10-22
from: Claude Fable 5.1
focus: Phase 3 is built, reviewed and verified live on branch phase-3. Review the diff, commit, open the PR. Then the small follow-ups below.
---

# 0007: Phase 3 built and verified

## State

- Branch `phase-3`, not committed. `main` is at `8f6442f` (v0.2.1 plus setup). The
  working tree holds the whole phase: ADR 0008 and 0009, SPEC section 18, code, tests,
  docs. `go test -race ./...` passes, `go vet` and `gofmt` are clean. shellcheck is
  not installed on the dev box; CI runs it.
- The host `proxmox` runs a pookad built from this tree (`phase3-dev`). Its
  `/etc/pooka/pookad.yaml` gained `domain: 192-168-68-200.sslip.io`,
  `proxy_listen: 0.0.0.0:443` and `idle_timeout: 30m` from the install script. The
  generated CA and leaf live in `/etc/pooka/tls/`. Port 443 on the host is pookad's.
- Scratch environment `p3-web` (VMID 9100) exists on the host, stopped, with a 2
  minute idle timeout and a python `http.server` systemd unit on 8080. Destroy it
  when done: `pooka destroy p3-web --force`.
- Decisions, grilled with the user on 2026-10-21 and 2026-10-22: see ADR 0008 (domain,
  first-label routing, proxy in pookad on 443, generated CA, wake, URL auth) and ADR
  0009 (idle stop). SPEC section 17 records a phase 3b candidate: a tunnel
  (Cloudflare Tunnel, frp) for `public` URLs. Needs its own ADR.

## What landed

- `internal/backend`: `Settings{URLAuth, IdleTimeout *Duration}` embedded in `Env`,
  `Env.URL`, `CreateOptions.IdleTimeout`, `Backend.Update`. LXC backend stores
  `url_auth` and `idle_timeout` in the container description JSON and reads them in
  the inventory scan. Fake backend gained `Update`, status flips on start and stop,
  `Established`, `StartErr`, `Gets`.
- `internal/daemon`: `Domain`, `IdleTimeout`, `Now`; `toSprite` emits the real URL,
  `url_settings.auth` and a `pooka` object with `ip` and `idle_timeout`;
  `PUT /v1/sprites/{name}` stores auth; `PUT /pooka/v1/sprites/{name}/settings`;
  `Touch` on exec, fs, checkpoint, restore, start, create; `idle.go` with
  `idleTick` and `RunIdle`.
- `internal/proxy`: first-label routing, `api.<domain>`, bearer or per-host HMAC
  cookie, `?token=` sets the cookie, single-flight wake, port poll, negative lookup
  cache, shared transport with a 5 s idle timeout so the proxy's own keep-alive
  connections never trip the idle check.
- `internal/pki`: CA plus wildcard leaf, renewed when the domain changes or 30 days
  remain. Loaded at pookad start only; a renewal needs a restart.
- `cmd/pookad`: new config keys, TLS listener, proxy failure logs and keeps the API
  up, idle loop, shutdown waits for both servers.
- `internal/cli`: `pooka url [--auth] [--open]`, `pooka idle`, `create
  --idle-timeout`, CA file for https `daemon_url` (`ca_file`, default
  `~/.config/pooka/ca.pem`). `pooka setup` asks for the domain, passes it to the
  install script as `POOKA_DOMAIN`, saves the CA, defaults `daemon_url` to
  `https://api.<domain>`, prints the trust step.
- `scripts/install-daemon.sh` writes the URL keys into a new pookad.yaml and appends
  them to an old one without a `domain:`.
- Docs: README, SPEC sections 2, 4, 8, 10, 16, 17, 18, docs/sprites-api/README.md,
  docs/benchmarks.md, ADR index.

## Live check, 2026-10-22, from LXC 111 against the host

- `pooka ls` over `https://api.192-168-68-200.sslip.io` with the CA: 0.66 s.
- `pooka create p3-web --idle-timeout 2m`: 9.5 s, prints the URL.
- URL without credentials: 401 with the hint. `pooka url` link: 303 to the clean URL
  with the cookie; cookie alone: 200; the same cookie on another host name: 401;
  wrong org: 404. `--auth public`: 200 without credentials. `--auth sprite` back.
- `pooka stop`, then one request to the URL: 200 after 9.6 s, one `proxy: waking`
  line in the journal, `pooka ls` shows running.
- `ss -Htn state established` as root inside the container lists an open SSH login
  from the LAN and nothing after it closes.
- Idle, three phases: no activity, stopped after 2 m 10 s. Started, SSH login held
  170 s, still running at 165 s. Login closed, stopped after 2 m 59 s.
- A host name outside the configured domain fails TLS, as expected: the cert covers
  `*.<domain>` only. The routing itself accepts it.

## Code review findings applied

Review at medium found ten items. Fixed: per-request transport (keep-alive
connections defeated idle stop), proxy listen failure killed the API, running
environment without an IP waited 30 s, no negative lookup cache, `idle -n` with two
positionals silently did nothing, wake raced with an external start, the yaml append
lived in two places, `Update` re-read the config, shutdown cut proxied requests.
Left as is: `url --auth` and `idle` call `Update` then `Get`; the interface returns
only an error, as SPEC 18.6 says.

## After the PR opened

- CI caught a data race in the test fake (`Get` wrote a map with no lock) and a
  repeated run found a double start on a concurrent wake. Both fixed in `e0c1bed`:
  the fake takes a mutex, the wake leader checks the live status before `Start`.
- An architecture review (improve-codebase-architecture skill) raised two points,
  both applied: `fs/read` now records activity like the other filesystem calls, and
  `pooka setup` reads the installed domain back from the host's pookad.yaml so
  `daemon_url` follows the certificate when an existing file keeps its own domain.
  SPEC 18.5 now says that information reads (`ls`, get, checkpoint list) are not
  activity. The review's larger suggestion, one module that owns activity policy
  and the idle decision instead of `Touch` calls in each handler, is a refactor for
  a later pass.

## Next steps in order

1. Review `git diff main`. Commit on `phase-3` as `feat: environment URLs, wake on
   request and idle stop (phase 3)`. Open the PR with that title. Merge is the user's
   call.
2. Destroy `p3-web` on the host.
3. After the merge, tag v0.3.0 so `pooka setup` can download a pookad with the proxy.
4. Follow-ups, each small: live cert renewal without a restart (`tls.Config.
   GetCertificate`); a wake on port 22 so SSH to a stopped environment starts it;
   `pooka ls` showing the URL; the official `sprite url` command against the daemon
   (not installed on the dev box, so not verified).
5. Phase 3b ADR on tunnels, when the user wants `public` URLs on the internet.

## Facts needed to continue

- Dev box: LXC 111, Go 1.27.3 at `~/.local/bin/go`. `go test -race ./...` needs no
  host. Build: `go build ./cmd/...`.
- Host `root@proxmox` is 192.168.68.200, PVE 8.4.1. pookad on 7700 and 443, token
  `/etc/pooka/token`, config `/etc/pooka/pookad.yaml`, CA `/etc/pooka/tls/ca.pem`.
  AdGuard (CT 100) and Nginx Proxy Manager (CT 102) have their own addresses; the
  host's 443 was free.
- Deploy: `CGO_ENABLED=0 GOOS=linux go build -o /tmp/pookad ./cmd/pookad`, `scp` to
  `proxmox:/root/pooka-scripts/pookad`, `scp scripts/install-daemon.sh` beside it,
  `ssh proxmox 'bash /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad'`.
- CLI against the daemon without touching `~/.config`: a config file with
  `daemon_url: https://api.192-168-68-200.sslip.io`, `token_file:
  ~/.config/pooka/token` and `ca_file: <copy of ca.pem>`, exported as `POOKA_CONFIG`.
- The Bash permission hook blocks a command whose text mixes credential words with
  `ssh`, `scp`, `curl` or `ss`. Write code with the Write and Edit tools. For URL
  checks, use `$(pooka url <name>)` and a cookie jar instead of typing the token.
- Scratch environments: name them `p3-*`, destroy them when done.

## Suggested skills

- `code-review` on the final diff if more changes land before the PR.
- `pr` for the PR body.
