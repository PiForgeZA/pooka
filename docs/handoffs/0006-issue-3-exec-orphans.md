---
date: 2026-10-21
from: Claude Fable 5.1
focus: Issue #3, a process inside the container outlives a client disconnect. Fix it on its own branch while another agent builds phase 3 on a parallel branch.
---

# 0006: Issue #3, exec process outlives the client

## State

- `main` is at `7545e16`, released as v0.2.0. The issue, with the observation, the
  cause, four fix options ranked by preference and the acceptance criteria, is
  https://github.com/PiForgeZA/pooka/issues/3. Read it first. Do not repeat the
  investigation; start at option 1.
- Phase 3 (URLs, TLS, wake-on-request, idle stop) runs in parallel from handoff
  0005 (docs/handoffs/0005-phase-3-urls-wake-idle.md). That work owns new proxy
  code, `internal/daemon/sprites.go`, `internal/daemon/server.go` routing,
  `cmd/pookad/main.go` config and the install script. It has been told to stay out
  of the exec path. The file split below keeps the two branches from colliding.
- The host runs a pookad equivalent to v0.2.0. The three exec fixes from
  commit `c5a5586` are in it: stdin no longer blocks completion, stdin EOF no longer
  stops disconnect detection, request logs carry no query string. Issue #3 is the
  one exec gap left, besides attach-by-session.

## Files this work owns

- `internal/host/local.go` and `local_test.go`: how the daemon runs and cancels a
  command. The likely whole fix for option 1 is here: `exec.Cmd.Cancel` and
  `WaitDelay`.
- `internal/backend/lxc/lxc.go` `Exec` and `attach`: the `lxc-attach` and `runuser`
  argument shapes, only if a wrapper inside the container is needed (option 2 or 3).
- `internal/daemon/exec.go`: the session bookkeeping and the WebSocket reader. Touch
  only if the cancel path needs it. Attach-by-session, if you take it on, lives
  here too.
- Do not change the `Backend` or `host.Host` interfaces. If a change seems needed,
  write an ADR and tell the user before doing it; phase 3 builds on both.

## Facts needed to continue

- Reproduce: `pooka exec <name> -- sleep 30`, kill the client after 2 s, then
  `pooka exec <name> -- pgrep -c sleep` prints 1. Use a scratch environment named
  `fix3-*` and destroy it when done. Never touch containers without the `pooka`
  tag.
- How the daemon cancels today: `exec.CommandContext` sends `SIGKILL` to
  `lxc-attach` on the host. `lxc-attach` forks the command into the container's pid
  namespace with its own session; the host side never learns that pid.
- With a TTY, the attached process has the pty as its controlling terminal
  (`runuser --session-command`, SPEC section 10), so closing the pty master should
  already deliver `SIGHUP`. Check whether the TTY path is in fact fine and only the
  non-TTY path leaks. Test both.
- Over SSH the same `lxc-attach` runs under `sshd`; verify whether `ssh` dying
  leaves the inner process too, and fix it in the same place if the fix is in the
  argument shape rather than in `host.Local`.
- Dev box: LXC 111 on the host, no `/dev/kvm`, Go 1.27.3 at `~/.local/bin/go`.
  `go test -race ./...` needs no host. Host `root@proxmox` is 192.168.68.200, SSH
  alias `proxmox`, pookad on 7700, token at `/etc/pooka/token`.
- Deploy a test daemon: `CGO_ENABLED=0 GOOS=linux go build -o /tmp/pookad
  ./cmd/pookad`, `scp` to `proxmox:/root/pooka-scripts/pookad`, `ssh proxmox 'bash
  /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad'`. The
  permission mode may refuse the install step; ask the user. Tell the user before
  deploying, because the phase 3 agent may deploy its own build to the same host.
  Agree on who owns the host daemon while both branches are open.
- CLI against the daemon without touching `~/.config`: a scratch config with
  `daemon_url` and `token_file`, exported as `POOKA_CONFIG`. Permission hooks block
  commands that read the token in a pipeline with a network tool; fetch it in its
  own step.
- Integration tests are tagged `//go:build integration`, create containers tagged
  `pooka-test` and must destroy them on exit (AGENTS.md). There are none yet; this
  issue is a good first one.

## Next steps in order

1. Branch `fix-exec-orphans` from `main`.
2. Unit test in `internal/host/local_test.go`: run `sh -c 'sleep 30'` through
   `host.Local`, cancel the context, assert the `sleep` is gone within a second.
   It fails today. Make it pass with option 1 from the issue (`Cancel` sending a
   catchable signal, `WaitDelay` as the backstop). Keep the pty path's behaviour.
3. Check that `lxc-attach` forwards that signal into the container. If it does
   not, go to option 3 in the issue and extend the LXC backend. Record the choice in
   an ADR only if it changes the command shape users can see in `pct` or `ps`.
4. Integration test on the host for the container case, TTY and non-TTY, daemon
   and SSH transports.
5. Live check with `pooka exec`, `sprite exec --stdin` and `sprite exec
   --http-post`, as the issue's acceptance list says.
6. Optional, same branch or a second PR: attach-by-session so `sprite exec
   --no-stdin` works. docs/sprites-api/README.md records what is known and what the
   public specs do not say. Do not inspect the official binary.
7. PR titled `fix: end the process inside the container when the exec client
   disconnects`. Merge `main` into the branch before opening it if phase 3 has
   landed. Never rebase. Merge is the user's call.

## Suggested skills

- `diagnose` for step 3 if `lxc-attach` does not behave as expected.
- `tdd` for steps 2 and 4.
- `code-review` before the PR.
