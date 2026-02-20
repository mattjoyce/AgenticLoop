---
id: 110
status: archive
priority: High
blocked_by: []
tags: [runner, api, scalability, config]
---

# #110: Add Backpressure Control for Runner Queue

Prevent API request hangs when the runner queue is full by making enqueue behavior explicit and configurable.

## Acceptance Criteria

- Queue capacity is configurable via `config.yaml`.
- Enqueue behavior is explicit under pressure (reject fast with 429/503, or timeout-based enqueue).
- `POST /v1/wake` does not block indefinitely on full queue.
- Add tests for full-queue behavior and returned API status.

## Narrative

- 2026-02-20: Created from repository review; enqueue is currently a blocking channel send with fixed capacity, which can stall request handling under load. (by @assistant)
- 2026-02-20: Added configurable runner queue controls (`agent.queue_capacity`, `agent.enqueue_timeout`) and made enqueue return explicit `queue full` errors instead of blocking indefinitely. `POST /v1/wake` now returns `503` under enqueue backpressure and retries can re-enqueue queued runs. Added runner/API tests for queue-full behavior and documented config/API behavior in README/config. (by @assistant)
