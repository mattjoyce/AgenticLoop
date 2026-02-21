package agent

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceStateReadWrite(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir(), "run-1")
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if got := ws.ReadState(); got != "" {
		t.Fatalf("expected empty initial state, got %q", got)
	}

	state := json.RawMessage(`{"todo":[{"id":"T1","task":"x","done":false}]}`)
	if err := ws.WriteState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if got := ws.ReadState(); got != string(state) {
		t.Fatalf("state roundtrip mismatch: got %q want %q", got, string(state))
	}
}
