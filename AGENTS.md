# Repository Guidelines

## Project Structure & Module Organization
- `cmd/agenticloop/main.go`: CLI entrypoint (`start`, `version`).
- `internal/agent`: loop orchestration, runner, workspace lifecycle.
- `internal/api`: HTTP server, auth middleware, and handlers (`/v1/wake`, `/v1/runs/{id}`, `/healthz`).
- `internal/config`: YAML config types and loader.
- `internal/storage` and `internal/store`: SQLite connection and run/step persistence.
- `internal/ductile`, `internal/localtools`, `internal/provider`: tool adapters and LLM provider wiring.
- `config.yaml`: local runtime defaults; `kanban/`: project tracking notes.

## Build, Test, and Development Commands
- `go build ./cmd/agenticloop`: build the service binary.
- `go run ./cmd/agenticloop start --config config.yaml`: run locally with config.
- `go test ./...`: run all tests.
- `go test ./internal/localtools -run TestWorkspace`: run focused workspace-tool tests.
- `go fmt ./... && go vet ./...`: format and catch common issues before opening a PR.

## Coding Style & Naming Conventions
- Follow standard Go formatting (`gofmt`) and import ordering.
- Use tabs (Go default) and idiomatic package boundaries under `internal/`.
- Exported identifiers: `CamelCase`; unexported: `camelCase`; file names: lowercase (use `_` only when needed, e.g., `workspace_tools.go`).
- Prefer table-driven tests for input/output variants and explicit error checks.

## Testing Guidelines
- Framework: Go `testing` package.
- Place tests in `*_test.go` beside implementation files.
- Test names should be descriptive (`TestSanitizePath`, `TestWorkspaceReadTruncation`).
- Cover boundary and safety cases (path traversal, truncation, invalid args), not only happy paths.

## Commit & Pull Request Guidelines
- Match existing commit style: `type: short summary` (examples in history: `feat: ...`, `fix: ...`, `docs: ...`).
- Keep commits focused and atomic; include code + tests together when practical.
- PRs should include:
  - What changed and why.
  - Any config/API impact (example request/response if endpoints change).
  - Commands run locally (at minimum `go test ./...`).
