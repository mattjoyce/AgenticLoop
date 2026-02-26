---
id: 117
status: todo
priority: Normal
blocked_by: []
tags: [docs, config, onboarding]
---

# Add AgenticLoop example config file

## Job Story
When I need to onboard AgenticLoop quickly, I want a `config.example.yaml` in the repo, so I can copy it to my local config directory without editing the tracked `config.yaml`.

## Context
We rely on `~/.config/agenticloop` (or other local config paths) for real deployments. Keeping a clearly marked example file prevents accidental edits to repo `config.yaml` and clarifies the expected fields for operators.

## Acceptance Criteria
- [ ] Add `config.example.yaml` with documented placeholders (API token, Ductile token, OpenAI key).
- [ ] Keep repo `config.yaml` as a minimal example or remove sensitive defaults.
- [ ] Document how to copy the example to local config in README or operator docs.

## Narrative
- 2026-02-27: Identified the need to avoid editing tracked `config.yaml` for local deployments; decided to add a dedicated example config file for AgenticLoop onboarding. (by @assistant)
