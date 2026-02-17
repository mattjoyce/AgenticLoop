package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// StepPhase represents what part of the agent loop a step is in.
type StepPhase string

const (
	StepPhaseFrame   StepPhase = "frame"
	StepPhasePlan    StepPhase = "plan"
	StepPhaseReason  StepPhase = "reason"
	StepPhaseAct     StepPhase = "act"
	StepPhaseObserve StepPhase = "observe"
	StepPhaseReflect StepPhase = "reflect"
	StepPhaseDone    StepPhase = "done"
)

// StepStatus represents the lifecycle state of a step.
type StepStatus string

const (
	StepStatusPending StepStatus = "pending"
	StepStatusRunning StepStatus = "running"
	StepStatusOK      StepStatus = "ok"
	StepStatusError   StepStatus = "error"
)

// Step represents a single step in an agent run.
type Step struct {
	ID          string          `json:"id"`
	RunID       string          `json:"run_id"`
	StepNum     int             `json:"step_num"`
	Phase       StepPhase       `json:"phase"`
	Tool        *string         `json:"tool,omitempty"`
	ToolInput   json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput  json.RawMessage `json:"tool_output,omitempty"`
	Status      StepStatus      `json:"status"`
	Attempt     int             `json:"attempt"`
	Error       *string         `json:"error,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// StepStore provides operations on the steps table.
type StepStore struct {
	db *sql.DB
}

// NewStepStore creates a new StepStore.
func NewStepStore(db *sql.DB) *StepStore {
	return &StepStore{db: db}
}

// Append inserts a new step for a run.
func (s *StepStore) Append(ctx context.Context, runID string, stepNum int, phase StepPhase, tool *string, toolInput json.RawMessage) (*Step, error) {
	now := time.Now().UTC()
	step := &Step{
		ID:        uuid.New().String(),
		RunID:     runID,
		StepNum:   stepNum,
		Phase:     phase,
		Tool:      tool,
		ToolInput: toolInput,
		Status:    StepStatusPending,
		Attempt:   1,
		CreatedAt: now,
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO steps (id, run_id, step_num, phase, tool, tool_input, status, attempt, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		step.ID, step.RunID, step.StepNum, string(step.Phase),
		step.Tool, step.ToolInput, string(step.Status), step.Attempt,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert step: %w", err)
	}

	return step, nil
}

// UpdateStatus updates a step's status and output.
func (s *StepStore) UpdateStatus(ctx context.Context, id string, status StepStatus, toolOutput json.RawMessage, errMsg *string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var startedAt, completedAt *string
	if status == StepStatusRunning {
		startedAt = &now
	}
	if status == StepStatusOK || status == StepStatusError {
		completedAt = &now
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE steps SET status = ?, tool_output = COALESCE(?, tool_output), error = COALESCE(?, error),
		 started_at = COALESCE(?, started_at), completed_at = COALESCE(?, completed_at)
		 WHERE id = ?`,
		string(status), toolOutput, errMsg, startedAt, completedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update step status: %w", err)
	}
	return nil
}

// GetByRunID retrieves all steps for a run, ordered by step_num.
func (s *StepStore) GetByRunID(ctx context.Context, runID string) ([]*Step, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, step_num, phase, tool, tool_input, tool_output, status, attempt, error, started_at, completed_at, created_at
		 FROM steps WHERE run_id = ? ORDER BY step_num ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("get steps by run: %w", err)
	}
	defer rows.Close()

	var steps []*Step
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// MaxStepNum returns the highest step_num for a run, or 0 if none.
func (s *StepStore) MaxStepNum(ctx context.Context, runID string) (int, error) {
	var maxNum sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(step_num) FROM steps WHERE run_id = ?`, runID).Scan(&maxNum)
	if err != nil {
		return 0, fmt.Errorf("max step num: %w", err)
	}
	if !maxNum.Valid {
		return 0, nil
	}
	return int(maxNum.Int64), nil
}

func scanStep(s scanner) (*Step, error) {
	var step Step
	var phase, status string
	var tool sql.NullString
	var toolInputJSON sql.NullString
	var toolOutputJSON sql.NullString
	var errMsg sql.NullString
	var startedAt, completedAt, createdAt *string

	err := s.Scan(&step.ID, &step.RunID, &step.StepNum, &phase,
		&tool, &toolInputJSON, &toolOutputJSON, &status,
		&step.Attempt, &errMsg, &startedAt, &completedAt, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("scan step: %w", err)
	}

	if tool.Valid {
		v := tool.String
		step.Tool = &v
	}
	if toolInputJSON.Valid && toolInputJSON.String != "" {
		step.ToolInput = json.RawMessage(toolInputJSON.String)
	}
	if toolOutputJSON.Valid && toolOutputJSON.String != "" {
		step.ToolOutput = json.RawMessage(toolOutputJSON.String)
	}
	if errMsg.Valid {
		v := errMsg.String
		step.Error = &v
	}

	step.Phase = StepPhase(phase)
	step.Status = StepStatus(status)
	step.StartedAt = parseTime(startedAt)
	step.CompletedAt = parseTime(completedAt)
	if createdAt != nil {
		if t, err := time.Parse(time.RFC3339Nano, *createdAt); err == nil {
			step.CreatedAt = t
		}
	}
	return &step, nil
}
