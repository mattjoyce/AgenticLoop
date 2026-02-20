---
id: 112
status: backlog
priority: Normal
blocked_by: []
tags: [store, loop, cleanup, design]
---

# #112: Clean Up Step Lifecycle Model Deadwood

Align persisted step lifecycle fields with actual runtime usage, or implement the missing transitions so fields are meaningful.

## Acceptance Criteria

- Decide whether to keep or remove `running/error` step states and `attempt`.
- If kept, runtime writes real transitions and retries into step records.
- If removed, schema/types/tests/docs are simplified accordingly.
- Migration or compatibility approach is documented.

## Narrative

- 2026-02-20: Created from repository review; current model stores lifecycle fields that are mostly unused by loop execution. (by @assistant)
