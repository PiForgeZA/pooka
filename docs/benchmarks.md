# Benchmarks

Measured with `scripts/bench.sh` and manual runs on the real host. Update this file when
the numbers change.

## 2026-10-20, host `proxmox`, PVE 8.4.1, LVM-thin on `local-lvm`

Host: 24 cores, 31 GB RAM, kernel 6.8.12-10-pve. Template 9000, 16 GB thin, Ubuntu 24.04,
4.1 GB used after provisioning.

| Operation | Measured | Target (SPEC §15) | Status |
|---|---|---|---|
| clone (linked) | 1.85 s | | |
| start | 2.04 s | under 2 s | borderline |
| wait until systemd ready | 2.17 s | | |
| firstboot | 1.14 s | | |
| **create total** (clone + start + ready + firstboot) | **7.2 s** | under 5 s | missed by 2 s, acceptable for phase 1 |
| exec `true` (via ssh + pct exec) | 0.56 s | under 1 s | ok |
| checkpoint (`pct snapshot`, container running) | 0.92 s | under 3 s | ok |
| rollback (`pct rollback`) | 0.89 s | | |
| start after rollback, naive | 44 to 48 s | | see below |
| start after rollback, with `clear_mmp` | 2.02 s | | |
| **restore total** (stop + rollback + clear_mmp + start) | **6.8 s** (bench.sh re-run) | under 10 s | ok |
| stop (`pct stop`) | 1.9 s | | |
| shutdown (`pct shutdown`) | 2.5 s | | |
| destroy | 2.75 s | | |

## Finding: ext4 MMP makes a rolled-back volume wait 43 s to mount

Proxmox formats LVM-thin container volumes with the ext4 `mmp` feature (multi-mount
protection). A snapshot of a mounted filesystem carries an MMP block that says "in use".
When the rolled-back volume is mounted, the kernel waits out the protection interval:

```
EXT4-fs warning (device dm-13): ext4_multi_mount_protect:328: MMP interval 42 higher than expected, please wait.
```

Checked and ruled out: `fsck` (not running), `sync` before snapshot (no change),
`fsfreeze` before snapshot (no change), snapshot of a stopped container (fast, 2 s,
because the MMP block is clean).

Fixes measured:

| Fix | Cost | Start after rollback |
|---|---|---|
| `tune2fs -f -E clear_mmp <lv>` after rollback, before start | 0.04 s | 2.02 s |
| `tune2fs -O ^mmp <lv>` once per volume | 11 s once | 2.03 s |

Decision: run `clear_mmp` in the restore path. See ADR 0003. ZFS subvolumes are not ext4
and do not have this problem.

## Where create time goes

Start (2.0 s) and waiting for systemd (2.2 s) dominate. Candidates to cut later: trim
template services, mask units that are not needed in a container, shrink firstboot.

## 2026-10-20, first end-to-end run of the Go CLI

`pooka create` measured 33.5 s with `POOKA_DEBUG=1`. Breakdown:

| Step | Time | Note |
|---|---|---|
| `pct resize rootfs 32G` | 23.4 s | template was 16 GB. Fix: build template at 32 GB, skip resize. |
| each `pct` call | 0.55 s | perl startup. SSH itself is 0.01 s with multiplexing. |
| `pct exec` vs `lxc-attach` | 0.57 s vs 0.007 s | exec, firstboot and the ready wait now use lxc-attach. |
| `pct clone` | 1.2 s | |
| `pct start` | 2.0 s | |

After the template rebuild at 32 GB: `pooka create` 8.7 s end to end (resources 0.9,
config 0.6, clone 1.2, set 0.6, config 0.6, start 2.0, systemd ready wait 2.5, firstboot
0.5). Remaining fat: the four `pct` calls at 0.55 s each, and the systemd wait.

## 2026-10-21, through the daemon

`sprite create` via the official CLI against pookad on the host: 7.9 s. The daemon runs
`pct` locally, so the SSH round trips of the CLI path are gone; what remains is pct's
own startup per call and the systemd ready wait.

## 2026-10-21, pooka CLI over both transports (phase 2b)

Wall-clock from the dev box (LXC 111 on the same host), one run each, container
running unless noted. Same code path on the host for both transports.

| Operation | SSH | Daemon | Note |
|---|---|---|---|
| create | 8.3 s | 7.9 s | |
| checkpoint | 1.6 s | 3.5 s | daemon: `Get` before the stream (0.7 s) and the checkpoint list after it (1.3 s) to learn the id, per ADR 0005 |
| restore | 7.2 s | 7.7 s | |
| stop | 2.7 s | 2.7 s | |
| start | 2.7 s | 2.7 s | |
| exec `id` | under 1 s | under 1 s | |

Checkpoint over the daemon misses the 3 s target by 0.5 s. Two fixes are available
when it matters: drop the extra `Get` in the daemon's checkpoint handler, or put the
new id on the stream's `complete` event as an extra field and skip the list.

## 2026-10-22, phase 3: URLs, wake on request, idle stop

Same host. pookad built from the phase-3 branch, proxy on 443 with the generated CA,
domain `192-168-68-200.sslip.io`. Measured from the dev container (LXC 111) with
`curl --cacert ca.pem` and `POOKA_DEBUG=1`.

| Operation | Measured | Target (SPEC §18) | Status |
|---|---|---|---|
| `pooka ls` over TLS at `api.<domain>` | 0.66 s | | same as plain HTTP on 7700 |
| URL request, running environment, cookie auth | under 0.1 s | | |
| URL request, `?token=` sets the cookie and redirects | 1 redirect | | |
| **wake by URL request** (stopped, python http.server as a systemd unit) | **4.3 to 9.6 s** (two runs) | under 30 s, then 502 | ok |
| second request, environment running | 0.004 s | | |
| `pooka create --idle-timeout 2m` | 9.5 s | | create unchanged |

The wake time is `pct start` (about 2.7 s) plus the container's boot to the point
where the service listens on 8080 plus DHCP. The proxy polls the port every 250 ms, so
the wait is the service's own start time, not a timer.

The idle loop's connection check, `ss -Htn state established` run as root inside the
container, lists an open SSH login from the LAN as one established line and nothing
once the login ends. See the idle stop live check in handoff 0007.
