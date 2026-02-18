package api

import (
	"testing"
	"time"

	"github.com/mattjoyce/agenticloop/internal/store"
)

func TestRunStreamSignatureChangesOnStatus(t *testing.T) {
	now := time.Now().UTC()
	summary := "ok"
	errText := "boom"
	run := &store.Run{
		ID:        "run-1",
		Status:    store.RunStatusQueued,
		Summary:   &summary,
		Error:     &errText,
		UpdatedAt: now,
	}

	sigQueued := runStreamSignature(run)
	run.Status = store.RunStatusRunning
	sigRunning := runStreamSignature(run)

	if sigQueued == sigRunning {
		t.Fatalf("expected different signatures when run status changes")
	}

	// Different pointer addresses with same values should keep the signature stable.
	summaryCopy := "ok"
	errCopy := "boom"
	run.Status = store.RunStatusQueued
	run.Summary = &summaryCopy
	run.Error = &errCopy
	sigQueuedAgain := runStreamSignature(run)
	if sigQueued != sigQueuedAgain {
		t.Fatalf("expected stable signature for identical run values")
	}
}

func TestStepStreamSignatureChangesOnOutput(t *testing.T) {
	step := &store.Step{
		ID:         "step-1",
		StepNum:    1,
		Phase:      store.StepPhaseAct,
		Status:     store.StepStatusRunning,
		ToolInput:  []byte(`{"path":"a.txt"}`),
		ToolOutput: []byte(`{"status":"ok"}`),
	}

	sigA := stepStreamSignature(step)
	step.ToolOutput = []byte(`{"status":"ok","bytes_written":10}`)
	sigB := stepStreamSignature(step)

	if sigA == sigB {
		t.Fatalf("expected different signatures when step output changes")
	}

	// Different pointer addresses with same values should keep the signature stable.
	tool := "workspace_write"
	errMsg := "none"
	step.Tool = &tool
	step.Error = &errMsg
	sigC := stepStreamSignature(step)

	toolCopy := "workspace_write"
	errCopy := "none"
	step.Tool = &toolCopy
	step.Error = &errCopy
	sigD := stepStreamSignature(step)
	if sigC != sigD {
		t.Fatalf("expected stable signature for identical step values")
	}
}
