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
| [0003](0003-phase-2b-cli-http-backend.md) | Phase 2b: pooka CLI uses the daemon by default, SSH as fallback |
