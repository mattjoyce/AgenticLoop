---
id: 111
status: archive
priority: Normal
blocked_by: []
tags: [runner, loop, storage, reliability]
---

# #111: Make Fail Path Terminal-State Writes Reliable

Stop silently ignoring database write failures when marking runs failed, and define fallback behavior when terminal state persistence fails.

## Acceptance Criteria

- Failure path does not discard `UpdateStatus` errors silently.
- Logs include explicit severity/context when terminal-state persistence fails.
- Run termination flow guarantees best-effort consistency for DB status and callback emission.
- Add tests for DB-write failure behavior in fail paths.

## Narrative

- 2026-02-20: Created from repository review; fail path currently ignores run status update errors and can leave runs in non-terminal states. (by @assistant)
- 2026-02-20: Updated fail path to log run-status persistence failures explicitly and return a wrapped error that includes both root failure and status-write failure details, while still attempting callback emission for best-effort terminal signaling. Added loop fail-path test using closed DB to validate behavior. (by @assistant)
