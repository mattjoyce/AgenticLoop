package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mattjoyce/agenticloop/internal/storage"
)

func TestRunStoreCreateWakeIDIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := NewRunStore(db)
	wakeID := "wake-123"

	first, existing, err := store.Create(ctx, "goal", &wakeID, nil, nil)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if existing {
		t.Fatalf("first create should not be existing")
	}

	second, existing, err := store.Create(ctx, "goal", &wakeID, nil, nil)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if !existing {
		t.Fatalf("second create should be existing")
	}
	if first.ID != second.ID {
		t.Fatalf("expected same run id for duplicate wake_id, got %s vs %s", first.ID, second.ID)
	}
}

func TestRunStoreCreateWakeIDConcurrent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := NewRunStore(db)
	wakeID := "wake-concurrent"

	const workers = 20
	type result struct {
		run      *Run
		existing bool
		err      error
	}

	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, existing, err := store.Create(ctx, "goal", &wakeID, nil, nil)
			results <- result{run: run, existing: existing, err: err}
		}()
	}
	wg.Wait()
	close(results)

	ids := map[string]struct{}{}
	var createdCount int
	var existingCount int
	for r := range results {
		if r.err != nil {
			t.Fatalf("concurrent create failed: %v", r.err)
		}
		ids[r.run.ID] = struct{}{}
		if r.existing {
			existingCount++
		} else {
			createdCount++
		}
	}

	if len(ids) != 1 {
		t.Fatalf("expected one canonical run id, got %d", len(ids))
	}
	if createdCount != 1 {
		t.Fatalf("expected exactly one created run, got %d", createdCount)
	}
	if existingCount != workers-1 {
		t.Fatalf("expected %d existing responses, got %d", workers-1, existingCount)
	}
}
