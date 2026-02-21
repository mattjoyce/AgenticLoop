---
id: 113
status: archive
priority: Normal
blocked_by: []
tags: [prompts, tools, design, agent]
---

# #113: Align Prompt Tool List with Runtime Tool Catalog

Remove drift between hardcoded prompt tool lists and actual bound tools so the model sees accurate capabilities each run.

## Acceptance Criteria

- Act-stage prompt references runtime-generated available tools.
- Hardcoded tool list in default config is removed or minimized.
- Behavior is correct whether Ductile tools are present or absent.
- Add tests or fixtures validating prompt rendering with dynamic tool sets.

## Narrative

- 2026-02-20: Created from repository review; runtime builds a tool catalog but default prompt content still hardcodes tool names, causing potential drift. (by @assistant)
- 2026-02-20: Updated ACT prompt template to inject `{{.AvailableTools}}` from runtime-bound tool metadata and removed static hardcoded tool entries. Added fixture test in `internal/config/loader_test.go` to validate dynamic tool-catalog rendering contract in `config.yaml`. (by @assistant)
