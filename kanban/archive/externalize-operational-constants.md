---
id: 114
status: archive
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
- 2026-02-20: Externalized operational constants: configurable API SSE polling/heartbeat intervals (`api.stream_poll_interval`, `api.stream_heartbeat_interval`), configurable watch polling interval (`--poll-interval`), and configurable LLM max tokens (`llm.max_tokens`) for anthropic. Queue size/timeout knobs from `#110` remain in place. Updated README/config docs and added config validation/default tests. (by @assistant)
