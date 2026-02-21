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
		if savedState := ws.ReadState(); savedState != "" {
			state.State = clipText(savedState, 12000)
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
		return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("prepare toolset: %w", err))
	}
	state.AvailableTools = buildToolCatalog(toolset.infos)

	nextStage := "frame" // first iteration always starts at frame

	for iter := 1; iter <= maxLoops; iter++ {
		select {
		case <-ctx.Done():
			return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("context cancelled: %w", ctx.Err()))
		default:
		}
		state.Iteration = iter
		if ws != nil {
			state.Memory = clipText(ws.ReadRunMemory(), 12000)
			state.State = clipText(ws.ReadState(), 12000)
			if l.cfg.SaveLoopMemory && iter > 1 {
				if err := ws.ArchiveLoopMemory(iter - 1); err != nil {
					l.logger.Error("failed to archive loop memory", "run_id", run.ID, "iteration", iter-1, "error", err)
				}
			}
			if err := ws.ClearLoopMemory(); err != nil {
				l.logger.Error("failed to clear loop memory", "run_id", run.ID, "iteration", iter, "error", err)
			}
		}

		l.logger.Info("loop iteration", "run_id", run.ID, "iter", iter, "next_stage", nextStage)

		if nextStage == "frame" {
			framePrompt := l.renderPrompt(l.cfg.Prompts.Frame, state)
			if ws != nil {
				_ = ws.AppendStagePrompt(iter, "frame", framePrompt)
			}
			frameOut, err := l.runTextStageStep(ctx, run.ID, &stepNum, store.StepPhaseFrame, framePrompt, "Produce the frame now.")
			if err != nil {
				return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("frame stage: %w", err))
			}
			state.Frame = frameOut
			if ws != nil {
				statePayload := normalizeStateJSON(frameOut)
				if err := ws.WriteState(statePayload); err != nil {
					l.logger.Error("failed to write frame state", "run_id", run.ID, "iteration", iter, "error", err)
				} else {
					state.State = clipText(string(statePayload), 12000)
				}
			}
		}

		if nextStage == "frame" || nextStage == "plan" {
			planPrompt := l.renderPrompt(l.cfg.Prompts.Plan, state)
			if ws != nil {
				_ = ws.AppendStagePrompt(iter, "plan", planPrompt)
			}
			planOut, err := l.runTextStageStep(ctx, run.ID, &stepNum, store.StepPhasePlan, planPrompt, "Produce the plan now.")
			if err != nil {
				return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("plan stage: %w", err))
			}
			state.Plan = planOut
		}

		actPrompt := l.renderPrompt(l.cfg.Prompts.Act, state)
		if ws != nil {
			_ = ws.AppendStagePrompt(iter, "act", actPrompt)
		}
		actResult, err := l.runActStageStep(ctx, run.ID, &stepNum, toolset, actPrompt)
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

		reflectPrompt := l.renderPrompt(l.cfg.Prompts.Reflect, state)
		if ws != nil {
			_ = ws.AppendStagePrompt(iter, "reflect", reflectPrompt)
		}
		reflectOut, err := l.runTextStageStep(ctx, run.ID, &stepNum, store.StepPhaseReflect, reflectPrompt, "Return reflection JSON now.")
		if err != nil {
			return l.failRun(ctx, callbackURL, run.ID, fmt.Errorf("reflect stage: %w", err))
		}

		decision := parseReflectDecision(reflectOut)
		if ws != nil {
			if len(decision.UpdatedState) > 0 {
				mergedState, err := mergeStateJSON(json.RawMessage(ws.ReadState()), decision.UpdatedState)
				if err != nil {
					l.logger.Error("failed to merge updated_state into state.json", "run_id", run.ID, "iteration", iter, "error", err)
				} else if err := ws.WriteState(mergedState); err != nil {
					l.logger.Error("failed to persist merged state.json", "run_id", run.ID, "iteration", iter, "error", err)
				} else {
					state.State = clipText(string(mergedState), 12000)
				}
			}

			memoryUpdate := strings.TrimSpace(decision.MemoryUpdate)
			if memoryUpdate == "" {
				memoryUpdate = strings.TrimSpace(decision.NextFocus)
			}
			if memoryUpdate != "" {
				if err := ws.AppendRunMemory(iter, memoryUpdate); err != nil {
					l.logger.Error("failed to append run memory", "run_id", run.ID, "iteration", iter, "error", err)
				}
			}
			if l.cfg.SaveLoopMemory {
				if err := ws.ArchiveLoopMemory(iter); err != nil {
					l.logger.Error("failed to archive loop memory after reflect", "run_id", run.ID, "iteration", iter, "error", err)
				}
			}
			if err := ws.ClearLoopMemory(); err != nil {
				l.logger.Error("failed to clear loop memory after reflect", "run_id", run.ID, "iteration", iter, "error", err)
			}
		}

		nextStage = decision.resolvedNextStage()
		l.logger.Info("reflect decision", "run_id", run.ID, "iter", iter, "next_stage", nextStage)

		if nextStage == "done" {
			if !state.SuccessReported {
				state.NextFocus = "Call report_success with summary and evidence before declaring done."
				l.logger.Info("reflect requested done but report_success not yet called; continuing", "run_id", run.ID, "iteration", iter)
				nextStage = "plan"
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
	State           string
	LoopMemory      string
	Frame           string
	Plan            string
	Act             string
	NextFocus       string
	AvailableTools  string
	SuccessReported bool
	SuccessSummary  string
	Iteration       int
	MaxLoops        int
}

type reflectDecision struct {
	NextStage    string          `json:"next_stage"` // "plan" | "act" | "done"
	Done         bool            `json:"done"`       // legacy fallback
	Summary      string          `json:"summary"`
	NextFocus    string          `json:"next_focus"`
	MemoryUpdate string          `json:"memory_update"`
	UpdatedState json.RawMessage `json:"updated_state"`
}

func (d reflectDecision) resolvedNextStage() string {
	switch d.NextStage {
	case "plan", "act", "done":
		return d.NextStage
	}
	// legacy fallback
	if d.Done {
		return "done"
	}
	return "plan"
}

type preparedToolset struct {
	model  model.ToolCallingChatModel
	byName map[string]tool.InvokableTool
	infos  []*schema.ToolInfo
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

	return &preparedToolset{model: toolModel, byName: byName, infos: infos}, nil
}

func (l *Loop) runTextStage(ctx context.Context, prompt, userDirective string) (string, int, tokenUsage, error) {
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
	attempts := 0
	usage := tokenUsage{}
	maxRetries := l.cfg.MaxRetryPerStep
	if maxRetries <= 0 {
		maxRetries = 1
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		attempts = attempt + 1
		resp, err = l.chatModel.Generate(ctx, msgs)
		if err == nil {
			usage.add(tokenUsageFromMessage(resp))
			break
		}
		if attempt < maxRetries-1 {
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			l.logger.Warn("text stage LLM error, retrying", "attempt", attempt+1, "backoff", backoff, "error", err)
			select {
			case <-ctx.Done():
				return "", attempts, usage, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	if err != nil {
		return "", attempts, usage, err
	}
	return strings.TrimSpace(resp.Content), attempts, usage, nil
}

type actStageResult struct {
	Summary         string
	SuccessReported bool
	ReportedSummary string
	Attempts        int
	TokenUsage      tokenUsage
	ToolTokenUsage  map[string]toolTokenUsage
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
			result.Attempts++
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
		roundUsage := tokenUsageFromMessage(resp)
		result.TokenUsage.add(roundUsage)
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

		for i, tc := range resp.ToolCalls {
			toolSeq++
			name := tc.Function.Name
			arguments := normalizeJSON(tc.Function.Arguments)
			if name != "" {
				if result.ToolTokenUsage == nil {
					result.ToolTokenUsage = map[string]toolTokenUsage{}
				}
				stat := result.ToolTokenUsage[name]
				stat.Calls++
				stat.add(splitTokenUsage(roundUsage, len(resp.ToolCalls), i))
				result.ToolTokenUsage[name] = stat
			}

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

func (l *Loop) runTextStageStep(ctx context.Context, runID string, stepNum *int, phase store.StepPhase, prompt, userDirective string) (string, error) {
	*stepNum = *stepNum + 1
	step, err := l.stepStore.Append(ctx, runID, *stepNum, phase, nil, nil)
	if err != nil {
		return "", fmt.Errorf("append step: %w", err)
	}
	if err := l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusRunning, nil, nil, 1); err != nil {
		return "", fmt.Errorf("mark step running: %w", err)
	}

	out, attempts, usage, stageErr := l.runTextStage(ctx, prompt, userDirective)
	if attempts <= 0 {
		attempts = 1
	}
	if stageErr != nil {
		errMsg := stageErr.Error()
		_ = l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusError, nil, &errMsg, attempts)
		return "", stageErr
	}

	outPayload := map[string]any{"content": out}
	if !usage.isZero() {
		outPayload["token_usage"] = usage
	}
	outJSON := mustJSON(outPayload)
	if err := l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusOK, outJSON, nil, attempts); err != nil {
		return "", fmt.Errorf("mark step ok: %w", err)
	}
	return out, nil
}

func (l *Loop) runActStageStep(ctx context.Context, runID string, stepNum *int, toolset *preparedToolset, prompt string) (actStageResult, error) {
	*stepNum = *stepNum + 1
	step, err := l.stepStore.Append(ctx, runID, *stepNum, store.StepPhaseAct, nil, nil)
	if err != nil {
		return actStageResult{}, fmt.Errorf("append act step: %w", err)
	}
	if err := l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusRunning, nil, nil, 1); err != nil {
		return actStageResult{}, fmt.Errorf("mark act step running: %w", err)
	}

	result, stageErr := l.runActStage(ctx, toolset, prompt)
	attempts := result.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	if stageErr != nil {
		errMsg := stageErr.Error()
		_ = l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusError, nil, &errMsg, attempts)
		return result, stageErr
	}

	outPayload := map[string]any{"content": result.Summary}
	if !result.TokenUsage.isZero() {
		outPayload["token_usage"] = result.TokenUsage
	}
	if len(result.ToolTokenUsage) > 0 {
		outPayload["tool_token_usage"] = result.ToolTokenUsage
		outPayload["tool_token_usage_estimated"] = true
	}
	outJSON := mustJSON(outPayload)
	if err := l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusOK, outJSON, nil, attempts); err != nil {
		return actStageResult{}, fmt.Errorf("mark act step ok: %w", err)
	}
	return result, nil
}

func (l *Loop) appendTextStep(ctx context.Context, runID string, stepNum *int, phase store.StepPhase, content string) error {
	*stepNum = *stepNum + 1
	step, err := l.stepStore.Append(ctx, runID, *stepNum, phase, nil, nil)
	if err != nil {
		return err
	}
	if err := l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusRunning, nil, nil, 1); err != nil {
		return err
	}
	out := mustJSON(map[string]string{"content": content})
	return l.stepStore.UpdateStatusWithAttempt(ctx, step.ID, store.StepStatusOK, out, nil, 1)
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
	updateErr := l.runStore.UpdateStatus(bgCtx, runID, store.RunStatusFailed, nil, &errMsg)
	if updateErr != nil {
		l.logger.Error("failed to persist failed run status", "run_id", runID, "error", updateErr)
	}
	l.emitCallback(bgCtx, callbackURL, runID, "failed", nil, &errMsg)
	if updateErr != nil {
		return fmt.Errorf("%w; additionally failed to persist run status: %v", err, updateErr)
	}
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

func buildToolCatalog(infos []*schema.ToolInfo) string {
	var b strings.Builder
	for _, info := range infos {
		b.WriteString(info.Name)
		if info.Desc != "" {
			b.WriteString(" — ")
			b.WriteString(info.Desc)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
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

type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func tokenUsageFromMessage(msg *schema.Message) tokenUsage {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return tokenUsage{}
	}
	return tokenUsage{
		PromptTokens:     msg.ResponseMeta.Usage.PromptTokens,
		CompletionTokens: msg.ResponseMeta.Usage.CompletionTokens,
		TotalTokens:      msg.ResponseMeta.Usage.TotalTokens,
	}
}

func (u *tokenUsage) add(other tokenUsage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
}

func (u tokenUsage) isZero() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0
}

type toolTokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	Calls            int `json:"calls"`
}

func (u *toolTokenUsage) add(other tokenUsage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
}

func splitTokenUsage(u tokenUsage, parts, idx int) tokenUsage {
	if parts <= 0 {
		return tokenUsage{}
	}
	return tokenUsage{
		PromptTokens:     splitIntEvenly(u.PromptTokens, parts, idx),
		CompletionTokens: splitIntEvenly(u.CompletionTokens, parts, idx),
		TotalTokens:      splitIntEvenly(u.TotalTokens, parts, idx),
	}
}

func splitIntEvenly(total, parts, idx int) int {
	if parts <= 0 {
		return 0
	}
	base := total / parts
	rem := total % parts
	if idx >= 0 && idx < rem {
		return base + 1
	}
	return base
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

func normalizeStateJSON(raw string) json.RawMessage {
	text := strings.TrimSpace(raw)
	if text == "" {
		return mustJSON(map[string]any{
			"todo":     []map[string]any{},
			"evidence": []string{},
			"notes":    []string{},
		})
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err == nil {
		return mustJSON(obj)
	}

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(text[start:end+1]), &obj); err == nil {
			return mustJSON(obj)
		}
	}

	return mustJSON(map[string]any{
		"todo":     []map[string]any{},
		"evidence": []string{},
		"notes":    []string{text},
	})
}

func mergeStateJSON(existingRaw, updatedRaw json.RawMessage) (json.RawMessage, error) {
	existing := map[string]any{}
	if len(existingRaw) > 0 {
		if err := json.Unmarshal(existingRaw, &existing); err != nil {
			return nil, fmt.Errorf("parse existing state: %w", err)
		}
	}

	updated := map[string]any{}
	if len(updatedRaw) > 0 {
		if err := json.Unmarshal(updatedRaw, &updated); err != nil {
			return nil, fmt.Errorf("parse updated_state: %w", err)
		}
	}

	for k, v := range updated {
		switch k {
		case "todo":
			existing[k] = mergeTodo(existing[k], v)
		case "evidence", "notes":
			existing[k] = mergeStringLists(existing[k], v)
		default:
			existing[k] = v
		}
	}

	return mustJSON(existing), nil
}

func mergeTodo(existingVal, updatedVal any) []map[string]any {
	existingList := toObjectList(existingVal)
	updatedList := toObjectList(updatedVal)
	if len(existingList) == 0 {
		return updatedList
	}
	if len(updatedList) == 0 {
		return existingList
	}

	index := make(map[string]int, len(existingList))
	for i, item := range existingList {
		if id, _ := item["id"].(string); strings.TrimSpace(id) != "" {
			index[id] = i
		}
	}

	for _, item := range updatedList {
		id, _ := item["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			existingList = append(existingList, item)
			continue
		}
		if i, ok := index[id]; ok {
			existingList[i] = mergeObject(existingList[i], item)
			continue
		}
		index[id] = len(existingList)
		existingList = append(existingList, item)
	}
	return existingList
}

func mergeObject(existing, updated map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range updated {
		out[k] = v
	}
	return out
}

func mergeStringLists(existingVal, updatedVal any) []string {
	existing := toStringList(existingVal)
	updated := toStringList(updatedVal)
	if len(existing) == 0 {
		return updated
	}
	if len(updated) == 0 {
		return existing
	}

	seen := make(map[string]struct{}, len(existing)+len(updated))
	out := make([]string, 0, len(existing)+len(updated))
	for _, s := range existing {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range updated {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func toObjectList(v any) []map[string]any {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func toStringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
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
