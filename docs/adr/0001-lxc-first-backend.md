# 0001: Phase 1 uses LXC through pct over SSH

Status: accepted
Date: 2026-10-20

## Context

The goal is a Sprites-like tool on Proxmox. Sprites use Firecracker microVMs with RAM
snapshots for sleep and wake. On Proxmox, QEMU VMs boot in 5 to 15 s and need a standby
pool to feel instant. Hibernate and resume are bound by RAM size. The first user already
runs coding agents in Ubuntu LXC containers and accepts that isolation level.

## Decision

Phase 1 uses LXC containers. The CLI runs `pct` on the host over SSH. There is no daemon,
no database and no agent inside the container. Proxmox is the only source of truth.

## Consequences

- Clone, start, checkpoint and restore are instant on LVM-thin or ZFS. No pool needed.
- Sleep is `pct stop`. Wake is `pct start`. No pause tiers.
- Isolation is kernel-shared. The tool must say so in its docs.
- A `Backend` interface exists from the first commit so QEMU and Firecracker can follow.
- Transport sits behind a `Host` interface so a daemon can replace SSH in phase 2.
