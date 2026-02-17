package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RunStatus represents the lifecycle state of a run.
type RunStatus string

const (
	RunStatusQueued  RunStatus = "queued"
	RunStatusRunning RunStatus = "running"
	RunStatusDone    RunStatus = "done"
	RunStatusFailed  RunStatus = "failed"
)

// Run represents an agent run.
type Run struct {
	ID          string          `json:"id"`
	WakeID      *string         `json:"wake_id,omitempty"`
	Goal        string          `json:"goal"`
	Context     json.RawMessage `json:"context,omitempty"`
	Constraints json.RawMessage `json:"constraints,omitempty"`
	Status      RunStatus       `json:"status"`
	Summary     *string         `json:"summary,omitempty"`
	Error       *string         `json:"error,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at"`
	CreatedAt   time.Time       `json:"created_at"`
}

// RunStore provides CRUD operations on the runs table.
type RunStore struct {
	db *sql.DB
}

// NewRunStore creates a new RunStore.
func NewRunStore(db *sql.DB) *RunStore {
	return &RunStore{db: db}
}

// DB returns the underlying database connection.
func (s *RunStore) DB() *sql.DB {
	return s.db
}

// Create inserts a new run. If wakeID is non-nil and already exists, returns the existing run.
func (s *RunStore) Create(ctx context.Context, goal string, wakeID *string, runCtx json.RawMessage, constraints json.RawMessage) (*Run, bool, error) {
	// Check for existing wake_id (idempotency)
	if wakeID != nil {
		existing, err := s.GetByWakeID(ctx, *wakeID)
		if err == nil && existing != nil {
			return existing, true, nil
		}
	}

	now := time.Now().UTC()
	run := &Run{
		ID:          uuid.New().String(),
		WakeID:      wakeID,
		Goal:        goal,
		Context:     runCtx,
		Constraints: constraints,
		Status:      RunStatusQueued,
		UpdatedAt:   now,
		CreatedAt:   now,
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (id, wake_id, goal, context, constraints, status, updated_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.WakeID, run.Goal, run.Context, run.Constraints,
		string(run.Status), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, false, fmt.Errorf("insert run: %w", err)
	}

	return run, false, nil
}

// GetByID retrieves a run by its ID.
func (s *RunStore) GetByID(ctx context.Context, id string) (*Run, error) {
	return s.scanOne(ctx, `SELECT id, wake_id, goal, context, constraints, status, summary, error, started_at, completed_at, updated_at, created_at FROM runs WHERE id = ?`, id)
}

// GetByWakeID retrieves a run by its wake_id.
func (s *RunStore) GetByWakeID(ctx context.Context, wakeID string) (*Run, error) {
	return s.scanOne(ctx, `SELECT id, wake_id, goal, context, constraints, status, summary, error, started_at, completed_at, updated_at, created_at FROM runs WHERE wake_id = ?`, wakeID)
}

// ListByStatus retrieves all runs with the given status.
func (s *RunStore) ListByStatus(ctx context.Context, status RunStatus) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, wake_id, goal, context, constraints, status, summary, error, started_at, completed_at, updated_at, created_at FROM runs WHERE status = ? ORDER BY created_at ASC`, string(status))
	if err != nil {
		return nil, fmt.Errorf("list runs by status: %w", err)
	}
	defer rows.Close()

	var runs []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// UpdateStatus updates a run's status and optional fields.
func (s *RunStore) UpdateStatus(ctx context.Context, id string, status RunStatus, summary *string, errMsg *string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var completedAt *string
	var startedAt *string
	if status == RunStatusRunning {
		startedAt = &now
	}
	if status == RunStatusDone || status == RunStatusFailed {
		completedAt = &now
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, summary = COALESCE(?, summary), error = COALESCE(?, error),
		 started_at = COALESCE(?, started_at), completed_at = COALESCE(?, completed_at), updated_at = ?
		 WHERE id = ?`,
		string(status), summary, errMsg, startedAt, completedAt, now, id,
	)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

// NextQueued returns the oldest queued run, or nil if none.
func (s *RunStore) NextQueued(ctx context.Context) (*Run, error) {
	run, err := s.scanOne(ctx,
		`SELECT id, wake_id, goal, context, constraints, status, summary, error, started_at, completed_at, updated_at, created_at
		 FROM runs WHERE status = ? ORDER BY created_at ASC LIMIT 1`, string(RunStatusQueued))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return run, err
}

func (s *RunStore) scanOne(ctx context.Context, query string, args ...any) (*Run, error) {
	row := s.db.QueryRowContext(ctx, query, args...)
	r, err := scanRunRow(row)
	if err == sql.ErrNoRows {
		return nil, err
	}
	return r, err
}

// scanner is an interface satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanRun(s scanner) (*Run, error) {
	var r Run
	var status string
	var wakeID sql.NullString
	var contextJSON sql.NullString
	var constraintsJSON sql.NullString
	var summary sql.NullString
	var errMsg sql.NullString
	var startedAt, completedAt, updatedAt, createdAt *string

	err := s.Scan(&r.ID, &wakeID, &r.Goal, &contextJSON, &constraintsJSON,
		&status, &summary, &errMsg, &startedAt, &completedAt, &updatedAt, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}

	if wakeID.Valid {
		v := wakeID.String
		r.WakeID = &v
	}
	if contextJSON.Valid && contextJSON.String != "" {
		r.Context = json.RawMessage(contextJSON.String)
	}
	if constraintsJSON.Valid && constraintsJSON.String != "" {
		r.Constraints = json.RawMessage(constraintsJSON.String)
	}
	if summary.Valid {
		v := summary.String
		r.Summary = &v
	}
	if errMsg.Valid {
		v := errMsg.String
		r.Error = &v
	}

	r.Status = RunStatus(status)
	r.StartedAt = parseTime(startedAt)
	r.CompletedAt = parseTime(completedAt)
	if updatedAt != nil {
		if t, err := time.Parse(time.RFC3339Nano, *updatedAt); err == nil {
			r.UpdatedAt = t
		}
	}
	if createdAt != nil {
		if t, err := time.Parse(time.RFC3339Nano, *createdAt); err == nil {
			r.CreatedAt = t
		}
	}
	return &r, nil
}

func scanRunRow(row *sql.Row) (*Run, error) {
	return scanRun(row)
}

func parseTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	return &t
}
