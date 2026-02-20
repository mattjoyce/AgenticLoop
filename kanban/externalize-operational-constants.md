---
id: 114
status: backlog
priority: Normal
blocked_by: []
tags: [config, operations, api, cli]
---

# #114: Externalize Hardcoded Operational Constants

Move key runtime constants into config with sensible defaults so operators can tune behavior without code changes.

## Acceptance Criteria

- Configurable runner queue size.
- Configurable SSE poll interval and heartbeat interval.
- Configurable watch polling interval.
- Provider-specific hardcoded values reviewed; only intentional constants remain.
- README and `config.yaml` document new knobs.

## Narrative

- 2026-02-20: Created from repository review; multiple timings/capacities are currently fixed in code and reduce operational control. (by @assistant)
