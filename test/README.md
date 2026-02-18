# AgenticLoop Local Dev Test Configuration

This directory contains the configuration used for local integration testing
of AgenticLoop with ductile. It documents a working end-to-end setup.

## What's Here

```
test/
├── config.yaml    # Full AgenticLoop service config (LLM, API, ductile, agent prompts)
└── README.md
```

## Setup

### Prerequisites

- AgenticLoop binary (built from this repo: `go build -o agenticloop ./cmd/agenticloop/`)
- ductile running on `localhost:8080` with the `agenticloop` and `youtube_transcript` plugins loaded
  (see `test/local-dev` branch of github.com/mattjoyce/ductile)
- OpenAI API key

### Environment Variables

```bash
export OPENAI_API_KEY=<your-openai-api-key>
export AGENTICLOOP_API_TOKEN=<your-agenticloop-api-token>   # e.g. test_agenticloop_token_local
export DUCTILE_TOOL_TOKEN=<your-ductile-token>              # e.g. test_admin_token_local
```

The config uses `${VAR}` interpolation for all secrets — nothing is hardcoded.

### Directory Layout

Run from the directory that will contain `data/`:

```
your-run-dir/
└── data/
    ├── agenticloop.db       # created at runtime
    └── workspaces/          # one subdir per agent run
        └── <run-id>/
            ├── run_memory.md
            ├── loop_memory.md
            ├── loop_memory_iter_1.md   # with save_loop_memory: true
            └── summary.md              # agent output
```

### Starting

```bash
agenticloop start --config ./test/config.yaml
```

Check health:

```bash
curl http://127.0.0.1:8090/healthz
```

## Configuration Notes

### LLM

Uses `gpt-4o-mini` via OpenAI. Swap `provider` and `model` for Anthropic or Ollama.

### Ductile Tool Integration

AgenticLoop calls `GET /plugin/{name}` on the ductile gateway at tool registration time
to fetch typed `input_schema` for each allowlisted plugin/command. The LLM receives proper
parameter names and types rather than a generic payload object.

Allowlisted tools (from `ductile.allowlist`):
- `echo/poll`, `echo/handle` — basic test tools
- `fabric/handle` — fabric pattern execution
- `file_handler/handle` — file read/write
- `youtube_transcript/handle` — YouTube transcript fetching

### save_loop_memory

With `agent.save_loop_memory: true`, each completed loop iteration archives its
`loop_memory.md` to `loop_memory_iter_{N}.md` in the workspace. Useful for debugging
exactly what the LLM called and what it received back.

### Stage Prompts

Full Frame → Plan → Act → Reflect prompts are included in `config.yaml`.
These use Go template syntax (`{{.Goal}}`, `{{.Memory}}`, etc.) and XML-bounded
stage context blocks for clear LLM instruction.

## E2E Test

With ductile running (see ductile repo `test/local-dev`):

```bash
curl -X POST http://localhost:8080/plugin/agenticloop/handle \
  -H "Authorization: Bearer test_admin_token_local" \
  -H "Content-Type: application/json" \
  -d '{
    "payload": {
      "goal": "Fetch the transcript for https://www.youtube.com/watch?v=RtMLnCMv3do and write a summary to summary.md",
      "wake_id": "yt-summary-test-001"
    }
  }'
```

The agent will:
1. Frame / Plan the task
2. Call `ductile_youtube_transcript_handle` with `{"url": "https://..."}`
3. Receive the transcript
4. Write `summary.md` to its workspace
5. Call `report_success`

Check the run status:

```bash
curl http://127.0.0.1:8090/v1/runs/<run-id> \
  -H "Authorization: Bearer test_agenticloop_token_local"
```
