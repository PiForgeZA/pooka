# pooka specification, phase 1

This document uses simplified technical English. Sentences are short. Each sentence gives
one fact or one instruction.

## 1. Purpose

pooka creates isolated Linux environments on a Proxmox VE host. A user creates an
environment with one command. The environment is ready in seconds. The environment keeps
its files until the user destroys it.

Phase 1 serves one user on one host. Later phases serve more users and more hosts.

## 2. Terms

- **host**: the Proxmox VE node that runs environments.
- **environment**: one LXC container that pooka manages. The Sprites product calls this a
  sprite. pooka uses "environment" in docs and the environment name in commands.
- **template**: an LXC container converted to a Proxmox template. Every environment is a
  linked clone of a template.
- **checkpoint**: a Proxmox snapshot of an environment's filesystem.
- **backend**: the code that talks to one hypervisor type. Phase 1 has one backend: LXC.

## 3. Goals

- Create a ready environment in less than 5 seconds after the command starts.
- Start a stopped environment in less than 2 seconds.
- Make a checkpoint in less than 3 seconds.
- Run with no daemon, no database and no agent inside the environment.
- Keep all state in Proxmox. A user can inspect or repair everything with `pct`.

## 4. Non-goals for phase 1

- Hardware isolation. LXC shares the host kernel.
- HTTPS URLs for environments.
- Automatic sleep on idle.
- Multiple hosts or multiple users.
- The Sprites HTTP API.

## 5. Host requirements

- Proxmox VE 8.x or 9.x.
- One storage of type `lvmthin` or `zfspool` with content type `rootdir`. Linked clones
  and snapshots need this. Directory storage is not supported.
- SSH access for the user as `root`, or as a user with `pct` rights through sudo.
- One template container. Section 7 describes it.

## 6. Client requirements

- Linux or macOS.
- An SSH key that the host accepts.
- The `pooka` binary.

## 7. The template

The template gives environments their speed. Build it once. Rebuild it when tools change.

1. Create an unprivileged Ubuntu LTS container on the thin storage.
2. Set `features: nesting=1,keyctl=1` so systemd and Docker work.
3. Install base packages with apt. Install mise. Install language runtimes, coding
   agents and herdr with mise from `/etc/mise/config.toml`. The repo keeps this file at
   `scripts/template/mise.toml`. Edit it to change the tool set.
4. Create a user named `pooka` with passwordless sudo. mise installs tools for this user.
   Commands run as `pooka`, not root.
5. Place `/etc/pooka/firstboot.sh`. The create command runs this script once. See section 9.
6. Clean caches and logs. Stop the container.
7. Convert it with `pct template <id>`.
8. Record the template ID in the pooka config file.

pooka ships a script, `scripts/build-template.sh`, that does steps 1 to 7. The script
is embedded in the `pooka` binary; `pooka setup` copies it to the host and runs it.

## 8. Configuration

pooka reads `~/.config/pooka/config.yaml`. Example:

```yaml
host: root@proxmox
template: 9000
storage: local-lvm
bridge: vmbr0
vmid_range: [9100, 9199]
disk_gb: 32
cores: 4
memory_mb: 8192
ssh_pubkey: ~/.ssh/id_ed25519.pub
daemon_url: http://proxmox:7700   # optional; selects the daemon transport
token_file: ~/.config/pooka/token # or token: <value>
```

Transport selection (ADR 0005): if `daemon_url` is set, commands go to `pookad` over
HTTP. Otherwise they go over SSH. `--transport ssh|daemon` on any command forces one.
The environment variables `SPRITES_API_URL` and `SPRITE_TOKEN` override `daemon_url`
and `token`, so one shell serves both `pooka` and the official `sprite` CLI.

The template runs an SSH server that accepts keys only and refuses root. `create` passes
the public key named by `ssh_pubkey` to the firstboot script as `POOKA_SSH_PUBKEY`. The
user can then reach the environment with `ssh pooka@<ip>` and with tools such as herdr.

A `.pooka` file in the current directory can name the active environment. The command
`pooka use <name>` writes it. Commands without a name use the active environment.

`pooka setup` writes the config file (ADR 0007). It connects to the host over SSH,
reads the thin storages, bridges, address and architecture, asks each question with a
default, builds the template if the VMID does not exist, installs `pookad`, saves the
token to `~/.config/pooka/token`, and verifies with a list. Every answer is a flag.
`--yes` takes the flags and defaults without asking. `--daemon=false` keeps SSH as the
transport. `--pookad <file>` pushes a local daemon binary instead of the release.

## 9. Create

`pooka create <name>` does these steps:

1. Validate the name. Allow lowercase letters, digits and hyphens. Maximum 32 characters.
2. Refuse if an environment with that name exists.
3. Pick the lowest free VMID in `vmid_range`.
4. Run `pct clone <template> <vmid> --hostname <name>`. Do not pass `--full`. This makes a
   linked clone.
5. Set cores, memory and the `pooka` tag. Set the description to a JSON object with the
   pooka version, the creation time and the template ID.
6. Resize the root disk to `disk_gb` if the template is smaller. A resize costs about
   23 s, so build the template at `disk_gb` (default 32 GB, thin) and this step is a no-op.
7. Run `pct start <vmid>`.
8. Wait until `pct exec <vmid> -- systemctl is-system-running` returns `running` or
   `degraded`. Time out after 30 seconds.
9. Run `pct exec <vmid> -- /etc/pooka/firstboot.sh` if the file exists. Pass the
   environment name in `POOKA_NAME` and the public key in `POOKA_SSH_PUBKEY`.
10. Print the name, VMID and IP address.

The firstboot script can set git identity, copy dotfiles and read secrets. pooka does
not manage secrets in phase 1. The user puts them in the firstboot script or copies them
with `pooka push`.

## 10. Commands

Each command maps to `pct` calls on the host over SSH. The table gives the mapping.

| Command | Host action |
|---|---|
| `pooka create <name>` | Section 9. |
| `pooka ls` | `pct list`, filtered by the `pooka` tag. Show name, VMID, status, IP. |
| `pooka exec <name> -- <cmd...>` | `lxc-attach` runs an exec supervisor and `runuser -l pooka --session-command '<cmd...>'`. Add `-t` for a TTY. `--root` runs as root. The supervisor ends the process session when the client disconnects. See ADR 0006. |
| `pooka console <name>` | `exec` of `bash -l` with a TTY. |
| `pooka push <name> <local> <remote>` | `scp` to host, then `pct push`. |
| `pooka pull <name> <remote> <local>` | `pct pull`, then `scp` from host. |
| `pooka checkpoint <name> [--comment c]` | `pct snapshot <vmid> cp-<unix-time> --description c`. |
| `pooka checkpoint ls <name>` | `pct listsnapshot <vmid>`. |
| `pooka checkpoint rm <name> <id>` | `pct delsnapshot <vmid> <id>`. |
| `pooka restore <name> <id>` | `pct stop`, `pct rollback <vmid> <id>`, then on `lvmthin` storage `tune2fs -f -E clear_mmp $(pvesm path <rootfs volid>)`, then `pct start`. See ADR 0003. |
| `pooka stop <name>` | `pct shutdown <vmid>`, then `pct stop` after 10 seconds. |
| `pooka start <name>` | `pct start <vmid>`. |
| `pooka destroy <name>` | `pct stop`, then `pct destroy <vmid> --purge`. |
| `pooka use <name>` | Write `.pooka` in the current directory. |
| `pooka setup` | Section 8. Copies the embedded `scripts/` to `/root/pooka-scripts`, runs `build-template.sh` and `install-daemon.sh` there as needed. |

Rules:

- Commands act only on containers with the `pooka` tag. Refuse all others.
- `restore` and `destroy` ask the user to type the name. The `--force` flag skips this.
- `exec` returns the exit code of the command inside the environment.
- All commands accept `-n <name>` as an alternative to the positional name.
- Over the daemon, `exec --root` uses the pooka extension route
  `/pooka/v1/sprites/{name}/exec?root=true`. The Sprites API has no run-as-root
  option.
- With a TTY, `exec` keeps the session that `lxc-attach` gave its terminal to
  (`runuser --session-command`), so the shell has job control and Ctrl-C works.
- Exec over SSH uses a dedicated connection. Other SSH calls use connection multiplexing.
- On Linux, killing the client also closes its dedicated SSH connection. On other
  client platforms, SSH cleanup requires normal exit or context cancellation.
- Over the daemon, `create` sends only the flags the user set. Unset flags take the
  daemon's defaults from `/etc/pooka/pookad.yaml`, not the client config.
- Over the daemon, `push` is limited to 16 MB per file. Use `--transport ssh` for
  larger files.

## 11. Name to VMID lookup

pooka stores the name as the container hostname. It finds a container by tag and
hostname. It never stores a name-to-VMID map on the client. This keeps Proxmox as the
only source of truth.

## 12. Transport

The CLI has two transports. Commands see only the `Backend` interface (section 13)
and must not depend on either.

SSH: the CLI runs `pct` over the system `ssh` binary so existing keys, agents and
config work. It enables SSH connection multiplexing (`ControlMaster`) so one handshake
serves every call in a command. This sits behind an interface named `Host` with
`Run(ctx, cmd)`, `CopyTo` and `CopyFrom`. Template builds always use SSH.

Daemon: the CLI is an HTTP and WebSocket client of `pookad` (section 16). The
`httpapi` backend maps each `Backend` method to a Sprites endpoint, or to a pooka
extension under `/pooka/v1` where Sprites has none: create with options, start,
stop and delete checkpoint. TTY exec puts the local terminal in raw mode and forwards
window size changes.

Section 8 gives the selection rule. Set `POOKA_DEBUG=1` to log every host call or
daemon request with its duration.

## 13. Backend interface

```go
type Backend interface {
    Create(ctx, name, opts) (Env, error)
    List(ctx) ([]Env, error)
    Get(ctx, name) (Env, error)
    Start(ctx, name) error
    Stop(ctx, name) error
    Destroy(ctx, name) error
    Exec(ctx, name, cmd, io) (exitCode int, err error)
    Push(ctx, name, local, remote) error
    Pull(ctx, name, remote, local) error
    Checkpoint(ctx, name, comment) (Checkpoint, error)
    ListCheckpoints(ctx, name) ([]Checkpoint, error)
    DeleteCheckpoint(ctx, name, id) error
    Restore(ctx, name, id) error
}
```

The LXC backend is the only implementation in phase 1. The QEMU backend in a later phase
implements the same interface.

## 14. Errors

- If the host is not reachable, say so and show the SSH error.
- If the template ID does not exist, say so and point to `scripts/build-template.sh`.
- If the storage is not thin, refuse to create and name the storage type.
- If a `pct` call fails, show its full stderr. Do not hide it.

## 15. Performance targets

Measure these on the real host before the first release. Record the numbers in
docs/benchmarks.md. First measurement on 2026-10-20: create 7.2 s, restore about 5 s,
all others within target.

| Operation | Target |
|---|---|
| clone + start + firstboot | under 5 s |
| start | under 2 s |
| checkpoint | under 3 s |
| restore | under 10 s |
| exec `true` | under 1 s |

## 16. Phase 2: the daemon

`pookad` runs on the host and serves the Sprites HTTP and WebSocket API. See ADR 0004
and docs/sprites-api/README.md for the endpoint map and the compatibility probe.

- Install with `scripts/install-daemon.sh <pookad binary>`. It writes
  `/etc/pooka/pookad.yaml`, generates `/etc/pooka/token`, and enables a systemd unit.
- Listens on `0.0.0.0:7700` by default. Plain HTTP. The LAN is the trust boundary.
- One bearer token. One organization, `local`.
- The official `sprite` CLI and SDKs target pooka with three environment variables:
  `SPRITES_API_URL=http://<host>:7700`, `SPRITE_TOKEN=<token>`, `SPRITE_ORG=local`.
- Implemented: sprites list, create, get, update (no-op), delete; checkpoint create,
  list, get, restore; exec over WebSocket with TTY, stdin and resize; exec over HTTP
  POST with framed output; filesystem read, write, list and chown; session list and
  kill.
- pooka extensions under `/pooka/v1`, for the `pooka` CLI only (ADR 0005): create
  with `cores`, `memory_mb`, `disk_gb` and `ssh_pubkey`; start; stop; delete
  checkpoint. docs/sprites-api/README.md lists them.
- Not implemented, returns 501: policies, services, OAuth connections, proxy, watch.
- Known limitation: attaching to an existing exec session returns 404. The official
  CLI's `exec --no-stdin` mode needs it. Use `--stdin` or `--http-post`.
- Status mapping: running is `running`; stopped is `cold`. No `warm`.
- The daemon runs commands locally; the LXC backend is unchanged. `POOKA_DEBUG=1` on
  the daemon logs every host call with its duration.
- The backend reads containers from `pct list` and the config files, never from the
  cluster resources endpoint, which lags several seconds behind changes.

## 17. Later phases

These are not part of phases 1 and 2. They set the direction.

- Phase 2b, done: the `pooka` CLI talks to `pookad` when `daemon_url` is set. See
  sections 8 and 12.
- Phase 3: a reverse proxy with a wildcard domain and TLS. A request to an
  environment's URL starts it. Idle environments stop after a timeout.
- Phase 4: an agent inside the environment for streaming exec, PTY, file watch and
  services.
- Phase 5: a QEMU backend on Proxmox, then a Firecracker backend beside Proxmox.
