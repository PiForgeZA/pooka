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

pooka ships a script, `scripts/build-template.sh`, that does steps 1 to 7.

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
```

The template runs an SSH server that accepts keys only and refuses root. `create` passes
the public key named by `ssh_pubkey` to the firstboot script as `POOKA_SSH_PUBKEY`. The
user can then reach the environment with `ssh pooka@<ip>` and with tools such as herdr.

A `.pooka` file in the current directory can name the active environment. The command
`pooka use <name>` writes it. Commands without a name use the active environment.

## 9. Create

`pooka create <name>` does these steps:

1. Validate the name. Allow lowercase letters, digits and hyphens. Maximum 32 characters.
2. Refuse if an environment with that name exists.
3. Pick the lowest free VMID in `vmid_range`.
4. Run `pct clone <template> <vmid> --hostname <name>`. Do not pass `--full`. This makes a
   linked clone.
5. Set cores, memory and the `pooka` tag. Set the description to a JSON object with the
   pooka version, the creation time and the template ID.
6. Resize the root disk to `disk_gb` if the template is smaller.
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
| `pooka exec <name> -- <cmd...>` | `pct exec <vmid> -- runuser -l pooka -c '<cmd...>'`. Add `-t` for a TTY. `--root` runs as root. |
| `pooka console <name>` | `ssh -t host pct exec <vmid> -- su - pooka`. |
| `pooka push <name> <local> <remote>` | `scp` to host, then `pct push`. |
| `pooka pull <name> <remote> <local>` | `pct pull`, then `scp` from host. |
| `pooka checkpoint <name> [--comment c]` | `pct snapshot <vmid> cp-<unix-time> --description c`. |
| `pooka checkpoint ls <name>` | `pct listsnapshot <vmid>`. |
| `pooka checkpoint rm <name> <id>` | `pct delsnapshot <vmid> <id>`. |
| `pooka restore <name> <id>` | `pct stop`, `pct rollback <vmid> <id>`, `pct start`. |
| `pooka stop <name>` | `pct shutdown <vmid>`, then `pct stop` after 10 seconds. |
| `pooka start <name>` | `pct start <vmid>`. |
| `pooka destroy <name>` | `pct stop`, then `pct destroy <vmid> --purge`. |
| `pooka use <name>` | Write `.pooka` in the current directory. |

Rules:

- Commands act only on containers with the `pooka` tag. Refuse all others.
- `restore` and `destroy` ask the user to type the name. The `--force` flag skips this.
- `exec` returns the exit code of the command inside the environment.
- All commands accept `-n <name>` as an alternative to the positional name.

## 11. Name to VMID lookup

pooka stores the name as the container hostname. It finds a container by tag and
hostname. It never stores a name-to-VMID map on the client. This keeps Proxmox as the
only source of truth.

## 12. Transport

Phase 1 runs `pct` over SSH. The CLI opens one SSH connection per command. It uses the
system `ssh` binary so existing keys, agents and config work. Put this behind an
interface named `Host` with one method: `Run(ctx, args, stdin, stdout, stderr)`.

Phase 2 replaces this with an HTTP client to a daemon on the host. Commands must not
depend on SSH details.

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
docs/benchmarks.md.

| Operation | Target |
|---|---|
| clone + start + firstboot | under 5 s |
| start | under 2 s |
| checkpoint | under 3 s |
| restore | under 10 s |
| exec `true` | under 1 s |

## 16. Later phases

These are not part of phase 1. They set the direction.

- Phase 2: a daemon on the host that implements the Sprites OpenAPI spec. The official
  `sprite` CLI and SDKs may then target pooka.
- Phase 3: a reverse proxy with a wildcard domain. A request to an environment's URL
  starts it. Idle environments stop after a timeout.
- Phase 4: an agent inside the environment for streaming exec, PTY, file watch and
  services.
- Phase 5: a QEMU backend on Proxmox, then a Firecracker backend beside Proxmox.
