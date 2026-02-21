package agent

import (
	"encoding/json"
	"testing"
)

func TestNormalizeStateJSONFallback(t *testing.T) {
	raw := "plain text frame output"
	got := normalizeStateJSON(raw)

	var state map[string]any
	if err := json.Unmarshal(got, &state); err != nil {
		t.Fatalf("decode normalized state: %v", err)
	}
	notes, ok := state["notes"].([]any)
	if !ok || len(notes) != 1 || notes[0] != raw {
		t.Fatalf("expected fallback notes to contain raw text, got %#v", state["notes"])
	}
}

func TestMergeStateJSONUpdatedState(t *testing.T) {
	existing := json.RawMessage(`{
		"todo":[{"id":"T1","task":"first","done":false}],
		"evidence":["e1"],
		"notes":["n1"]
	}`)
	updated := json.RawMessage(`{
		"todo":[{"id":"T1","done":true},{"id":"T2","task":"second","done":false}],
		"evidence":["e2","e1"],
		"notes":["n2"]
	}`)

	merged, err := mergeStateJSON(existing, updated)
	if err != nil {
		t.Fatalf("merge state: %v", err)
	}

	var state map[string]any
	if err := json.Unmarshal(merged, &state); err != nil {
		t.Fatalf("decode merged state: %v", err)
	}

	todo, ok := state["todo"].([]any)
	if !ok || len(todo) != 2 {
		t.Fatalf("expected two todo items after merge, got %#v", state["todo"])
	}

	var t1 map[string]any
	for _, item := range todo {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if m["id"] == "T1" {
			t1 = m
			break
		}
	}
	if t1 == nil {
		t.Fatalf("todo item T1 missing after merge")
	}
	done, _ := t1["done"].(bool)
	if !done {
		t.Fatalf("expected T1.done=true after merge")
	}

	evidence, ok := state["evidence"].([]any)
	if !ok || len(evidence) != 2 {
		t.Fatalf("expected deduplicated evidence entries, got %#v", state["evidence"])
	}
	if evidence[0] != "e1" || evidence[1] != "e2" {
		t.Fatalf("unexpected evidence order/content: %#v", evidence)
	}
}
