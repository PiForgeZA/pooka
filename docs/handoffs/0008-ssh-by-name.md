---
date: 2026-10-23
from: Claude Fable 5.1
focus: SSH by name is built and verified on branch ssh-proxy. Review and merge the PR. Then the follow-ups from handoff 0007.
---

# 0008: SSH by name

## State

- Phase 3 merged as `6520dfc` (PR #7). Branch `ssh-proxy` holds this follow-up, one
  commit, PR open. `go test -race ./...` green, vet and gofmt clean.
- The host runs the phase 3 pookad (`phase3-dev` build of the merged code). No daemon
  change in this branch. Scratch environments destroyed.

## What landed

- `pooka ssh-proxy <name>[.pooka]`: `Get`, `Start` when stopped, poll port 22 for up
  to 30 s, forward stdin and stdout. Meant as the ssh `ProxyCommand`.
- `pooka ssh-config [--install]`: prints or appends one `Host *.pooka` entry: user
  `pooka`, the ProxyCommand by absolute binary path, `StrictHostKeyChecking
  accept-new`, `UserKnownHostsFile ~/.config/pooka/known_hosts`.
- `pooka ip <name>`: prints the address, fails with a hint when stopped.
- `pooka setup` offers the ssh config entry (`--ssh-config=false` skips). `pooka
  destroy` runs `ssh-keygen -R <name>.pooka` on the pooka known_hosts file.
- SPEC section 10 rows, README, docs/sprites-api mapping, benchmarks.

## Live check, 2026-10-23

`ssh -F <cfg> p3-ssh.pooka hostname`: 0.97 s running; after `pooka stop`, 4.6 s with
`pooka: waking p3-ssh` on stderr. `pooka ip` printed the address. After `destroy` the
known_hosts entry was gone.

## Known limits

- The client dials the environment's address itself. A client that reaches the
  daemon but not the environment subnet needs its own `ProxyJump` through the host.
- The ProxyCommand holds the absolute path of the `pooka` binary at the time
  `ssh-config` ran. Re-run `pooka ssh-config --install` after moving the binary
  (the entry is skipped if present; edit or remove the old block first).

## Next steps

1. Merge the PR. Then the follow-ups from handoff 0007: live cert renewal, `pooka ls`
   with URLs, `sprite url` against the daemon, the activity-policy refactor, the
   phase 3b tunnel ADR.
2. Tag v0.3.0 after merge so `pooka setup` downloads a pookad with the proxy.
