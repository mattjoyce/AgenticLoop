package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"

	"github.com/mattjoyce/agenticloop/internal/config"
	"github.com/mattjoyce/agenticloop/internal/ductile"
	"github.com/mattjoyce/agenticloop/internal/store"
)

// Runner manages the serial execution of agent runs.
type Runner struct {
	runStore  *store.RunStore
	stepStore *store.StepStore
	chatModel model.ToolCallingChatModel
	tools     []tool.BaseTool
	cfg       config.AgentConfig
	client    *ductile.Client
	callback  string
	logger    *slog.Logger

	queue chan string
	mu    sync.Mutex
	done  chan struct{}
}

var ErrQueueFull = errors.New("runner queue is full")

// NewRunner creates a new Runner.
func NewRunner(runStore *store.RunStore, stepStore *store.StepStore, chatModel model.ToolCallingChatModel, tools []tool.BaseTool, cfg config.AgentConfig, client *ductile.Client, callbackURL string, logger *slog.Logger) *Runner {
	capacity := cfg.QueueCapacity
	if capacity <= 0 {
		capacity = 100
	}

	return &Runner{
		runStore:  runStore,
		stepStore: stepStore,
		chatModel: chatModel,
		tools:     tools,
		cfg:       cfg,
		client:    client,
		callback:  callbackURL,
		logger:    logger,
		queue:     make(chan string, capacity),
		done:      make(chan struct{}),
	}
}

// Create creates a run (delegates to RunStore) and satisfies the RunCreator interface.
func (r *Runner) Create(ctx context.Context, goal string, wakeID *string, runCtx json.RawMessage, constraints json.RawMessage) (*store.Run, bool, error) {
	return r.runStore.Create(ctx, goal, wakeID, runCtx, constraints)
}

// GetByID retrieves a run by ID (satisfies RunCreator interface).
func (r *Runner) GetByID(ctx context.Context, id string) (*store.Run, error) {
	return r.runStore.GetByID(ctx, id)
}

// Enqueue adds a run ID to the processing queue.
// It returns ErrQueueFull when the queue cannot accept the run within EnqueueTimeout.
func (r *Runner) Enqueue(runID string) error {
	timeout := r.cfg.EnqueueTimeout
	if timeout <= 0 {
		select {
		case r.queue <- runID:
			return nil
		default:
			return ErrQueueFull
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r.queue <- runID:
		return nil
	case <-timer.C:
		return ErrQueueFull
	}
}

// Start runs the serial worker loop. Blocks until context is cancelled.
func (r *Runner) Start(ctx context.Context) {
	defer close(r.done)
	r.logger.Info("agent runner started")
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("agent runner stopping")
			return
		case runID := <-r.queue:
			r.processRun(ctx, runID)
		}
	}
}

// Done returns a channel that is closed when the runner has finished processing
// and the Start method has returned. Use this for graceful shutdown.
func (r *Runner) Done() <-chan struct{} {
	return r.done
}

// RecoverRuns finds interrupted runs (status=running or queued) and re-enqueues them.
func (r *Runner) RecoverRuns(ctx context.Context) error {
	running, err := r.runStore.ListByStatus(ctx, store.RunStatusRunning)
	if err != nil {
		return err
	}
	queued, err := r.runStore.ListByStatus(ctx, store.RunStatusQueued)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(running)+len(queued))
	enqueued := 0

	for _, run := range append(running, queued...) {
		if _, ok := seen[run.ID]; ok {
			continue
		}
		seen[run.ID] = struct{}{}

		r.logger.Info("recovering run", "run_id", run.ID, "status", run.Status)
		if err := r.Enqueue(run.ID); err != nil {
			r.logger.Warn("failed to enqueue recovered run", "run_id", run.ID, "status", run.Status, "error", err)
			continue
		}
		enqueued++
	}

	if len(seen) > 0 {
		r.logger.Info("recovery scan complete", "candidates", len(seen), "enqueued", enqueued)
	}
	return nil
}

func (r *Runner) processRun(ctx context.Context, runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, err := r.runStore.GetByID(ctx, runID)
	if err != nil {
		r.logger.Error("failed to load run for processing", "run_id", runID, "error", err)
		return
	}

	if run.Status != store.RunStatusQueued && run.Status != store.RunStatusRunning {
		r.logger.Warn("skipping run with unexpected status", "run_id", runID, "status", run.Status)
		return
	}

	loop := NewLoop(r.chatModel, r.tools, r.cfg, r.runStore, r.stepStore, r.client, r.logger)

	start := time.Now()
	if err := loop.Execute(ctx, run, r.callback); err != nil {
		r.logger.Error("run failed", "run_id", runID, "error", err, "duration", time.Since(start))
	} else {
		r.logger.Info("run completed", "run_id", runID, "duration", time.Since(start))
	}
}
