package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mattjoyce/agenticloop/internal/storage"
	"github.com/mattjoyce/agenticloop/internal/store"
)

type testCreator struct {
	runStore   *store.RunStore
	enqueueErr error

	mu       sync.Mutex
	enqueued []string
}

func (t *testCreator) Create(ctx context.Context, goal string, wakeID *string, runCtx json.RawMessage, constraints json.RawMessage) (*store.Run, bool, error) {
	return t.runStore.Create(ctx, goal, wakeID, runCtx, constraints)
}

func (t *testCreator) GetByID(ctx context.Context, id string) (*store.Run, error) {
	return t.runStore.GetByID(ctx, id)
}

func (t *testCreator) Enqueue(runID string) error {
	if t.enqueueErr != nil {
		return t.enqueueErr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.enqueued = append(t.enqueued, runID)
	return nil
}

func (t *testCreator) enqueueCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.enqueued)
}

func TestHandleWakeIdempotentWakeID(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runStore := store.NewRunStore(db)
	creator := &testCreator{runStore: runStore}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(Config{Token: "test-token"}, runStore, creator, logger)
	router := srv.setupRoutes()

	payload := map[string]any{
		"wake_id": "wake-dup",
		"goal":    "do thing",
	}
	body, _ := json.Marshal(payload)

	doWake := func() (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/v1/wake", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		var resp map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode wake response: %v", err)
		}
		return rr, resp
	}

	firstRR, firstResp := doWake()
	secondRR, secondResp := doWake()

	if firstRR.Code != http.StatusAccepted {
		t.Fatalf("first wake status = %d, want %d", firstRR.Code, http.StatusAccepted)
	}
	if secondRR.Code != http.StatusOK {
		t.Fatalf("second wake status = %d, want %d", secondRR.Code, http.StatusOK)
	}
	if firstResp["run_id"] != secondResp["run_id"] {
		t.Fatalf("expected same run_id, got %v vs %v", firstResp["run_id"], secondResp["run_id"])
	}
	if firstResp["existing"] != false {
		t.Fatalf("expected first wake existing=false, got %v", firstResp["existing"])
	}
	if secondResp["existing"] != true {
		t.Fatalf("expected second wake existing=true, got %v", secondResp["existing"])
	}
	if creator.enqueueCount() != 2 {
		t.Fatalf("expected queued run to be enqueued on both wake attempts, got %d", creator.enqueueCount())
	}
}

func TestHandleWakeQueueBackpressureReturns503(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agenticloop.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runStore := store.NewRunStore(db)
	creator := &testCreator{
		runStore:   runStore,
		enqueueErr: errors.New("queue full"),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(Config{Token: "test-token"}, runStore, creator, logger)

	body := []byte(`{"wake_id":"wake-full","goal":"do thing"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/wake", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("wake status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("expected non-empty error message")
	}
}
