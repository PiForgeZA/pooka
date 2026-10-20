<p align="center">
  <img src="docs/assets/banner.jpg" alt="pooka: a glowing púca conjuring Linux containers over a server rack" width="100%">
</p>

# pooka

Spin up a fresh, isolated Linux environment on your own Proxmox hardware in seconds.

pooka is a self-hosted take on [fly.io Sprites](https://docs.fly.io/sprites): persistent,
disposable computers for coding agents and for you. A púca is a shapeshifting sprite from
Irish folklore. This one lives on Proxmox.

## Status

Pre-alpha. The name, the spec, the template build scripts and first benchmarks exist.
The CLI does not yet. See [SPEC.md](SPEC.md) for what phase 1 builds,
[docs/research.md](docs/research.md) for the investigation behind the design, and
[docs/benchmarks.md](docs/benchmarks.md) for measured numbers. First measurement on a
Proxmox 8.4 host with LVM-thin: create in 7.2 s, checkpoint in 0.9 s, restore in 6.8 s.

## What it will do

```
pooka create dev1            # linked clone of your template, started, ready in ~2 s
pooka exec dev1 -- claude    # run a command inside it
pooka console dev1           # interactive shell
pooka checkpoint dev1        # snapshot the filesystem
pooka restore dev1 <id>      # roll it back
pooka stop dev1              # sleep; start wakes it in under a second
pooka destroy dev1
```

Phase 1 uses Proxmox LXC containers on LVM-thin or ZFS. Clone, start, snapshot and
rollback are all instant on that storage. Later phases add a host daemon that speaks the
Sprites HTTP API, a per-environment URL that wakes it on request, and QEMU and Firecracker
backends for hardware isolation.

## Requirements

- A Proxmox VE host (8.x or 9.x) with a `lvmthin` or `zfspool` storage for containers.
- SSH access from your machine to that host.
- A template container with your tools baked in. The spec explains how to build one.

## Why not just use Sprites?

Use Sprites if you want Fly to run it. Use pooka if you already own the hardware, want
your agents' data to stay on your network, or want to understand the whole stack.

## Roadmap

1. LXC backend, CLI over SSH, template-based create, checkpoints. (phase 1)
2. Host daemon with the Sprites OpenAPI surface so the official SDKs and CLI can target it.
3. Per-environment HTTPS URL with wake-on-request and idle sleep.
4. In-environment agent: streaming exec, PTY, file watch, service supervisor.
5. QEMU backend on Proxmox, then Firecracker beside Proxmox.

## License

MIT. See [LICENSE](LICENSE).
