package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

	// Always try to enqueue queued runs. This allows retries to re-enqueue a run
	// if an earlier wake created it but enqueueing failed due backpressure.
	if run.Status == store.RunStatusQueued {
		if err := s.creator.Enqueue(run.ID); err != nil {
			s.logger.Warn("failed to enqueue run", "run_id", run.ID, "existing", existing, "error", err)
			s.writeError(w, http.StatusServiceUnavailable, "runner queue is full; retry later")
			return
		}
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

// handleListRuns handles GET /v1/runs?status=<status>.
// status defaults to "running" if not supplied.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	statusParam := r.URL.Query().Get("status")
	if statusParam == "" {
		statusParam = "running"
	}
	runs, err := s.runs.ListByStatus(r.Context(), store.RunStatus(statusParam))
	if err != nil {
		s.logger.Error("failed to list runs", "status", statusParam, "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}
	type runSummary struct {
		ID        string    `json:"id"`
		Goal      string    `json:"goal"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := make([]runSummary, len(runs))
	for i, run := range runs {
		out[i] = runSummary{
			ID:        run.ID,
			Goal:      run.Goal,
			Status:    string(run.Status),
			CreatedAt: run.CreatedAt,
		}
	}
	respondJSON(w, http.StatusOK, out)
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

// handleRunEvents handles GET /v1/runs/{run_id}/events using Server-Sent Events.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")

	run, err := s.runs.GetByID(r.Context(), runID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "run not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	stepStore := store.NewStepStore(s.runs.DB())
	steps, err := stepStore.GetByRunID(r.Context(), runID)
	if err != nil {
		s.logger.Error("failed to get steps for stream snapshot", "run_id", runID, "error", err)
		steps = nil
	}

	if err := writeSSEEvent(w, flusher, "snapshot", map[string]any{
		"type":      "snapshot",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"run_id":    runID,
		"run":       run,
		"steps":     steps,
	}); err != nil {
		return
	}

	runSig := runStreamSignature(run)
	stepSigs := make(map[string]string, len(steps))
	for _, step := range steps {
		stepSigs[step.ID] = stepStreamSignature(step)
	}
	if run.Status == store.RunStatusDone || run.Status == store.RunStatusFailed {
		_ = writeSSEEvent(w, flusher, "stream.closed", map[string]any{
			"type":      "stream.closed",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"run_id":    runID,
			"status":    run.Status,
		})
		return
	}

	pollInterval := s.config.StreamPollInterval
	if pollInterval <= 0 {
		pollInterval = 700 * time.Millisecond
	}
	heartbeatInterval := s.config.StreamHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15 * time.Second
	}

	pollTicker := time.NewTicker(pollInterval)
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer pollTicker.Stop()
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeatTicker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-pollTicker.C:
			currentRun, err := s.runs.GetByID(r.Context(), runID)
			if err != nil {
				_ = writeSSEEvent(w, flusher, "error", map[string]any{
					"type":      "error",
					"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
					"run_id":    runID,
					"error":     "run not found",
				})
				return
			}

			if currentSig := runStreamSignature(currentRun); currentSig != runSig {
				runSig = currentSig
				if err := writeSSEEvent(w, flusher, "run.updated", map[string]any{
					"type":      "run.updated",
					"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
					"run_id":    runID,
					"run":       currentRun,
				}); err != nil {
					return
				}
			}

			currentSteps, err := stepStore.GetByRunID(r.Context(), runID)
			if err != nil {
				s.logger.Error("failed to get steps for stream update", "run_id", runID, "error", err)
				continue
			}
			for _, step := range currentSteps {
				sig := stepStreamSignature(step)
				prev, ok := stepSigs[step.ID]
				if !ok {
					stepSigs[step.ID] = sig
					if err := writeSSEEvent(w, flusher, "step.created", map[string]any{
						"type":      "step.created",
						"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
						"run_id":    runID,
						"step":      step,
					}); err != nil {
						return
					}
					continue
				}
				if prev != sig {
					stepSigs[step.ID] = sig
					if err := writeSSEEvent(w, flusher, "step.updated", map[string]any{
						"type":      "step.updated",
						"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
						"run_id":    runID,
						"step":      step,
					}); err != nil {
						return
					}
				}
			}

			if currentRun.Status == store.RunStatusDone || currentRun.Status == store.RunStatusFailed {
				_ = writeSSEEvent(w, flusher, "stream.closed", map[string]any{
					"type":      "stream.closed",
					"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
					"run_id":    runID,
					"status":    currentRun.Status,
				})
				return
			}
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func runStreamSignature(run *store.Run) string {
	if run == nil {
		return ""
	}
	raw := []byte(fmt.Sprintf(
		"%s|%s|%s|%s|%s",
		run.ID,
		run.Status,
		derefString(run.Summary),
		derefString(run.Error),
		run.UpdatedAt.UTC().Format(time.RFC3339Nano),
	))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func stepStreamSignature(step *store.Step) string {
	if step == nil {
		return ""
	}
	raw := []byte(fmt.Sprintf(
		"%s|%d|%s|%s|%s|%s|%s|%s|%s|%s",
		step.ID,
		step.StepNum,
		step.Phase,
		step.Status,
		derefString(step.Tool),
		string(step.ToolInput),
		string(step.ToolOutput),
		derefString(step.Error),
		derefTime(step.StartedAt),
		derefTime(step.CompletedAt),
	))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func respondJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, statusCode int, message string) {
	respondJSON(w, statusCode, ErrorResponse{Error: message})
}
