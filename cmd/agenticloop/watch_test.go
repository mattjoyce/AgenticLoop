package main

import (
	"encoding/json"
	"testing"
)

func TestParseStepOutputExtractsTokenUsage(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"content": "Tool workspace_write output:\n{\"status\":\"ok\",\"path\":\"notes.md\"}\n",
		"token_usage": map[string]any{
			"prompt_tokens":     120,
			"completion_tokens": 30,
			"total_tokens":      150,
		},
		"tool_token_usage": map[string]any{
			"workspace_write": map[string]any{
				"prompt_tokens":     60,
				"completion_tokens": 15,
				"total_tokens":      75,
				"calls":             1,
			},
		},
	})

	parsed := parseStepOutput(raw)
	if parsed.TokenUsage.TotalTokens != 150 {
		t.Fatalf("total_tokens = %d, want 150", parsed.TokenUsage.TotalTokens)
	}
	usage, ok := parsed.ToolTokenUsage["workspace_write"]
	if !ok {
		t.Fatalf("expected workspace_write tool usage")
	}
	if usage.Calls != 1 || usage.TotalTokens != 75 {
		t.Fatalf("unexpected workspace_write usage: %+v", usage)
	}
}

func TestWatchModelRecalculateTokenTotals(t *testing.T) {
	m := watchModel{
		stepMetrics: map[string]stepMetrics{
			"step-1": {
				Tokens: tokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
				ToolTokenUsage: map[string]toolTokenUsage{
					"workspace_write": {PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, Calls: 1},
				},
			},
			"step-2": {
				Tokens: tokenUsage{PromptTokens: 40, CompletionTokens: 20, TotalTokens: 60},
				ToolTokenUsage: map[string]toolTokenUsage{
					"workspace_write": {PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10, Calls: 1},
					"workspace_list":  {PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15, Calls: 1},
				},
			},
		},
	}

	m.recalculateTokenTotals()

	if m.tokenTotals.TotalTokens != 90 {
		t.Fatalf("job total tokens = %d, want 90", m.tokenTotals.TotalTokens)
	}
	write := m.toolTokenTotals["workspace_write"]
	if write.TotalTokens != 25 || write.Calls != 2 {
		t.Fatalf("workspace_write totals = %+v, want total=25 calls=2", write)
	}
	list := m.toolTokenTotals["workspace_list"]
	if list.TotalTokens != 15 || list.Calls != 1 {
		t.Fatalf("workspace_list totals = %+v, want total=15 calls=1", list)
	}
}
