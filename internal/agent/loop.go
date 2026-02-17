package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"text/template"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/mattjoyce/agenticloop/internal/config"
	"github.com/mattjoyce/agenticloop/internal/ductile"
	"github.com/mattjoyce/agenticloop/internal/localtools"
	"github.com/mattjoyce/agenticloop/internal/store"
)

// Loop builds and executes an explicit staged agent loop for a single run.
type Loop struct {
	chatModel model.ToolCallingChatModel
	tools     []tool.BaseTool
	cfg       config.AgentConfig
	runStore  *store.RunStore
	stepStore *store.StepStore
	client    *ductile.Client
	logger    *slog.Logger
}

// NewLoop creates a new Loop.
func NewLoop(chatModel model.ToolCallingChatModel, tools []tool.BaseTool, cfg config.AgentConfig, runStore *store.RunStore, stepStore *store.StepStore, client *ductile.Client, logger *slog.Logger) *Loop {
	return &Loop{
		chatModel: chatModel,
		tools:     tools,
		cfg:       cfg,
		runStore:  runStore,
		stepStore: stepStore,
		client:    client,
		logger:    logger,
	}
}

// Execute runs the staged loop for a given run. It persists steps and updates run status.
func (l *Loop) Execute(ctx context.Context, run *store.Run, callbackURL string) error {
	l.logger.Info("starting agent loop", "run_id", run.ID, "goal", run.Goal)

	if err := l.runStore.UpdateStatus(ctx, run.ID, store.RunStatusRunning, nil, nil); err != nil {
		return fmt.Errorf("mark run running: %w", err)
	}

	ws, err := NewWorkspace(l.cfg.WorkspaceDir, run.ID)
	if err != nil {
		l.logger.Error("failed to create workspace", "run_id", run.ID, "error", err)
	}

	maxLoops := l.cfg.DefaultMaxLoops
	deadline := l.cfg.DefaultDeadline
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

	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	stepNum, err := l.stepStore.MaxStepNum(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("get max step num: %w", err)
	}

	state := stageState{
		Goal:        run.Goal,
		Context:     jsonOrNull(run.Context),
		Constraints: jsonOrNull(run.Constraints),
		MaxLoops:    maxLoops,
	}

	if ws != nil {
		if memory := ws.ReadRunMemory(); memory != "" {
			state.Memory = clipText(memory, 12000)
		}
		if err := ws.WritePromptSnapshot(run.Goal, run.Context, run.Constraints, "staged-prompts: frame, plan, act, reflect"); err != nil {
			l.logger.Error("failed to write prompt snapshot", "run_id", run.ID, "error", err)
		}
	}

	activeTools := l.tools
	if ws != nil {
		activeTools = l.rebuildToolsWithObserver(ws)
	}

	toolset, err := l.buildToolset(ctx, activeTools)
	if err != nil {
		errMsg := err.Error()
		_ = l.runStore.UpdateStatus(ctx, run.ID, store.RunStatusFailed, nil, &errMsg)
		l.emitCallback(ctx, callbackURL, run.ID, "failed", nil, &errMsg)
		return err
	}

	for iter := 1; iter <= maxLoops; iter++ {
		select {
		case <-ctx.Done():
			return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("context cancelled: %w", ctx.Err()))
		default:
		}
		state.Iteration = iter
		if ws != nil {
			state.Memory = clipText(ws.ReadRunMemory(), 12000)
			if err := ws.ClearLoopMemory(); err != nil {
				l.logger.Error("failed to clear loop memory", "run_id", run.ID, "iteration", iter, "error", err)
			}
		}

		framePrompt := l.renderPrompt(l.cfg.Prompts.Frame, state)
		if ws != nil {
			_ = ws.AppendStagePrompt(iter, "frame", framePrompt)
		}
		frameOut, err := l.runTextStage(ctx, framePrompt, "Produce the frame now.")
		if err != nil {
			return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("frame stage: %w", err))
		}
		state.Frame = frameOut
		if err := l.appendTextStep(ctx, run.ID, &stepNum, store.StepPhaseFrame, frameOut); err != nil {
			l.logger.Error("failed to persist frame step", "run_id", run.ID, "error", err)
		}

		planPrompt := l.renderPrompt(l.cfg.Prompts.Plan, state)
		if ws != nil {
			_ = ws.AppendStagePrompt(iter, "plan", planPrompt)
		}
		planOut, err := l.runTextStage(ctx, planPrompt, "Produce the plan now.")
		if err != nil {
			return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("plan stage: %w", err))
		}
		state.Plan = planOut
		if err := l.appendTextStep(ctx, run.ID, &stepNum, store.StepPhasePlan, planOut); err != nil {
			l.logger.Error("failed to persist plan step", "run_id", run.ID, "error", err)
		}

		actPrompt := l.renderPrompt(l.cfg.Prompts.Act, state)
		if ws != nil {
			_ = ws.AppendStagePrompt(iter, "act", actPrompt)
		}
		actResult, err := l.runActStage(ctx, toolset, actPrompt)
		if err != nil {
			return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("act stage: %w", err))
		}
		state.Act = actResult.Summary
		if actResult.SuccessReported {
			state.SuccessReported = true
			if actResult.ReportedSummary != "" {
				state.SuccessSummary = actResult.ReportedSummary
			}
		}
		if ws != nil {
			state.LoopMemory = clipText(ws.ReadLoopMemory(), 12000)
		}
		if err := l.appendTextStep(ctx, run.ID, &stepNum, store.StepPhaseAct, actResult.Summary); err != nil {
			l.logger.Error("failed to persist act summary step", "run_id", run.ID, "error", err)
		}

		reflectPrompt := l.renderPrompt(l.cfg.Prompts.Reflect, state)
		if ws != nil {
			_ = ws.AppendStagePrompt(iter, "reflect", reflectPrompt)
		}
		reflectOut, err := l.runTextStage(ctx, reflectPrompt, "Return reflection JSON now.")
		if err != nil {
			return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("reflect stage: %w", err))
		}
		if err := l.appendTextStep(ctx, run.ID, &stepNum, store.StepPhaseReflect, reflectOut); err != nil {
			l.logger.Error("failed to persist reflect step", "run_id", run.ID, "error", err)
		}

		decision := parseReflectDecision(reflectOut)
		if ws != nil {
			memoryUpdate := strings.TrimSpace(decision.MemoryUpdate)
			if memoryUpdate == "" {
				memoryUpdate = strings.TrimSpace(decision.NextFocus)
			}
			if memoryUpdate != "" {
				if err := ws.AppendRunMemory(iter, memoryUpdate); err != nil {
					l.logger.Error("failed to append run memory", "run_id", run.ID, "iteration", iter, "error", err)
				}
			}
			if err := ws.ClearLoopMemory(); err != nil {
				l.logger.Error("failed to clear loop memory after reflect", "run_id", run.ID, "iteration", iter, "error", err)
			}
		}

		if decision.Done {
			if !state.SuccessReported {
				state.NextFocus = "Call report_success with summary and evidence before declaring done."
				l.logger.Info("reflect requested done but report_success not yet called; continuing", "run_id", run.ID, "iteration", iter)
				continue
			}

			summary := strings.TrimSpace(decision.Summary)
			if summary == "" {
				summary = strings.TrimSpace(state.SuccessSummary)
			}
			if summary == "" {
				summary = strings.TrimSpace(state.Act)
			}
			doneCtx, doneCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := l.runStore.UpdateStatus(doneCtx, run.ID, store.RunStatusDone, &summary, nil); err != nil {
				doneCancel()
				return fmt.Errorf("mark run done: %w", err)
			}
			if err := l.appendTextStep(doneCtx, run.ID, &stepNum, store.StepPhaseDone, summary); err != nil {
				l.logger.Error("failed to persist done step", "run_id", run.ID, "error", err)
			}
			doneCancel()
			l.emitCallback(ctx, callbackURL, run.ID, "done", &summary, nil)
			l.logger.Info("agent loop completed", "run_id", run.ID, "iteration", iter)
			return nil
		}

		state.NextFocus = decision.NextFocus
	}

	if !state.SuccessReported {
		return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("max loops exhausted without required report_success call"))
	}
	return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("max loops exhausted without completion"))
}

type stageState struct {
	Goal            string
	Context         string
	Constraints     string
	Memory          string
	LoopMemory      string
	Frame           string
	Plan            string
	Act             string
	NextFocus       string
	SuccessReported bool
	SuccessSummary  string
	Iteration       int
	MaxLoops        int
}

type reflectDecision struct {
	Done         bool   `json:"done"`
	Summary      string `json:"summary"`
	NextFocus    string `json:"next_focus"`
	MemoryUpdate string `json:"memory_update"`
}

type preparedToolset struct {
	model  model.ToolCallingChatModel
	byName map[string]tool.InvokableTool
}

func (l *Loop) buildToolset(ctx context.Context, tools []tool.BaseTool) (*preparedToolset, error) {
	infos := make([]*schema.ToolInfo, 0, len(tools))
	byName := make(map[string]tool.InvokableTool, len(tools))

	for _, base := range tools {
		inv, ok := base.(tool.InvokableTool)
		if !ok {
			continue
		}
		info, err := inv.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("tool info: %w", err)
		}
		infos = append(infos, info)
		byName[info.Name] = inv
	}

	toolModel, err := l.chatModel.WithTools(infos)
	if err != nil {
		return nil, fmt.Errorf("bind tools: %w", err)
	}

	return &preparedToolset{model: toolModel, byName: byName}, nil
}

func (l *Loop) runTextStage(ctx context.Context, prompt, userDirective string) (string, error) {
	if l.cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.cfg.StepTimeout)
		defer cancel()
	}
	msgs := []*schema.Message{
		schema.SystemMessage(prompt),
		schema.UserMessage(userDirective),
	}
	var resp *schema.Message
	var err error
	maxRetries := l.cfg.MaxRetryPerStep
	if maxRetries <= 0 {
		maxRetries = 1
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err = l.chatModel.Generate(ctx, msgs)
		if err == nil {
			break
		}
		if attempt < maxRetries-1 {
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			l.logger.Warn("text stage LLM error, retrying", "attempt", attempt+1, "backoff", backoff, "error", err)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

type actStageResult struct {
	Summary         string
	SuccessReported bool
	ReportedSummary string
}

func (l *Loop) runActStage(ctx context.Context, toolset *preparedToolset, prompt string) (actStageResult, error) {
	if l.cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.cfg.StepTimeout)
		defer cancel()
	}
	messages := []*schema.Message{
		schema.SystemMessage(prompt),
		schema.UserMessage("Execute the action now. Use tools when needed, then summarize what you accomplished."),
	}

	result := actStageResult{}
	var transcript strings.Builder
	maxRounds := l.cfg.MaxActRounds
	if maxRounds <= 0 {
		maxRounds = 6
	}
	toolSeq := 0

	for round := 1; round <= maxRounds; round++ {
		var resp *schema.Message
		var genErr error
		maxRetries := l.cfg.MaxRetryPerStep
		if maxRetries <= 0 {
			maxRetries = 1
		}
		for attempt := 0; attempt < maxRetries; attempt++ {
			resp, genErr = toolset.model.Generate(ctx, messages)
			if genErr == nil {
				break
			}
			if attempt < maxRetries-1 {
				backoff := time.Duration(1<<uint(attempt)) * time.Second
				l.logger.Warn("act stage LLM error, retrying", "round", round, "attempt", attempt+1, "backoff", backoff, "error", genErr)
				select {
				case <-ctx.Done():
					return result, ctx.Err()
				case <-time.After(backoff):
				}
			}
		}
		if genErr != nil {
			return result, genErr
		}
		messages = append(messages, resp)

		if len(resp.ToolCalls) == 0 {
			content := strings.TrimSpace(resp.Content)
			if content != "" {
				if transcript.Len() > 0 {
					transcript.WriteString("\n\n")
				}
				transcript.WriteString(content)
			}
			if strings.TrimSpace(transcript.String()) != "" {
				result.Summary = strings.TrimSpace(transcript.String())
				return result, nil
			}
			result.Summary = "No actionable output produced."
			return result, nil
		}

		for _, tc := range resp.ToolCalls {
			toolSeq++
			name := tc.Function.Name
			arguments := normalizeJSON(tc.Function.Arguments)

			inv, ok := toolset.byName[name]
			if !ok {
				errMsg := fmt.Sprintf("unknown tool: %s", name)
				obsJSON := mustJSON(map[string]string{"error": errMsg})
				messages = append(messages, schema.ToolMessage(string(obsJSON), toolCallID(tc, name, toolSeq)))
				transcript.WriteString(fmt.Sprintf("Tool %s error: %s\n", name, errMsg))
				continue
			}

			out, runErr := inv.InvokableRun(ctx, string(arguments))
			obsJSON := normalizeJSON(out)
			if runErr != nil {
				e := runErr.Error()
				obsJSON = mustJSON(map[string]string{"error": e})
			} else if name == "report_success" {
				result.SuccessReported = true
				if summary := extractSummaryFromArguments(arguments); summary != "" {
					result.ReportedSummary = summary
				}
			}

			messages = append(messages, schema.ToolMessage(string(obsJSON), toolCallID(tc, name, toolSeq)))
			transcript.WriteString(fmt.Sprintf("Tool %s output:\n%s\n", name, string(obsJSON)))
		}
	}

	result.Summary = strings.TrimSpace(transcript.String())
	return result, nil
}

func (l *Loop) appendTextStep(ctx context.Context, runID string, stepNum *int, phase store.StepPhase, content string) error {
	*stepNum = *stepNum + 1
	step, err := l.stepStore.Append(ctx, runID, *stepNum, phase, nil, nil)
	if err != nil {
		return err
	}
	out := mustJSON(map[string]string{"content": content})
	return l.stepStore.UpdateStatus(ctx, step.ID, store.StepStatusOK, out, nil)
}

func (l *Loop) renderPrompt(tmpl string, data stageState) string {
	t, err := template.New("stage_prompt").Parse(tmpl)
	if err != nil {
		return tmpl
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return tmpl
	}
	return b.String()
}

func (l *Loop) failRun(_ context.Context, callbackURL, runID string, err error) error {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errMsg := err.Error()
	_ = l.runStore.UpdateStatus(bgCtx, runID, store.RunStatusFailed, nil, &errMsg)
	l.emitCallback(bgCtx, callbackURL, runID, "failed", nil, &errMsg)
	return err
}

func (l *Loop) emitCallback(_ context.Context, callbackURL, runID, status string, summary *string, errMsg *string) {
	if callbackURL == "" || l.client == nil {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload := map[string]any{
		"run_id": runID,
		"status": status,
	}
	if summary != nil {
		payload["summary"] = *summary
	}
	if errMsg != nil {
		payload["error"] = *errMsg
	}

	if err := l.client.Callback(bgCtx, callbackURL, payload); err != nil {
		l.logger.Error("failed to emit callback", "run_id", runID, "url", callbackURL, "error", err)
	} else {
		l.logger.Info("callback emitted", "run_id", runID, "url", callbackURL, "status", status)
	}
}

func jsonOrNull(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}

func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}

func normalizeJSON(s string) json.RawMessage {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return mustJSON(map[string]string{"raw": ""})
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	return mustJSON(map[string]string{"raw": trimmed})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func toolCallID(tc schema.ToolCall, toolName string, idx int) string {
	if tc.ID != "" {
		return tc.ID
	}
	return fmt.Sprintf("%s-%d", toolName, idx)
}

func parseReflectDecision(raw string) reflectDecision {
	text := strings.TrimSpace(raw)
	if text == "" {
		return reflectDecision{}
	}

	var d reflectDecision
	if err := json.Unmarshal([]byte(text), &d); err == nil {
		return d
	}

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(text[start:end+1]), &d); err == nil {
			return d
		}
	}

	return reflectDecision{Done: false, Summary: text}
}

func extractSummaryFromArguments(arguments json.RawMessage) string {
	var payload struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(arguments, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Summary)
}

func (l *Loop) rebuildToolsWithObserver(ws *Workspace) []tool.BaseTool {
	observer := func(toolName, input, output, status string) {
		if err := ws.AppendLoopToolCall(toolName, input, output, status); err != nil {
			l.logger.Error("failed to write loop memory", "tool", toolName, "error", err)
		}
	}

	var wrapped []tool.BaseTool
	for _, t := range l.tools {
		if dt, ok := t.(*ductile.DuctileTool); ok {
			wrapped = append(wrapped, dt.WithObserver(observer))
		} else if st, ok := t.(*localtools.CommandTool); ok {
			wrapped = append(wrapped, st.WithObserver(observer))
		} else if rs, ok := t.(*localtools.ReportSuccessTool); ok {
			wrapped = append(wrapped, rs.WithObserver(observer))
		} else {
			wrapped = append(wrapped, t)
		}
	}

	// Add workspace file tools sandboxed to the run's workspace directory.
	for _, wt := range localtools.BuildWorkspaceTools(ws.Dir()) {
		wrapped = append(wrapped, wt.WithObserver(observer))
	}

	return wrapped
}
