package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/mattjoyce/agenticloop/internal/config"
	"github.com/mattjoyce/agenticloop/internal/ductile"
)

func TestRunActStageCanExecuteTwoDuctileTools(t *testing.T) {
	var alphaCalls int
	var betaCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/plugin/alpha/one":
			alphaCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"job_id":"job-alpha","status":"queued","plugin":"alpha","command":"one"}`)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/plugin/beta/two":
			betaCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"job_id":"job-beta","status":"queued","plugin":"beta","command":"two"}`)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/job/job-alpha":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"job_id":"job-alpha","status":"succeeded","plugin":"alpha","command":"one","result":{"ok":true}}`)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/job/job-beta":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"job_id":"job-beta","status":"succeeded","plugin":"beta","command":"two","result":{"ok":true}}`)
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer server.Close()

	client := ductile.NewClient(server.URL, "test-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	baseTools := ductile.BuildTools(client, []string{"alpha/one", "beta/two"}, nil)

	toolMap := make(map[string]tool.InvokableTool, 2)
	for _, bt := range baseTools {
		_, ok := bt.(tool.InvokableTool)
		if !ok {
			t.Fatalf("tool does not implement InvokableTool")
		}
	}
	toolMap["ductile_alpha_one"] = baseTools[0].(tool.InvokableTool)
	toolMap["ductile_beta_two"] = baseTools[1].(tool.InvokableTool)

	model := &scriptedToolCallingModel{
		responses: []*schema.Message{
			{
				Role: schema.Assistant,
				ResponseMeta: &schema.ResponseMeta{
					Usage: &schema.TokenUsage{
						PromptTokens:     120,
						CompletionTokens: 40,
						TotalTokens:      160,
					},
				},
				ToolCalls: []schema.ToolCall{
					{
						ID:   "tc-1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "ductile_alpha_one",
							Arguments: `{"input":"a"}`,
						},
					},
					{
						ID:   "tc-2",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "ductile_beta_two",
							Arguments: `{"input":"b"}`,
						},
					},
				},
			},
			{
				Role:    schema.Assistant,
				Content: "completed two tool calls",
				ResponseMeta: &schema.ResponseMeta{
					Usage: &schema.TokenUsage{
						PromptTokens:     80,
						CompletionTokens: 20,
						TotalTokens:      100,
					},
				},
			},
		},
	}

	loop := &Loop{
		cfg: config.AgentConfig{
			MaxActRounds:    3,
			MaxRetryPerStep: 1,
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	result, err := loop.runActStage(context.Background(), &preparedToolset{
		model:  model,
		byName: toolMap,
	}, "prompt")
	if err != nil {
		t.Fatalf("runActStage: %v", err)
	}
	if alphaCalls != 1 || betaCalls != 1 {
		t.Fatalf("expected two ductile tool calls (alpha=1,beta=1), got alpha=%d beta=%d", alphaCalls, betaCalls)
	}
	if !strings.Contains(result.Summary, "completed two tool calls") {
		t.Fatalf("unexpected act summary: %q", result.Summary)
	}
	if result.TokenUsage.TotalTokens != 260 {
		t.Fatalf("expected total token usage 260, got %d", result.TokenUsage.TotalTokens)
	}
	alphaUsage, ok := result.ToolTokenUsage["ductile_alpha_one"]
	if !ok {
		t.Fatalf("missing ductile_alpha_one token usage")
	}
	if alphaUsage.Calls != 1 || alphaUsage.TotalTokens != 80 {
		t.Fatalf("unexpected alpha token usage: %+v", alphaUsage)
	}
	betaUsage, ok := result.ToolTokenUsage["ductile_beta_two"]
	if !ok {
		t.Fatalf("missing ductile_beta_two token usage")
	}
	if betaUsage.Calls != 1 || betaUsage.TotalTokens != 80 {
		t.Fatalf("unexpected beta token usage: %+v", betaUsage)
	}
}

type scriptedToolCallingModel struct {
	responses []*schema.Message
	idx       int
}

func (m *scriptedToolCallingModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if m.idx >= len(m.responses) {
		return nil, errors.New("no scripted response remaining")
	}
	resp := m.responses[m.idx]
	m.idx++

	// Return a detached copy so downstream mutation doesn't affect the script.
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var cloned schema.Message
	if err := json.Unmarshal(b, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func (m *scriptedToolCallingModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented in scripted model")
}

func (m *scriptedToolCallingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
