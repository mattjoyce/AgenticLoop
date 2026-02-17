package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mattjoyce/agenticloop/internal/store"
)

// WakeRequest is the JSON body for POST /v1/wake.
type WakeRequest struct {
	WakeID      *string         `json:"wake_id,omitempty"`
	Goal        string          `json:"goal"`
	Context     json.RawMessage `json:"context,omitempty"`
	Constraints json.RawMessage `json:"constraints,omitempty"`
}

// WakeResponse is returned on successful wake.
type WakeResponse struct {
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Existing bool   `json:"existing"`
}

// RunResponse is returned by GET /v1/runs/{run_id}.
type RunResponse struct {
	ID          string          `json:"id"`
	WakeID      *string         `json:"wake_id,omitempty"`
	Goal        string          `json:"goal"`
	Status      string          `json:"status"`
	Summary     *string         `json:"summary,omitempty"`
	Error       *string         `json:"error,omitempty"`
	Steps       []*store.Step   `json:"steps,omitempty"`
	Context     json.RawMessage `json:"context,omitempty"`
	Constraints json.RawMessage `json:"constraints,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// HealthzResponse is returned by GET /healthz.
type HealthzResponse struct {
	Status        string `json:"status"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// ErrorResponse is returned on errors.
type ErrorResponse struct {
	Error string `json:"error"`
}

// handleHealthz handles GET /healthz.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, HealthzResponse{
		Status:        "ok",
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
	})
}

// handleWake handles POST /v1/wake.
func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	var req WakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Goal == "" {
		s.writeError(w, http.StatusBadRequest, "goal is required")
		return
	}

	run, existing, err := s.creator.Create(r.Context(), req.Goal, req.WakeID, req.Context, req.Constraints)
	if err != nil {
		s.logger.Error("failed to create run", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to create run")
		return
	}

	if !existing {
		s.creator.Enqueue(run.ID)
	}

	s.logger.Info("wake request processed",
		"run_id", run.ID,
		"existing", existing,
		"goal", req.Goal,
	)

	status := http.StatusAccepted
	if existing {
		status = http.StatusOK
	}
	respondJSON(w, status, WakeResponse{
		RunID:    run.ID,
		Status:   string(run.Status),
		Existing: existing,
	})
}

// handleGetRun handles GET /v1/runs/{run_id}.
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")

	run, err := s.runs.GetByID(r.Context(), runID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "run not found")
		return
	}

	// Fetch steps
	stepStore := store.NewStepStore(s.runs.DB())
	steps, err := stepStore.GetByRunID(r.Context(), runID)
	if err != nil {
		s.logger.Error("failed to get steps", "run_id", runID, "error", err)
		steps = nil
	}

	respondJSON(w, http.StatusOK, RunResponse{
		ID:          run.ID,
		WakeID:      run.WakeID,
		Goal:        run.Goal,
		Status:      string(run.Status),
		Summary:     run.Summary,
		Error:       run.Error,
		Steps:       steps,
		Context:     run.Context,
		Constraints: run.Constraints,
		StartedAt:   run.StartedAt,
		CompletedAt: run.CompletedAt,
		CreatedAt:   run.CreatedAt,
	})
}

func respondJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, statusCode int, message string) {
	respondJSON(w, statusCode, ErrorResponse{Error: message})
}
