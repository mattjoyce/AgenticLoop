---
id: 115
status: archive
priority: Medium
blocked_by: []
tags: [agent, loop, state, workspace, prompts]
---

# #115: Structured State Persistence Across Loop Iterations

Persist FRAME's JSON state object to `state.json` in the workspace and thread it
through all stages so later loops can mutate the shared todo list and evidence
rather than reconstructing from unstructured run memory.

## Problem

FRAME now outputs structured JSON with `state.todo`, `state.evidence`, and
`state.notes`. However:

- The output is stored only as a string in `state.Frame` and passed to later
  stages as raw text via `{{.Frame}}`.
- REFLECT's `memory_update` is appended to `run_memory.md` as unstructured text.
- There is no durable, machine-readable todo list that survives across loops.
- PLAN outputs `updated_todo` but nobody reads or persists it.

This causes drift: the agent must re-parse frame JSON and re-infer todo state
each iteration instead of incrementally updating a shared object.

## Proposed Solution

### Phase 1 — Persist and expose (small)

1. After FRAME runs, write `state.Frame` to `state.json` in the run workspace.
2. Add `State string` to `stageState` in `loop.go`.
3. At loop start, read `state.json` into `state.State` (empty string if missing).
4. Expose as `{{.State}}` in all stage prompt templates.

This is purely additive. Stages can optionally read `{{.State}}` for structured
context without breaking anything.

### Phase 2 — Mutations via REFLECT (medium)

1. Add `updated_state` field to REFLECT's `output_contract`:
   ```json
   {
     "next_stage": "plan|act|done",
     "summary": "string",
     "next_focus": "string",
     "memory_update": "string",
     "updated_state": {
       "todo": [{"id":"T1","task":"string","done":true}],
       "evidence": ["path", ...],
       "notes": ["string", ...]
     }
   }
   ```
2. In the runtime, after parsing the reflect decision, merge `updated_state`
   into `state.json` (mark todo items done, append evidence/notes).
3. `state.json` becomes the durable todo tracker across loops.

## Why Not Memory Update String?

`memory_update` is appended as freeform text. It works for human-readable notes
but is brittle to parse. Structured JSON state enables the agent to reliably
tick off todo items and cite evidence paths without re-reading the entire memory.

## Acceptance Criteria

- [ ] `state.json` written to workspace after every FRAME stage.
- [ ] `{{.State}}` available in all stage templates.
- [ ] REFLECT can mark todo items as done via `updated_state`.
- [ ] Merged state survives across loop iterations.
- [ ] Existing runs without `state.json` degrade gracefully (empty `{{.State}}`).

## Narrative

- 2026-02-20: Created from prompt engineering session. FRAME/PLAN/ACT/REFLECT
  prompts upgraded to v2 with JSON output contracts. State persistence identified
  as the key missing runtime affordance to make structured todo tracking work.
- 2026-02-20: Renumbered from `#106` to `#115` to resolve duplicate kanban ID collision. (by @assistant)
- 2026-02-20: Implemented structured state persistence via workspace `state.json` with loop-level read/write plumbing, added `{{.State}}` to all stage templates, upgraded FRAME prompt contract to JSON state output, and added REFLECT `updated_state` merge support (todo merge by `id`, evidence/notes append-dedupe). Added unit tests for normalization/merge behavior and workspace state roundtrip. (by @assistant)
