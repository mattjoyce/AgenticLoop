package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"github.com/mattjoyce/agenticloop/internal/config"
	"github.com/mattjoyce/agenticloop/internal/store"
)

// Loop builds and executes an Eino ReAct agent for a single run.
type Loop struct {
	chatModel model.ToolCallingChatModel
	tools     []tool.BaseTool
	cfg       config.AgentConfig
	runStore  *store.RunStore
	stepStore *store.StepStore
	logger    *slog.Logger
}

// NewLoop creates a new Loop.
func NewLoop(chatModel model.ToolCallingChatModel, tools []tool.BaseTool, cfg config.AgentConfig, runStore *store.RunStore, stepStore *store.StepStore, logger *slog.Logger) *Loop {
	return &Loop{
		chatModel: chatModel,
		tools:     tools,
		cfg:       cfg,
		runStore:  runStore,
		stepStore: stepStore,
		logger:    logger,
	}
}

// Execute runs the agent loop for a given run. It persists steps and updates run status.
func (l *Loop) Execute(ctx context.Context, run *store.Run) error {
	l.logger.Info("starting agent loop", "run_id", run.ID, "goal", run.Goal)

	// Mark running
	if err := l.runStore.UpdateStatus(ctx, run.ID, store.RunStatusRunning, nil, nil); err != nil {
		return fmt.Errorf("mark run running: %w", err)
	}

	// Determine max loops and deadline
	maxLoops := l.cfg.DefaultMaxLoops
	deadline := l.cfg.DefaultDeadline

	// Apply constraints if present
	if len(run.Constraints) > 0 {
		var constraints struct {
			MaxLoops int    `json:"max_loops"`
			Deadline string `json:"deadline"`
		}
		if err := json.Unmarshal(run.Constraints, &constraints); err == nil {
			if constraints.MaxLoops > 0 {
				maxLoops = constraints.MaxLoops
			}
			if constraints.Deadline != "" {
				if d, err := time.ParseDuration(constraints.Deadline); err == nil {
					deadline = d
				}
			}
		}
	}

	// Apply deadline
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	// Build system prompt
	systemPrompt := fmt.Sprintf("You are an agent executing a task. Your goal is:\n\n%s", run.Goal)
	if len(run.Context) > 0 {
		systemPrompt += fmt.Sprintf("\n\nAdditional context:\n%s", string(run.Context))
	}

	// Build ReAct agent
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: l.chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: l.tools,
		},
		MessageModifier: func(ctx context.Context, input []*schema.Message) []*schema.Message {
			res := make([]*schema.Message, 0, len(input)+1)
			res = append(res, schema.SystemMessage(systemPrompt))
			res = append(res, input...)
			return res
		},
		MaxStep: maxLoops,
	})
	if err != nil {
		errMsg := fmt.Sprintf("build agent: %s", err)
		_ = l.runStore.UpdateStatus(ctx, run.ID, store.RunStatusFailed, nil, &errMsg)
		return fmt.Errorf("build agent: %w", err)
	}

	// Load existing steps for crash resume
	existingSteps, err := l.stepStore.GetByRunID(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("load existing steps: %w", err)
	}

	stepNum, err := l.stepStore.MaxStepNum(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("get max step num: %w", err)
	}

	// Build conversation from existing steps (crash resume)
	messages := buildConversation(existingSteps)
	if len(messages) == 0 {
		messages = []*schema.Message{
			schema.UserMessage(run.Goal),
		}
	}

	// Record the reasoning step
	stepNum++
	_, err = l.stepStore.Append(ctx, run.ID, stepNum, store.StepPhaseReason, nil, nil)
	if err != nil {
		l.logger.Error("failed to record reason step", "run_id", run.ID, "error", err)
	}

	// Execute the agent (non-streaming, safe with Claude)
	result, err := agent.Generate(ctx, messages)
	if err != nil {
		errMsg := err.Error()
		_ = l.runStore.UpdateStatus(ctx, run.ID, store.RunStatusFailed, nil, &errMsg)
		return fmt.Errorf("agent generate: %w", err)
	}

	// Extract summary from final message
	summary := result.Content
	if err := l.runStore.UpdateStatus(ctx, run.ID, store.RunStatusDone, &summary, nil); err != nil {
		return fmt.Errorf("mark run done: %w", err)
	}

	// Record completion step
	stepNum++
	completionJSON, _ := json.Marshal(map[string]string{"content": result.Content})
	doneStep, err := l.stepStore.Append(ctx, run.ID, stepNum, store.StepPhaseDone, nil, nil)
	if err == nil {
		_ = l.stepStore.UpdateStatus(ctx, doneStep.ID, store.StepStatusOK, completionJSON, nil)
	}

	l.logger.Info("agent loop completed", "run_id", run.ID, "steps", stepNum)
	return nil
}

// buildConversation reconstructs the message history from persisted steps.
func buildConversation(steps []*store.Step) []*schema.Message {
	if len(steps) == 0 {
		return nil
	}

	var messages []*schema.Message
	for _, step := range steps {
		switch step.Phase {
		case store.StepPhaseAct:
			if step.Tool != nil && step.ToolInput != nil {
				// Represent as assistant message with tool call
				messages = append(messages, schema.AssistantMessage("", []schema.ToolCall{
					{
						ID: step.ID,
						Function: schema.FunctionCall{
							Name:      *step.Tool,
							Arguments: string(step.ToolInput),
						},
					},
				}))
			}
		case store.StepPhaseObserve:
			if step.ToolOutput != nil {
				messages = append(messages, schema.ToolMessage(string(step.ToolOutput), step.ID))
			}
		}
	}
	return messages
}
