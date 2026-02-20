package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/agenticloop/internal/storage"
	"github.com/mattjoyce/agenticloop/internal/store"
)

func TestFailRunReportsStatusPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	runStore := store.NewRunStore(db)
	loop := &Loop{
		runStore: runStore,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	_ = db.Close()

	origErr := errors.New("boom")
	gotErr := loop.failRun(ctx, "", "run-1", origErr)
	if gotErr == nil {
		t.Fatalf("expected failRun to return an error")
	}
	if !strings.Contains(gotErr.Error(), origErr.Error()) {
		t.Fatalf("expected original error in return value, got %q", gotErr.Error())
	}
	if !strings.Contains(gotErr.Error(), "failed to persist run status") {
		t.Fatalf("expected persistence failure detail, got %q", gotErr.Error())
	}
}
