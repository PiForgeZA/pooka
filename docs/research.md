# Research: a Sprites-like tool on Proxmox

Investigation done 2026-10-20. This records what fly.io Sprites is, how its parts map to
Proxmox, and what prior art exists. SPEC.md turns this into a plan.

## 1. What a Sprite is

| Sprite feature | How Fly does it |
|---|---|
| Isolation | One Firecracker microVM per sprite, own kernel, 8 vCPU, memory grows under pressure. User code runs in an inner container. A root-namespace supervisor, storage stack and API server run beside it. Fly calls this "inside-out orchestration". |
| Create in 1 to 2 s | Each worker keeps a pool of pre-booted empty sprites on a standard Ubuntu image. Create assigns one from the pool. |
| Persistent 100 GB ext4 | First a modified JuiceFS: immutable chunks on S3, SQLite metadata made durable with Litestream, sparse NVMe as a read-through cache. Now moving to SBD, an S3-backed block device with ext4 on top. Thin. Billed per byte written. |
| Checkpoints, about 300 ms | Block-level copy-on-write snapshots. Auto-checkpoint hourly, on idle and on shutdown. Restore replaces the filesystem in place. Identity and URL do not change. |
| Warm and cold sleep | Warm: VM paused with RAM snapshotted, resume in 100 to 500 ms with memory intact. Cold: memory discarded, 1 to 2 s wake with a fresh boot. Services restart on cold wake. Manual processes do not. TCP connections never survive. |
| URL per sprite | `https://name-org.sprites.app`, routed to port 8080. Org-only auth by default. A request wakes the sprite. |
| Exec and files | HTTP and WebSocket API served from inside the VM: exec with and without TTY, file read and write, watch ports, watch filesystem, TCP proxy. |
| Services | In-VM supervisor with dependency order, backoff restart, definitions persisted to disk. |
| Policy | DNS-based egress allowlist, capability and device limits, memory limits, credential connectors brokered outside the VM. |
| Clients | `sprite` CLI, MIT SDKs for Go, JS, Python, Elixir, hosted MCP endpoint. The CLI takes an `api-url` argument on login. |

The runtime and control plane are closed. The OpenAPI and AsyncAPI specs are public. Fly
said in January 2026 that an open-source local runtime was in progress. As of August 2026
it had not shipped.

## 2. Mapping to Proxmox

| Sprite feature | Proxmox primitive | Gap |
|---|---|---|
| Create | `qm clone` or `pct clone` of a template on ZFS or LVM-thin is a linked clone, under 1 s. A QEMU cloud image boots in 5 to 15 s. An LXC starts in under 1 s. | For QEMU, pre-boot a standby pool. For LXC, no pool needed. |
| Persistent thin disk | Thin zvol or LVM-thin volume on the template. Linked clone. | Local disk is the source of truth. Durability comes from Proxmox Backup Server or `zfs send`. Linked clones pin an environment to the node that holds the template. |
| Checkpoint | `qm snapshot` or `pct snapshot` without vmstate is copy-on-write, about 1 s. | Comparable. |
| Restore | `qm rollback` or `pct rollback` | Proxmox stops the guest to roll back. Restore is stop, rollback, start. |
| Warm sleep | `qm suspend` without `--todisk` pauses the VM in host RAM. | Costs host RAM. Not available for LXC. LXC start is fast enough that stop and start replace it. |
| Cold sleep | `qm suspend --todisk` writes RAM to a vmstate volume. `qm start` resumes. | Bound by RAM size over disk bandwidth. A few seconds for 2 to 4 GB on NVMe. |
| Exec and files | QEMU guest agent via the API, or `pct exec`, `pct push`, `pct pull` for LXC. | Guest agent is poll-based with no TTY and small file limits. An in-VM agent is needed for the real product. |
| URL and wake | None built in. | Reverse proxy on the host with wildcard DNS and a wildcard cert, plus a resume-on-request hook. |
| Egress policy | Proxmox firewall per guest, IP and port level, via the API. | Domain allowlists need a filtering DNS or forward proxy on the host. |
| Memory elasticity | Balloon min and max plus KSM for QEMU. cgroup limits for LXC. | Close enough. |

Firecracker and Cloud Hypervisor run on a Proxmox host because it is Debian with KVM. The
Proxmox forum confirms a partner proof of concept. Proxmox does not manage those VMs. A
Firecracker backend must own TAP devices, raw disks and backups. It gets snapshot restore
in tens of milliseconds and diff snapshots.

## 3. Prior art

| Project | What it is | Relevance |
|---|---|---|
| Crabbox (MIT, Go) | Execution control plane with 80+ providers. Its Proxmox provider does full clone, cloud-init SSH key, guest-agent IP discovery, SSH exec, delete on release. | Proves the Proxmox clone-and-exec path. No sleep, snapshots, URLs or in-VM agent. |
| Perplexity SPACE (Apache-2, Rust, October 2026) | Firecracker sandbox platform with templates, warm pools, pause and wake, egress profiles, E2B-compatible wrapper. | Most complete open Firecracker runtime. Kubernetes-centric. |
| boxd (Rust, self-hostable, license pending) | Persistent KVM microVMs, Raft plus SQLite, sub-millisecond resume, copy-on-write fork of running VMs. | Closest to Sprites in spirit. Not open yet. |
| ForgeVM (Go) | Firecracker snapshot pool, vsock PID-1 agent, 28 ms restore. | Reference for a Firecracker backend and agent protocol. |
| microsandbox (Apache-2, libkrun) | Self-hosted microVM sandboxes, experimental. | Ephemeral. No persistence story. |
| E2B infra (Apache-2) | Firecracker fork, orchestrator plus host nodes, cloud-oriented. | Reference only. |

No project combines Proxmox with the Sprites model.

## 4. Decisions taken

- Phase 1 uses LXC. The user already accepts LXC isolation. LXC removes the need for
  pools, pause tiers and hibernate.
- Phase 1 has no daemon. The CLI runs `pct` over SSH. Proxmox is the source of truth.
- Later phases implement the Sprites OpenAPI spec so the official CLI and SDKs can target
  pooka. Whether the official CLI auth flow works against a third-party server is not
  verified.
- A `Backend` interface exists from the first commit. QEMU and Firecracker come later.

## 5. Sources

- Sprites docs: https://docs.fly.io/sprites, https://docs.fly.io/sprites/working-with-sprites/, https://docs.fly.io/sprites/cli/commands, https://docs.fly.io/llms.txt
- Fly design post: https://fly.io/blog/design-and-implementation/
- Fly, Agents need their own computer: https://fly.io/agents-need-a-computer/
- Fly vs Modal: https://fly.io/learn/fly-vs-modal/
- rywalker Sprites review: https://rywalker.com/research/sprites
- boxd vs Sprites: https://boxd.sh/blog/boxd-vs-sprites/
- sprites-go SDK: https://github.com/superfly/sprites-go
- Proxmox qm manual: https://pve.proxmox.com/pve-docs/qm.1.html
- Proxmox pct manual: https://pve.proxmox.com/pve-docs/pct.1.html
- Proxmox MicroVM support thread: https://forum.proxmox.com/posts/849706/
- Proxmox hibernate via qm: https://forum.proxmox.com/goto/post?id=312322
- Guest agent file-write limits: https://forum.proxmox.com/goto/post?id=454444
- Firecracker snapshot docs: https://github.com/firecracker-microvm/firecracker/blob/master/docs/snapshotting/snapshot-support.md
- Crabbox Proxmox provider: https://crabbox.sh/providers/proxmox.html
- Crabbox repo: https://github.com/openclaw/crabbox
- Perplexity SPACE: https://github.com/perplexityai/space
- ForgeVM write-up: https://dev.to/adwitiya/how-i-built-sandboxes-that-boot-in-28ms-using-firecracker-snapshots-i0k
- AI sandbox security study: https://arxiv.org/pdf/2606.08433
