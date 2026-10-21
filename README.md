<p align="center">
  <img src="docs/assets/banner.jpg" alt="pooka: a glowing púca conjuring Linux containers over a server rack" width="100%">
</p>

# pooka

[![CI](https://github.com/PiForgeZA/pooka/actions/workflows/ci.yml/badge.svg)](https://github.com/PiForgeZA/pooka/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/PiForgeZA/pooka?include_prereleases)](https://github.com/PiForgeZA/pooka/releases)

Spin up a fresh, isolated Linux environment on your own Proxmox hardware in seconds.

pooka is a self-hosted take on [fly.io Sprites](https://docs.fly.io/sprites): persistent,
disposable computers for coding agents and for you. A púca is a shapeshifting sprite from
Irish folklore. This one lives on Proxmox.

## Status

Alpha, phase 1. The CLI works end to end on one Proxmox host with LXC containers.
Measured on a Proxmox 8.4 host with LVM-thin: create in 8.7 s, checkpoint in 0.9 s,
restore in about 7 s, exec round trip under 0.1 s.

Phase 1 trades hardware isolation for speed and simplicity: environments are LXC
containers and share the host kernel. See [SPEC.md](SPEC.md) for the exact behaviour,
[docs/research.md](docs/research.md) for why, and [docs/benchmarks.md](docs/benchmarks.md)
for the numbers.

```
pooka create dev1                 # linked clone of your template, started, ready in seconds
pooka exec dev1 -- claude         # run a command inside it, as the pooka user
pooka console dev1                # interactive shell
pooka checkpoint dev1             # snapshot the filesystem (prints the checkpoint id)
pooka checkpoint ls dev1
pooka restore dev1 cp-1792506345  # roll back; asks you to type the name
pooka stop dev1                   # sleep; start wakes it in about 2 s
pooka start dev1
pooka push dev1 ./notes.md /home/pooka/notes.md
pooka pull dev1 /home/pooka/out.txt ./out.txt
pooka use dev1                    # make dev1 the default for this directory (.pooka)
pooka destroy dev1                # delete it and its checkpoints
```

Every environment comes from one template that you build once. The default template
has Ubuntu 24.04, git and build tools, mise-managed Node, Python and Go, Claude Code,
Codex, herdr, and an SSH server that accepts your key. Edit
`scripts/template/mise.toml` and `scripts/template/provision.sh` to change that.

## Quick start

### 1. Requirements

- A Proxmox VE host, 8.x or 9.x.
- A storage of type `lvmthin` or `zfspool` that allows containers (`rootdir`). Check
  with `pvesm status`. Directory storage will not work: no linked clones, no snapshots.
- SSH from your machine to the host as root, or as a user with `pct` rights.
  `ssh proxmox true` must succeed without a password prompt.
- Go 1.24 or newer to build the CLI.

### 2. Get the CLI

From a [release](https://github.com/PiForgeZA/pooka/releases): download the archive
for your platform, unpack it, put `pooka` on your PATH. The archive also contains the
`scripts/` directory used in step 4.

Or build from source with Go 1.24 or newer:

```
git clone https://github.com/PiForgeZA/pooka
cd pooka
go build -o pooka ./cmd/pooka
cp pooka ~/.local/bin/        # or anywhere on your PATH
```

### 3. Configure

pooka reads `~/.config/pooka/config.yaml`. Every key has a default, shown here. You only
need the file if your setup differs.

```yaml
host: root@proxmox            # ssh target; an alias from ~/.ssh/config works
template: 9000                # VMID of the template container
storage: local-lvm            # thin storage for environments
bridge: vmbr0
vmid_range: [9100, 9199]      # VMIDs pooka may use
disk_gb: 32                   # thin, so cheap
cores: 4
memory_mb: 8192
ssh_pubkey: ~/.ssh/id_ed25519.pub   # injected into each environment
```

### 4. Build the template, once

Copy the scripts to the host and run the build there as root. It takes a few minutes and
creates container 9000 on `local-lvm` at 32 GB thin. Override with `POOKA_TEMPLATE_VMID`,
`POOKA_STORAGE`, `POOKA_BRIDGE`, `POOKA_OSTEMPLATE` and `POOKA_TEMPLATE_DISK_GB`.

```
rsync -a scripts/ root@proxmox:/root/pooka-scripts/
ssh root@proxmox 'bash /root/pooka-scripts/build-template.sh'
```

Rebuild whenever you change the tool set: `pct destroy 9000 --purge`, then run the
script again. Existing environments keep their old tools.

### 5. Use it

```
pooka create dev1
pooka console dev1
```

`pooka ls` shows every environment. Add `POOKA_DEBUG=1` to any command to see each
call to the host with its duration.

### 6. Optional: the daemon and the official Sprites clients

`pookad` runs on the host and serves the [Sprites API](https://docs.fly.io/sprites/api/openapi.json),
so the official `sprite` CLI and the Sprites SDKs for Go, JavaScript, Python and
Elixir work against your Proxmox host. Install it once:

```
ssh root@proxmox 'bash /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad'
```

The script generates a token in `/etc/pooka/token` and starts a systemd unit on port
7700. Then, on any machine with the official CLI:

```
export SPRITES_API_URL=http://proxmox:7700
export SPRITE_TOKEN=<contents of /etc/pooka/token>
export SPRITE_ORG=local
sprite create dev1
sprite exec -s dev1 --stdin -- claude --version
sprite checkpoint create -s dev1
```

The daemon speaks plain HTTP. Keep port 7700 on your LAN until phase 3 adds TLS.
See `docs/sprites-api/README.md` for what is implemented and what is not.

## How it works

The CLI runs `pct` and `lxc-attach` on the host over SSH. There is no daemon, no
database and nothing installed inside the environment beyond a first-boot script.
Proxmox is the only source of truth: pooka finds its environments by the `pooka` tag
and refuses to touch anything else. If pooka breaks, `pct` can do everything by hand.

- create: `pct clone` of the template, `pct set`, `pct start`, wait for systemd, run
  `/etc/pooka/firstboot.sh` with your SSH key.
- checkpoint: `pct snapshot`. restore: stop, `pct rollback`, clear the ext4 MMP block
  (see ADR 0003, otherwise the next mount waits 43 s), start.
- exec: `lxc-attach` as the `pooka` user with a login shell, so mise tools are on PATH.

## Developing

```
go test ./...        # unit tests, no host needed
go vet ./...
```

CI runs gofmt, vet, tests, shellcheck, a cross-compile matrix and a GoReleaser config
check on every push and pull request. Pushing a tag `vX.Y.Z` builds release archives
for Linux and macOS, amd64 and arm64, and publishes them with a changelog grouped by
conventional commit type.

Layout: `cmd/pooka` is main, `internal/cli` the cobra commands, `internal/backend` the
`Backend` interface, `internal/backend/lxc` the only implementation, `internal/host`
the SSH transport and a scripted fake for tests. Read [AGENTS.md](AGENTS.md) before
contributing; design decisions live in `docs/adr/`.

## Roadmap

1. LXC backend, CLI over SSH, template-based create, checkpoints. **Done, phase 1.**
2. Host daemon with the Sprites OpenAPI surface so the official SDKs and CLI can target it.
3. Per-environment HTTPS URL with wake-on-request and idle sleep.
4. In-environment agent: streaming exec, PTY, file watch, service supervisor.
5. QEMU backend on Proxmox, then Firecracker beside Proxmox for hardware isolation.

## License

MIT. See [LICENSE](LICENSE).
