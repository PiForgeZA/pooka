# Handoffs

One file per session handoff. Number them in order. A handoff lets an agent with no
context continue the work.

Frontmatter template:

```
---
date: YYYY-MM-DD
from: <agent or person>
focus: <what the next session should do>
---
```

Sections: State, Facts needed to continue, Next steps in order, Suggested skills.

| Handoff | Focus |
|---|---|
| [0001](0001-project-bootstrap.md) | Publish repo, build template, scaffold CLI (done, v0.1.0) |
| [0002](0002-phase-2-daemon.md) | Phase 2: host daemon with the Sprites HTTP API (done except CLI client) |
| [0003](0003-phase-2b-cli-http-backend.md) | Phase 2b: pooka CLI uses the daemon by default, SSH as fallback (code and docs done; see 0004 for the live check) |
| [0004](0004-phase-2b-deploy-and-live-check.md) | Phase 2b deployed and verified live; commit, push, open the PR |
| [0005](0005-phase-3-urls-wake-idle.md) | Phase 3: URL per environment, TLS, wake-on-request, idle stop |
| [0006](0006-issue-3-exec-orphans.md) | Issue #3: exec process outlives the client (done, merged in v0.2.1) |
| [0007](0007-phase-3-built-and-verified.md) | Phase 3 built and verified live (done, merged as #7) |
| [0008](0008-ssh-by-name.md) | SSH by name: ssh-proxy, ssh-config, ip (done, merged as #8, released in v0.4.0) |
| [0009](0009-post-v0.4.0-follow-ups.md) | Follow-ups after v0.4.0: custom domain test, live cert renewal, URLs in ls, sprite url check, activity refactor, tunnel ADR, `pooka upgrade` |
