---
id: 107
status: archive
priority: High
blocked_by: []
tags: [security, workspace, localtools, filesystem]
---

# #107: Harden Workspace Path Sandboxing

Fix path containment checks for workspace tools to prevent prefix-collision escapes and symlink traversal outside the run workspace.

## Acceptance Criteria

- `sanitizePath` enforces true directory boundary checks (not string-prefix-only).
- File operations reject traversal through symlinks that point outside workspace.
- Tests cover prefix collision cases (for example `workspace` vs `workspace_evil`).
- Tests cover symlink escape attempts for read/write/edit/delete/list/mkdir paths.

## Narrative

- 2026-02-20: Created from repository review; current path containment logic is vulnerable to escape via prefix collisions and likely symlink traversal. (by @assistant)
- 2026-02-20: Replaced string-prefix containment checks with boundary-safe `filepath.Rel` validation and symlink-component enforcement. Added tests for prefix-collision escapes and symlink escapes across read/write/edit/delete/list/mkdir tool paths; validated with `go test ./internal/localtools`. (by @assistant)
