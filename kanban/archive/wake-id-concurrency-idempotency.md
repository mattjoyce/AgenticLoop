---
id: 108
status: archive
priority: High
blocked_by: []
tags: [api, storage, idempotency, concurrency]
---

# #108: Make `wake_id` Idempotency Race-Safe

Eliminate duplicate wake race conditions so concurrent requests with the same `wake_id` always return the same run instead of sporadic 500 errors.

## Acceptance Criteria

- Concurrent wake requests with identical `wake_id` return one canonical run.
- Unique-constraint conflicts are handled as idempotent success, not internal errors.
- API behavior is deterministic (`existing=true` for duplicates).
- Add concurrency-focused tests for `RunStore.Create` and `POST /v1/wake`.

## Narrative

- 2026-02-20: Created from repository review; read-before-insert idempotency currently has a race window under concurrent wake requests. (by @assistant)
- 2026-02-20: Switched `RunStore.Create` to `INSERT ... ON CONFLICT(wake_id) DO NOTHING` with deterministic fetch of existing run on conflict, eliminating race-prone read-before-insert behavior. Added concurrency/idempotency tests in store and API wake-handler tests; validated with `go test ./internal/store ./internal/api`. (by @assistant)
