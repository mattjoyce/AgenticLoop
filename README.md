# AgenticLoop

An autonomous agent orchestration service that runs a staged, iterative reasoning loop for AI agents. Designed as an external sibling service to [Ductile](https://github.com/mattjoyce/ductile), allowing long-running agent tasks to execute asynchronously without blocking a tool gateway scheduler.

## Overview

AgenticLoop accepts a goal via HTTP, then runs the agent through repeated **Frame → Plan → Act → Reflect** cycles until the task is complete or limits are reached. Each run is isolated in its own workspace with persistent memory across iterations.

```
POST /v1/wake  →  Runner (serial queue)  →  Loop (Frame/Plan/Act/Reflect)
                                               ↓
                                          Workspace (memory, tool logs)
                                               ↓
                                          SQLite (runs, steps)
                                               ↓
                                     Optional completion callback
```

## Features

- **Staged loop**: Structured Frame → Plan → Act → Reflect cycle, not free-form tool use
- **Memory layers**: `run_memory` (cross-iteration) and `loop_memory` (per-iteration transcript)
- **Loop memory archiving**: Optionally archive each iteration's `loop_memory.md` for per-iteration audit and debugging
- **Tool integration**: Built-in workspace file tools + Ductile plugin tools via allowlist
- **Dynamic tool catalog in prompts**: ACT prompt injects runtime-bound tools instead of static hardcoded tool names
- **Typed Ductile tools**: Fetches plugin schemas from the Ductile discovery API so the LLM receives correct parameter names and types, not a generic payload object
- **Pluggable LLM**: Anthropic Claude, OpenAI, or Ollama via the [Eino](https://github.com/cloudwego/eino) framework
- **Completion gate**: Agent must call `report_success` before it can mark itself done
- **Idempotency**: Optional `wake_id` prevents duplicate runs
- **Graceful recovery**: `queued` and `running` runs are re-queued on restart

## Requirements

- Go 1.21+
- A running [Ductile](https://github.com/mattjoyce/ductile) instance (optional, for Ductile tool access)
- An LLM API key (OpenAI, Anthropic, or a local Ollama instance)

## Configuration

Copy and edit `config.yaml`:

```yaml
service:
  name: agenticloop
  log_level: info

database:
  path: ./data/agenticloop.db

api:
  listen: "127.0.0.1:8090"
  token: "${AGENTICLOOP_API_TOKEN}"
  stream_poll_interval: 700ms
  stream_heartbeat_interval: 15s

ductile:
  base_url: "http://127.0.0.1:8080"
  token: "${DUCTILE_TOOL_TOKEN}"
  allowlist:
    - echo/poll
    - jina-reader/handle

llm:
  provider: openai          # openai | anthropic | ollama
  model: gpt-4o-mini
  api_key: "${OPENAI_API_KEY}"
  max_tokens: 4096          # used by anthropic provider

agent:
  default_max_loops: 10
  default_deadline: 5m
  step_timeout: 120s
  max_retry_per_step: 3
  max_act_rounds: 6
  queue_capacity: 100
  enqueue_timeout: 2s
  workspace_dir: ./data/workspaces
  save_loop_memory: false   # set true to archive loop_memory_iter_{N}.md each iteration
```

Set the required environment variables:

```bash
export OPENAI_API_KEY=...
export AGENTICLOOP_API_TOKEN=...
export DUCTILE_TOOL_TOKEN=...        # only needed if using Ductile tools
```

## Running

```bash
go build ./cmd/agenticloop
./agenticloop start --config config.yaml

# Watch a live run stream (orange-highlight TUI)
./agenticloop watch --api http://127.0.0.1:8090 --token "$AGENTICLOOP_API_TOKEN" --poll-interval 2s <run_id>
```

`watch` now includes:
- Event stream panel
- Token usage panel (`job total` + per-tool ACT usage accumulator)
- Workspace panel (file list, per-file size, total workspace size)

## API

All endpoints except `/healthz` require a Bearer token (`Authorization: Bearer <token>`).

### POST /v1/wake

Start or resume a run. Returns immediately with `202 Accepted`.

```json
{
  "wake_id": "optional-idempotency-key",
  "goal": "Summarise the linked article and save it to notes.md",
  "context": { "url": "https://example.com/article" },
  "constraints": {
    "max_loops": 5,
    "deadline": "3m"
  }
}
```

Response:

```json
{ "run_id": "abc123", "status": "queued", "existing": false }
```

If the internal runner queue is saturated, wake returns `503 Service Unavailable`
with `{ "error": "runner queue is full; retry later" }`.

### GET /v1/runs/{run_id}

Fetch the full run status and step history.

### GET /v1/runs/{run_id}/workspace

Fetch the run workspace inventory (relative file paths + sizes + total size).

### GET /v1/runs/{run_id}/events

Server-Sent Events stream for live run updates. Emits:

- `snapshot` (initial run + steps)
- `run.updated`
- `step.created`
- `step.updated`
- `stream.closed` (on terminal state)

### GET /healthz

Public health check. Returns `{ "status": "ok", "uptime_seconds": N }`.

## Agent Loop Stages

| Stage | Purpose |
|-------|---------|
| **Frame** | Analyse the goal, context, and constraints; produce a structured framing |
| **Plan** | Create a concrete action plan for this iteration |
| **Act** | Execute tools (workspace file ops, Ductile plugins, system info); multi-round until the LLM stops calling tools |
| **Reflect** | Assess progress; decide whether to continue or complete; update run memory |

The reflect stage returns a JSON decision:

```json
{
  "next_stage": "plan",
  "done": false,
  "summary": "...",
  "next_focus": "...",
  "memory_update": "...",
  "updated_state": {
    "todo": [{"id":"T1","task":"...","done":true}],
    "evidence": ["..."],
    "notes": ["..."]
  }
}
```

The agent cannot mark itself done without first calling `report_success`.

## Workspace Tools

Each run has a sandboxed workspace directory. The agent has access to:

- `workspace_read` / `workspace_write` / `workspace_append`
- `workspace_edit` (preview by default; apply with `expected_original_sha256`)
- `workspace_delete` / `workspace_mkdir` / `workspace_list`

Path traversal outside the workspace is blocked.

### Loop Memory Archiving

When `save_loop_memory: true` is set, `loop_memory.md` (the per-iteration tool call transcript) is copied to `loop_memory_iter_{N}.md` before being cleared at the end of each Reflect stage. This gives a full audit trail of what the LLM saw and did on every iteration, useful for debugging agent behaviour.

Structured loop state is persisted at `state.json` in each run workspace. The FRAME stage refreshes it, and REFLECT can apply incremental updates through `updated_state`.

## Ductile Tool Integration

Tools from the Ductile gateway are registered from the `allowlist` in config. At runtime, `DuctileTool.Info()` calls `GET /plugin/{name}` on the Ductile discovery API to fetch the command's JSON Schema. This is converted to typed Eino parameters so the LLM receives correct field names, types, and required flags rather than a generic `payload: object`.

If the discovery endpoint is unavailable or returns no schema, it falls back to the old generic payload schema transparently.

## Run States

`queued` → `running` → `done` | `failed`

## Architecture Notes

AgenticLoop is intentionally separate from Ductile. Ductile handles short-lived, stateless jobs. AgenticLoop handles stateful, multi-iteration cognition. Wake requests return immediately; the run executes asynchronously in a serial queue.
