---
date: 2026-10-20
from: Claude Fable 5.1
focus: Publish repo, build the template container, scaffold the Go CLI
---

# 0001: Project bootstrap

## State

- Name decided: pooka. Rejected pixie (collides with CNCF pixie-io/pixie), puck, wisp.
- Docs written: README.md, AGENTS.md, SPEC.md, docs/research.md, docs/adr/0001 and 0002,
  LICENSE (MIT), .gitignore. One local commit on `main`.
- No code yet. Go is not installed on the dev box.
- GitHub target not confirmed. `gh` is logged in as `wpieters`. `piforge` is a separate
  GitHub user account. The user belongs to org `PiForgeZA`. Ask before pushing.

## Facts needed to continue

- Dev box is LXC 111 on the Proxmox host. No `/dev/kvm`. Disk 95% full (2.2 GB free).
- Host is `root@proxmox` over SSH. SSH from the dev box is not yet verified.
- Storage: `local-lvm` is LVM-thin with about 345 GB free. Use it. `nvme_storage` is a
  directory storage with no clone or snapshot support. CTs 100 to 105 are on local-lvm,
  106 to 111 on nvme_storage.
- Proxmox VE 8.4, kernel 6.8.12-10-pve.

## Done after the first commit

- Repo published: https://github.com/PiForgeZA/pooka (public).
- SSH from the dev box to the host works as `ssh proxmox` (host 192.168.68.200, PVE 8.4.1,
  24 cores, 31 GB RAM).
- Decisions: mise manages runtimes and agents via `scripts/template/mise.toml`; herdr
  is in the default tool set; the template runs sshd (keys only, no root) and `create`
  injects the user's public key via `POOKA_SSH_PUBKEY`; commands run as user `pooka`.
- Scripts written: `scripts/build-template.sh`, `scripts/bench.sh`,
  `scripts/template/{provision.sh,firstboot.sh,mise.toml}`. Copied to
  `/root/pooka-scripts` on the host. Template 9000 build started 2026-10-20.

## Next steps in order

1. Confirm template 9000 exists (`pct config 9000`). Rebuild if the build failed.
2. Run `bash /root/pooka-scripts/bench.sh` on the host. Record in docs/benchmarks.md.
3. Install Go on the dev box. Disk is tight, so free space or put `GOCACHE` and
   `GOMODCACHE` on a larger mount first.
4. Scaffold the Go CLI: `Host` interface over SSH, `Backend` interface, LXC backend,
   commands from SPEC.md section 10. Unit tests mock `Host`.
5. Commit the scripts and spec changes (user commits; see AGENTS.md).

## Suggested skills

- `tdd` for the Go CLI with a mocked `Host`.
- `mattpocock-skills:wizard` if the template build needs human steps on the host.
- `to-issues` once the spec is stable, to split phase 1 into tracker issues.
