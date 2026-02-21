---
id: 112
status: archive
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

## Compatibility

No schema migration required. The runtime now actively uses the existing
`steps.status` lifecycle values (`pending -> running -> ok|error`) and existing
`steps.attempt` column to record retry counts.

## Narrative

- 2026-02-20: Created from repository review; current model stores lifecycle fields that are mostly unused by loop execution. (by @assistant)
- 2026-02-20: Chose to keep `running/error` statuses and `attempt` rather than remove columns. Updated loop stage persistence to record running/error/ok transitions and retry attempts for FRAME/PLAN/ACT/REFLECT execution paths; DONE stage now also marks running before ok. Added `StepStore.UpdateStatusWithAttempt` and store tests validating attempt/status/timestamps. (by @assistant)
