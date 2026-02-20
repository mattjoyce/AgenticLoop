package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/agenticloop/internal/config"
	"github.com/mattjoyce/agenticloop/internal/storage"
	"github.com/mattjoyce/agenticloop/internal/store"
)

func TestRunnerEnqueueQueueFull(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := NewRunner(nil, nil, nil, nil, config.AgentConfig{
		QueueCapacity:  1,
		EnqueueTimeout: 0,
	}, nil, "", logger)

	if err := runner.Enqueue("run-1"); err != nil {
		t.Fatalf("first enqueue should succeed: %v", err)
	}
	if err := runner.Enqueue("run-2"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestRunnerRecoverRunsIncludesQueuedAndRunning(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runStore := store.NewRunStore(db)
	stepStore := store.NewStepStore(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	queuedRun, created, err := runStore.Create(ctx, "queued goal", nil, nil, nil)
	if err != nil || created {
		t.Fatalf("create queued run: err=%v created=%v", err, created)
	}

	runningRun, created, err := runStore.Create(ctx, "running goal", nil, nil, nil)
	if err != nil || created {
		t.Fatalf("create running run: err=%v created=%v", err, created)
	}
	if err := runStore.UpdateStatus(ctx, runningRun.ID, store.RunStatusRunning, nil, nil); err != nil {
		t.Fatalf("mark running run running: %v", err)
	}

	runner := NewRunner(runStore, stepStore, nil, nil, config.AgentConfig{
		QueueCapacity:  10,
		EnqueueTimeout: 0,
	}, nil, "", logger)

	if err := runner.RecoverRuns(ctx); err != nil {
		t.Fatalf("recover runs: %v", err)
	}

	got := map[string]struct{}{}
	for i := 0; i < 2; i++ {
		select {
		case runID := <-runner.queue:
			got[runID] = struct{}{}
		default:
			t.Fatalf("expected 2 recovered run IDs, got %d", len(got))
		}
	}

	if _, ok := got[queuedRun.ID]; !ok {
		t.Fatalf("queued run was not recovered")
	}
	if _, ok := got[runningRun.ID]; !ok {
		t.Fatalf("running run was not recovered")
	}
}
