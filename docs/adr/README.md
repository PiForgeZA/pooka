# Architecture Decision Records

One file per decision. Number them in order. Do not edit a decided ADR. Write a new one
that supersedes it.

Template:

```
# NNNN: Title

Status: proposed | accepted | superseded by NNNN
Date: YYYY-MM-DD

## Context
## Decision
## Consequences
```

| ADR | Title | Status |
|---|---|---|
| [0001](0001-lxc-first-backend.md) | Phase 1 uses LXC through pct over SSH | accepted |
| [0002](0002-sprites-compatible-surface.md) | Follow the Sprites vocabulary and API | accepted |
| [0003](0003-clear-mmp-on-restore.md) | Clear the ext4 MMP block after rollback on LVM-thin | accepted |
| [0004](0004-daemon-with-sprites-api.md) | Phase 2 daemon speaks the Sprites API; official CLI compatibility confirmed | accepted |
| [0005](0005-cli-transport-and-pooka-extensions.md) | The CLI selects SSH or daemon transport; pooka extensions live under /pooka/v1 | accepted |
| [0006](0006-exec-process-lifetime.md) | Exec owns its process session | accepted |
| [0007](0007-setup-command-and-installer.md) | `pooka setup` provisions the host; the host scripts ship inside the binary | accepted |
| [0008](0008-urls-proxy-and-tls.md) | Each environment gets an HTTPS URL served by a proxy inside pookad | accepted |
| [0009](0009-idle-stop.md) | Idle environments stop after a timeout, unless a TCP connection is open inside | accepted |
