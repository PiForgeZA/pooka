# 0009: Idle environments stop after a timeout, unless a TCP connection is open inside

Status: accepted
Date: 2026-10-21

## Context

Sprites puts an idle sprite to sleep and wakes it on the next request. pooka has no
sleep tier: LXC stop and start take about 2.7 seconds each (ADR 0004, benchmarks).
Stop and start are the sleep.

The daemon sees proxy requests, exec sessions, filesystem calls and checkpoint
calls. It does not see SSH logins to the environment, herdr sessions, or any client
that talks to a service in the environment without the proxy. An idle stop that
ignores those would stop an environment under the user's shell.

Per-environment settings must live in the container description JSON (SPEC
section 3). ADR 0008 adds `Backend.Update` for that purpose.

## Decision

- Activity is any proxy request, exec session open, filesystem call or checkpoint
  call for an environment. The daemon records the time of the last activity per
  environment in memory. A daemon start and a `start` of the environment count as
  activity.
- A loop in the daemon runs every minute. For each running environment whose last
  activity is older than its idle timeout, the loop runs
  `ss -Htn state established` inside the container. One or more established TCP
  connections cancel the stop and reset the activity time. Otherwise the loop
  calls `Backend.Stop`, the same `pct shutdown` with a 10 second force that
  `pooka stop` uses. The loop logs every stop with the idle duration.
- `idle_timeout` in `pookad.yaml` is the default. The default is `30m`. `0`
  disables idle stop for every environment.
- A per-environment `idle_timeout` lives in the description JSON. `0` opts the
  environment out. `pooka create --idle-timeout <d>` sets it at creation.
  `pooka url` does not touch it. `PUT /pooka/v1/sprites/{name}/settings` and
  `pooka idle <name> <d|off>` change it later. The Sprites spec has no field for
  this, so it stays under `/pooka/v1`.
- No checkpoint on idle stop. Sprites checkpoints on idle; pooka does not, because
  snapshots accumulate and the user has `pooka checkpoint`.

## Consequences

- An environment with an open SSH session, an open herdr session or an open
  WebSocket never stops on idle. A client that keeps a connection open keeps the
  environment up. That is the intended meaning of "in use".
- The in-container check costs one `lxc-attach` per candidate environment per
  minute, only for environments already past their timeout. It needs `ss` in the
  template. Ubuntu 24.04 ships it in `iproute2`.
- A daemon restart resets every timer. Environments stay up for at least one
  timeout after a restart.
- The first request to a stopped environment pays the start time, about 3 seconds
  plus the service's own start. ADR 0008 holds the request for that time.
- Description JSON gains `idle_timeout`. Old environments without the field take
  the daemon default.
