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

Alpha, phase 3. The CLI works end to end on one Proxmox host with LXC containers,
either through the `pookad` daemon or over plain SSH. The daemon also serves the
Sprites API, so the official `sprite` CLI and SDKs work against it. Every
environment has an HTTPS URL that wakes it on request, and idle environments stop
after a timeout. Measured on a Proxmox 8.4 host with LVM-thin: create in about 8 s,
checkpoint in 0.9 s, restore in about 7 s, exec round trip under 0.1 s.

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
pooka url dev1 --open             # https://dev1-local.<domain> -> port 8080 inside; wakes it
pooka url dev1 --auth public      # let anyone who can reach the host open it
pooka idle dev1 2h                # stop after 2 h without activity (default 30 min; off)
ssh dev1.pooka                    # by name, via pooka ssh-proxy; wakes it when stopped
herdr --remote pooka@dev1.pooka   # any SSH tool works the same way; pooka ip dev1 prints the address
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
- SSH from your machine to the host as root, or as a user with `pct` rights, to
  build the template. `ssh proxmox true` must succeed without a password prompt.
- Go 1.24 or newer to build the CLI.

### 2. Install the CLI

```
curl -fsSL https://raw.githubusercontent.com/PiForgeZA/pooka/main/install.sh | sh
```

The script downloads the latest release for your OS and architecture, verifies it
against the release checksums, and puts `pooka` in `~/.local/bin`. It needs no root.
`--version v0.2.1` pins a release, `--bin-dir DIR` changes the target.

Later releases need no script: `pooka update` replaces the CLI and, once a host is
set up, the daemon on it. `pooka version` shows both versions.

Prefer not to pipe a script into a shell? Download the archive from a
[release](https://github.com/PiForgeZA/pooka/releases) and put `pooka` on your PATH,
or build from source with Go 1.24 or newer:

```
git clone https://github.com/PiForgeZA/pooka && cd pooka
go build -o ~/.local/bin/pooka ./cmd/pooka
```

### 3. Set up the host

```
pooka setup
```

It connects to your Proxmox host over SSH, asks a few questions with defaults
taken from the host (thin storage, bridge, template VMID, disk, cores, memory, your
public key, the domain for environment URLs), builds the template if it is missing,
installs the `pookad` daemon, saves its token and its CA certificate, and writes
`~/.config/pooka/config.yaml`. Every answer is also a flag; `pooka setup --yes`
takes the defaults without asking, and `--daemon=false` keeps SSH as the transport.
It ends with the one-time command that makes your browser trust the CA.

On a second machine, the same command joins the host: the defaults come from the
daemon that is already there, and the daemon is left alone when it runs the same
version. `pooka update --daemon` upgrades it; `pooka setup --reinstall` installs it
again.

Then:

```
pooka create dev1
pooka console dev1
```

That is the whole quick start. The rest of this section is the manual route, which
`pooka setup` automates.

### 4. Use it

`pooka ls` shows every environment. Add `POOKA_DEBUG=1` to any command to see each
request or host call with its duration.

Every environment has a URL, `https://<name>-local.<domain>`, served by the daemon
on port 443 and routed to port 8080 inside the environment. A request to a stopped
environment starts it and waits for the port. `pooka setup` defaults the domain to
`<host-ip>.sslip.io`, which any public resolver maps to the host, so no DNS entry is
needed on any client. By default only token holders may open a URL: `pooka url
<name>` prints a link that sets a cookie for that host, and programs send
`Authorization: Bearer <token>`. `pooka url <name> --auth public` opens it to anyone
who can reach the host. The certificate comes from a CA the daemon generates;
`pooka setup` saves it to `~/.config/pooka/ca.pem` and prints the one-time step
that makes browsers trust it.

SSH reaches an environment by name. `pooka setup` adds one `Host *.pooka` entry to
`~/.ssh/config` (or print it with `pooka ssh-config`), whose ProxyCommand asks pooka
for the address and starts the environment when it is stopped. So `ssh dev1.pooka`,
`herdr --remote pooka@dev1.pooka` and an IDE's remote SSH all work without looking
up an IP, on any network that reaches the daemon and the environment. Host keys go
to `~/.config/pooka/known_hosts`; `destroy` drops the entry so a new environment
under the same name is accepted.

An environment with no activity for 30 minutes stops. Activity is a URL request, an
exec session, a file or checkpoint call, or any established TCP connection inside
the environment, so an open SSH or IDE session keeps it up. `pooka idle <name> off`
opts one out; `idle_timeout` in `/etc/pooka/pookad.yaml` sets the default.

The official Sprites clients use the same daemon and token:

```
export SPRITES_API_URL=http://proxmox:7700
export SPRITE_TOKEN=$(cat ~/.config/pooka/token)
export SPRITE_ORG=local
sprite create dev1
sprite exec -s dev1 --stdin -- claude --version
sprite checkpoint create -s dev1
```

See `docs/sprites-api/README.md` for what is implemented and what is not.

### Manual route

#### Build the template, once

Copy the scripts to the host and run the build there as root. It takes a few minutes and
creates container 9000 on `local-lvm` at 32 GB thin. Override with `POOKA_TEMPLATE_VMID`,
`POOKA_STORAGE`, `POOKA_BRIDGE`, `POOKA_OSTEMPLATE` and `POOKA_TEMPLATE_DISK_GB`.

```
rsync -a scripts/ root@proxmox:/root/pooka-scripts/
ssh root@proxmox 'bash /root/pooka-scripts/build-template.sh'
```

Rebuild whenever you change the tool set: `pct destroy 9000 --purge`, then run the
script again. Existing environments keep their old tools.

#### Install the daemon on the host

`pookad` runs on the host, drives the containers locally, and serves the
[Sprites API](https://docs.fly.io/sprites/api/openapi.json). The `pooka` CLI, the
official `sprite` CLI and the Sprites SDKs all talk to it. Install it once; the
release archive and `go build ./cmd/pookad` both produce the binary.

```
scp pookad root@proxmox:/root/pooka-scripts/pookad
ssh root@proxmox 'bash /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad'
ssh root@proxmox cat /etc/pooka/token > ~/.config/pooka/token
```

The script generates the token, writes `/etc/pooka/pookad.yaml` with the defaults
for new environments and a `domain` of `<host-ip>.sslip.io`, and starts a systemd
unit. The daemon speaks plain HTTP on port 7700 and serves environment URLs and the
API over TLS on port 443 with a CA it generates at `/etc/pooka/tls/ca.pem`. Copy that
file to `~/.config/pooka/ca.pem` and point `daemon_url` at `https://api.<domain>`,
or keep port 7700 on your LAN.

If you would rather not run a daemon, skip this step. The CLI then runs `pct` over
SSH, which needs root SSH access from every machine you use pooka on.

#### Configure

pooka reads `~/.config/pooka/config.yaml`. Every key has a default, shown here. You
need the file for the two daemon keys; the rest only if your setup differs.

```yaml
daemon_url: https://api.192-168-68-200.sslip.io   # set this to use the daemon; or http://proxmox:7700
# ca_file: ~/.config/pooka/ca.pem    # the daemon's CA for an https daemon_url; this is the default
token_file: ~/.config/pooka/token     # or token: <value>
host: root@proxmox                    # ssh target; used by --transport ssh and template builds
template: 9000                        # VMID of the template container
storage: local-lvm                    # thin storage for environments
bridge: vmbr0
vmid_range: [9100, 9199]              # VMIDs pooka may use
disk_gb: 32                           # thin, so cheap
cores: 4
memory_mb: 8192
ssh_pubkey: ~/.ssh/id_ed25519.pub     # injected into each environment
```

With `daemon_url` set, every command goes to the daemon. Without it, every command
goes over SSH. `--transport ssh` or `--transport daemon` forces one for a single
command. The environment variables `SPRITES_API_URL` and `SPRITE_TOKEN` override the
two daemon keys, so one shell serves `pooka` and `sprite` alike. Over the daemon,
`create` uses the daemon's own defaults for cores, memory and disk unless you pass
the flags.

## How it works

Either the daemon on the host or the CLI over SSH runs `pct` and `lxc-attach`. There
is no database and nothing installed inside the environment beyond a first-boot
script. Proxmox is the only source of truth: pooka finds its environments by the
`pooka` tag and refuses to touch anything else. If pooka breaks, `pct` can do
everything by hand.

Over the daemon, the CLI is a client of the Sprites API: list, create, destroy,
checkpoints, a WebSocket for exec with a TTY, and the filesystem endpoints for push
and pull. Five small pooka extensions under `/pooka/v1` cover start, stop, delete
checkpoint, per-environment settings and create with options, which the Sprites
spec does not have.

The daemon's proxy on 443 routes on the first DNS label only: `dev1-local` in
`dev1-local.192-168-68-200.sslip.io`. The rest of the name is ignored, so any name
that resolves to the host works, including one on your own LAN DNS. To move to one,
add a wildcard record for `*.<domain>` to the host, set `domain` in
`/etc/pooka/pookad.yaml`, and run `pooka setup --yes --reinstall`: it restarts the
daemon and points `daemon_url` at `https://api.<domain>`. The daemon issues a new
certificate from the same CA, so clients keep their `ca.pem`. The host name
`api.<domain>` on the same listener serves the API over TLS. The URL auth setting
and the idle timeout live in the container description, next to the creation time,
so Proxmox stays the only state store (ADR 0008, 0009).

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

Layout: `cmd/pooka` and `cmd/pookad` are the mains, `internal/cli` the cobra
commands, `internal/backend` the `Backend` interface, `internal/backend/lxc` the
Proxmox implementation, `internal/backend/httpapi` the client of the daemon,
`internal/daemon` the Sprites API server and the idle loop, `internal/proxy` the
URL proxy, `internal/pki` the generated CA, `internal/host` the SSH and local
transports with a scripted fake for tests. Read [AGENTS.md](AGENTS.md) before
contributing; design decisions live in `docs/adr/`.

## Roadmap

1. LXC backend, CLI over SSH, template-based create, checkpoints. **Done, phase 1.**
2. Host daemon with the Sprites OpenAPI surface so the official SDKs and CLI can target
   it; the pooka CLI uses it too. **Done, phase 2.**
3. Per-environment HTTPS URL with wake-on-request and idle stop. **Done, phase 3.**
4. In-environment agent: streaming exec, PTY, file watch, service supervisor.
5. QEMU backend on Proxmox, then Firecracker beside Proxmox for hardware isolation.

## License

MIT. See [LICENSE](LICENSE).
