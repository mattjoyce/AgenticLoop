package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/agenticloop/internal/storage"
)

func TestStepStoreUpdateStatusWithAttempt(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runStore := NewRunStore(db)
	run, _, err := runStore.Create(ctx, "goal", nil, nil, nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	stepStore := NewStepStore(db)
	step, err := stepStore.Append(ctx, run.ID, 1, StepPhaseFrame, nil, nil)
	if err != nil {
		t.Fatalf("append step: %v", err)
	}

	if err := stepStore.UpdateStatusWithAttempt(ctx, step.ID, StepStatusRunning, nil, nil, 1); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	errMsg := "boom"
	if err := stepStore.UpdateStatusWithAttempt(ctx, step.ID, StepStatusError, nil, &errMsg, 3); err != nil {
		t.Fatalf("mark error with attempt: %v", err)
	}

	steps, err := stepStore.GetByRunID(ctx, run.ID)
	if err != nil {
		t.Fatalf("get steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected one step, got %d", len(steps))
	}
	if steps[0].Status != StepStatusError {
		t.Fatalf("status = %s, want %s", steps[0].Status, StepStatusError)
	}
	if steps[0].Attempt != 3 {
		t.Fatalf("attempt = %d, want 3", steps[0].Attempt)
	}
	if steps[0].StartedAt == nil {
		t.Fatalf("expected started_at to be set")
	}
	if steps[0].CompletedAt == nil {
		t.Fatalf("expected completed_at to be set")
	}
}
