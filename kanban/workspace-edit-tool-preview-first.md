---
id: 106
status: done
priority: High
blocked_by: []
tags: [localtools, workspace, editing, safety]
---

# #106: Add `workspace_edit` Tool (Preview-First, Safe Apply)

When an autonomous run needs to modify an existing file, I want a native edit tool that supports precise updates, so I can avoid full-file rewrites and reduce accidental changes.

## Scope

Add a workspace-scoped edit tool with two modes:
- Regex replace with exactly one required match.
- Line range replace with explicit start and end lines.

Default behavior should preview only. Applying changes must be explicit.

## Acceptance Criteria

- A new workspace tool `workspace_edit` is available.
- `apply` defaults to `false` and returns a deterministic preview payload.
- Regex mode requires exactly one match; `0` or `>1` matches return errors.
- Line mode supports 1-based inclusive `start_line`/`end_line` validation.
- Apply mode writes atomically (temp file + rename in same directory).
- No-op edits return a clear `no_change` result.
- Unit tests cover success, invalid inputs, no-change, and safety cases.
- Tool list/prompt docs mention `workspace_edit`.

## Narrative
- 2026-02-18: Scoped this card to a preview-first editing contract to prioritize safety and auditability over single-call speed. (by @assistant)
- 2026-02-18: Implemented `workspace_edit` with `regex_replace` and `line_replace` modes, enforced single-match regex, required preview hash confirmation for apply, added atomic write path, updated prompt/docs, and validated with `go test ./...`. (by @assistant)
