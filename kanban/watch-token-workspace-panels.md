---
id: 116
status: done
priority: High
blocked_by: []
tags: [watch, tui, observability, workspace, tokens]
---

# #116: Watch TUI Token + Workspace Panels (Orange Theme Highlight)

Enhance `agenticloop watch` so operators can see token usage and workspace footprint while runs are active.

## Scope

- Show run-level token accumulator in watch.
- Show per-tool token usage (ACT-stage attribution) in watch.
- Add workspace panel with filenames, per-file size, and total size.
- Use orange as the primary highlight color in the watch theme.

## Acceptance Criteria

- Watch shows `job total` token usage (prompt/completion/total).
- Watch shows per-tool token usage with call counts.
- Workspace panel shows relative filename list with sizes.
- Workspace panel shows total workspace size.
- API provides an authenticated workspace inventory endpoint for watch.
- README documents the new watch panels and workspace endpoint.
- Tests cover token parsing/aggregation and workspace endpoint behavior.

## Narrative
- 2026-02-21: Created card to track operator-requested watch enhancements for token observability, workspace visibility, and orange-forward TUI styling. (by @assistant)
- 2026-02-21: Implemented token metadata emission from stage outputs and ACT per-tool attribution, added `/v1/runs/{run_id}/workspace`, upgraded watch UI with dedicated Token Usage and Workspace panels, switched watch theme highlights to orange, and added tests for watch token parsing/aggregation plus workspace endpoint behavior. (by @assistant)
