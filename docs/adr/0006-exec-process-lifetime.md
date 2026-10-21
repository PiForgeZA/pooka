# 0006: Exec owns its process session

Status: accepted
Date: 2026-10-21

## Context

Killing `lxc-attach` leaves the command in the container running.
Direct SIGTERM and SIGHUP do not reach that command on LXC 6.0.0.
The same failure occurs with a terminal.
A group signal reaches non-terminal commands if runuser keeps the group, but it
does not solve terminal cleanup or SSH disconnects.
An SSH multiplex master also survives the loss of one client channel.

## Decision

Run each exec under a short shell supervisor in the container.
Use `setpriv --pdeathsig HUP` to notify it when `lxc-attach` exits.
The supervisor kills its session on HUP or TERM.
Keep `runuser` in that session with `--session-command`.
Use `setsid` when there is no terminal.
Keep the LXC terminal session when there is a terminal.

Use `setpriv --pdeathsig KILL` on the host to end `lxc-attach` when its
SSH server process exits. Give streaming exec a dedicated SSH connection.
Add `DedicatedSession` to `host.Cmd` for this purpose.
Keep the `Host` and `Backend` methods unchanged.

## Consequences

This uses option 2 from issue #3. The tests verified the required tools on the
current host and template. A separate PID record would require another host call
on cancellation and protection against stale PID records.

The supervisor exists only during exec. It needs no installed agent or state file.
The host needs `setpriv`. The template needs `setpriv`, `setsid`, `pkill`, and GNU
`env`. The current Debian host and Ubuntu template provide these commands.
Exec over SSH pays for a separate connection. Other host calls keep multiplexing.
Tests must cover stdin, exit codes, terminal job control, and disconnects.
The host integration tests passed on 2026-10-21. Cleanup took 17 to 36 ms
after the Linux client received SIGKILL.

See [exec test instructions](../exec-testing.md) for the test matrix and platform limits.
