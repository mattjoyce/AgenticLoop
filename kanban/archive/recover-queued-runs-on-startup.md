---
id: 109
status: archive
priority: High
blocked_by: []
tags: [runner, recovery, reliability]
---

# #109: Recover `queued` Runs on Service Restart

Ensure startup recovery re-enqueues runs left in `queued` state so work is not stranded after crashes or abrupt shutdowns.

## Acceptance Criteria

- Runner recovery includes both `running` and `queued` runs.
- Duplicate enqueue is prevented or harmless when recovery overlaps normal wake flow.
- Add tests covering restart scenarios with queued runs pending.
- Update docs to describe exact restart recovery semantics.

## Narrative

- 2026-02-20: Created from repository review; current recovery only includes `running` status and can miss queued runs stranded by restart timing. (by @assistant)
- 2026-02-20: Updated runner recovery to include both `running` and `queued` runs with deduped enqueue candidates. Added recovery test that verifies queued and running runs are both re-enqueued, and updated README wording to document restart semantics. (by @assistant)
