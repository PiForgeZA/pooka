# 0003: Clear the ext4 MMP block after rollback on LVM-thin

Status: accepted
Date: 2026-10-20

## Context

Proxmox formats LVM-thin container volumes with ext4 multi-mount protection (MMP). A
snapshot of a running container carries an MMP block marked in use. After `pct rollback`,
the first `pct start` waits about 43 s for the MMP interval before it mounts. Measured
restore was 48 s against a 10 s target. See docs/benchmarks.md.

Options:

1. Take checkpoints only when the container is stopped. Restore is fast, but a checkpoint
   interrupts work for about 5 s.
2. Disable MMP on the template filesystem with `tune2fs -O ^mmp`. One-time, 11 s. Removes
   a safety feature that stops a volume from being mounted twice.
3. Clear the MMP block on the rolled-back volume with `tune2fs -f -E clear_mmp`, before
   start. 40 ms. Keeps MMP enabled.

## Decision

Option 3. The restore path on the LXC backend for `lvmthin` storage is:
stop, rollback, `tune2fs -f -E clear_mmp <volume path>`, start. The volume path comes from
`pvesm path <volid>`. ZFS storage skips the step.

## Consequences

- Restore takes about 5 s. Checkpoints stay non-interrupting.
- The backend needs to know the storage type. It already checks this at create.
- `clear_mmp` must run only on a volume that is not mounted. The backend stops the
  container first, so this holds.
