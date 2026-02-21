package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/agenticloop/internal/storage"
	"github.com/mattjoyce/agenticloop/internal/store"
)

func TestHandleRunWorkspaceListsFilesAndTotalSize(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runStore := store.NewRunStore(db)
	run, _, err := runStore.Create(ctx, "inspect workspace", nil, nil, nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	workspaceBase := t.TempDir()
	runDir := filepath.Join(workspaceBase, run.ID)
	if err := os.MkdirAll(filepath.Join(runDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir run workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "sub", "b.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write b.md: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(Config{
		Token:        "test-token",
		WorkspaceDir: workspaceBase,
	}, runStore, &testCreator{runStore: runStore}, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/workspace", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()
	srv.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("workspace status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp WorkspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.RunID != run.ID {
		t.Fatalf("run_id = %q, want %q", resp.RunID, run.ID)
	}
	if resp.FileCount != 2 {
		t.Fatalf("file_count = %d, want 2", resp.FileCount)
	}
	if resp.TotalSizeBytes != 8 {
		t.Fatalf("total_size_bytes = %d, want 8", resp.TotalSizeBytes)
	}
	if len(resp.Files) != 2 {
		t.Fatalf("files length = %d, want 2", len(resp.Files))
	}
	if resp.Files[0].Path != "a.txt" || resp.Files[0].SizeBytes != 3 {
		t.Fatalf("unexpected first file: %+v", resp.Files[0])
	}
	if resp.Files[1].Path != "sub/b.md" || resp.Files[1].SizeBytes != 5 {
		t.Fatalf("unexpected second file: %+v", resp.Files[1])
	}
}

func TestHandleRunWorkspaceReturnsEmptyForMissingRunDir(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runStore := store.NewRunStore(db)
	run, _, err := runStore.Create(ctx, "inspect workspace", nil, nil, nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(Config{
		Token:        "test-token",
		WorkspaceDir: t.TempDir(),
	}, runStore, &testCreator{runStore: runStore}, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/workspace", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()
	srv.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("workspace status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp WorkspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.FileCount != 0 || resp.TotalSizeBytes != 0 || len(resp.Files) != 0 {
		t.Fatalf("expected empty workspace response, got %+v", resp)
	}
}
