# AGENTS.md

Instructions for coding agents working in this repository.

Context:
@SPEC.md
@docs/research.md
@docs/adr/README.md

## What this project is

pooka is a CLI, and later a daemon, that creates and manages isolated Linux environments
on a Proxmox VE host. It copies the user-facing model of fly.io Sprites. Read SPEC.md
before you change behaviour. Read docs/research.md if you need the reasoning. Read the
ADR index before you make a design decision, and write a new ADR when you make one.

## How to work

- Keep responses short. Sacrifice grammar for concision.
- Use ASD-STE100 for documentation, specifications and communication: short sentences,
  active voice, one instruction per sentence, no idioms.
- Write plans detailed enough for a `/handoff` to an agent with no context, and simple
  enough for a junior developer new to the project.
- Do not commit or push unless asked. Leave changes for human review.
- Do not modify anything outside this repository. If something outside is needed, raise
  it. Do not do it unless told.

## Documentation layout

```
SPEC.md                 # phase 1 specification
docs/
  research.md           # the investigation behind the design
  benchmarks.md         # measured numbers on the real host (to be created)
  adr/
    README.md           # index of every ADR and its status
    0001-*.md
  handoffs/
    README.md           # frontmatter template and index
    0001-*.md
```

Track all documentation under `docs/`, except SPEC.md which stays at the root.

## Ground rules

- Go is the implementation language. Target the latest stable Go. One binary, no CGO.
- Keep a `Backend` interface between commands and the hypervisor. LXC is the first
  implementation. Do not let `pct` calls leak outside the LXC backend package.
- Use Sprites vocabulary: create, exec, console, checkpoint, restore, stop, start,
  destroy, services. Do not invent synonyms.
- Phase 1 has no daemon, no database and no in-container agent. Proxmox is the source of
  truth. Do not add state stores without a spec change and an ADR.
- Every destructive command (destroy, restore) needs the name typed or a `--force` flag.

## Environment facts

- The dev box is itself an LXC container (ID 111) on the target Proxmox host. It has no
  `/dev/kvm`. It can build and run the CLI but cannot host environments.
- The Proxmox host is reachable as `root@proxmox` over SSH. Container storage for pooka
  is `local-lvm` (LVM-thin). `nvme_storage` is a directory storage and does not support
  linked clones or snapshots. Never place pooka environments there.
- Go may not be installed on the dev box. Check with `go version` before building.

## Testing

- Unit tests must run without a Proxmox host. Mock the SSH and `pct` layer.
- Integration tests run against the real host and are tagged `//go:build integration`.
  They create containers with the tag `pooka-test` and must destroy them on exit.
- Never run integration tests against containers that lack the `pooka` tag.

## Commits

When asked to commit, use conventional commits: `feat:`, `fix:`, `docs:`, `refactor:`,
`test:`, `chore:`. One logical change per commit. Keep SPEC.md in step with behaviour.
