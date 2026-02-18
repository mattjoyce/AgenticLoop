package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Workspace manages a per-run directory with a memory file for inter-loop context.
type Workspace struct {
	dir            string
	runMemoryPath  string
	loopMemoryPath string
	promptPath     string
}

// NewWorkspace creates a workspace directory for a run.
func NewWorkspace(baseDir, runID string) (*Workspace, error) {
	dir := filepath.Join(baseDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	return &Workspace{
		dir:            dir,
		runMemoryPath:  filepath.Join(dir, "run_memory.md"),
		loopMemoryPath: filepath.Join(dir, "loop_memory.md"),
		promptPath:     filepath.Join(dir, "prompt.md"),
	}, nil
}

// AppendLoopToolCall records a tool invocation and its result to the per-loop memory file.
func (w *Workspace) AppendLoopToolCall(tool, input, output, status string) error {
	f, err := os.OpenFile(w.loopMemoryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open loop memory file: %w", err)
	}
	defer f.Close()

	entry := fmt.Sprintf("## %s — %s\n**Status:** %s\n**Input:**\n```json\n%s\n```\n**Output:**\n```json\n%s\n```\n\n",
		time.Now().UTC().Format(time.RFC3339), tool, status, input, output)

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("write loop memory entry: %w", err)
	}
	return nil
}

// ReadLoopMemory returns the full contents of loop memory.
func (w *Workspace) ReadLoopMemory() string {
	data, err := os.ReadFile(w.loopMemoryPath)
	if err != nil {
		return ""
	}
	return string(data)
}

// ClearLoopMemory truncates loop memory so the next iteration starts clean.
func (w *Workspace) ClearLoopMemory() error {
	if err := os.WriteFile(w.loopMemoryPath, []byte(""), 0o644); err != nil {
		return fmt.Errorf("clear loop memory: %w", err)
	}
	return nil
}

// ArchiveLoopMemory copies the current loop_memory.md to loop_memory_iter_{iter}.md
// if the file is non-empty. A no-op when loop memory is empty.
func (w *Workspace) ArchiveLoopMemory(iter int) error {
	data, err := os.ReadFile(w.loopMemoryPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	dst := filepath.Join(w.dir, fmt.Sprintf("loop_memory_iter_%d.md", iter))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("archive loop memory iter %d: %w", iter, err)
	}
	return nil
}

// AppendRunMemory appends distilled reflective memory for cross-loop context.
func (w *Workspace) AppendRunMemory(iteration int, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	f, err := os.OpenFile(w.runMemoryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open run memory file: %w", err)
	}
	defer f.Close()

	entry := fmt.Sprintf("## Iteration %d — %s\n%s\n\n", iteration, time.Now().UTC().Format(time.RFC3339), strings.TrimSpace(text))
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("write run memory entry: %w", err)
	}
	return nil
}

// ReadRunMemory returns the full contents of persistent run memory.
func (w *Workspace) ReadRunMemory() string {
	data, err := os.ReadFile(w.runMemoryPath)
	if err != nil {
		return ""
	}
	return string(data)
}

// WritePromptSnapshot writes goal/context/constraints/system prompt for this run.
func (w *Workspace) WritePromptSnapshot(goal string, runContext, constraints json.RawMessage, systemPrompt string) error {
	var b strings.Builder
	b.WriteString("# Prompt Snapshot\n\n")
	b.WriteString("Generated: ")
	b.WriteString(time.Now().UTC().Format(time.RFC3339))
	b.WriteString("\n\n## Goal\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n## Context\n\n```json\n")
	if len(runContext) == 0 {
		b.WriteString("null")
	} else {
		b.Write(runContext)
	}
	b.WriteString("\n```\n\n## Constraints\n\n```json\n")
	if len(constraints) == 0 {
		b.WriteString("null")
	} else {
		b.Write(constraints)
	}
	b.WriteString("\n```\n\n## System Prompt\n\n```text\n")
	b.WriteString(systemPrompt)
	b.WriteString("\n```\n")

	if err := os.WriteFile(w.promptPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write prompt snapshot: %w", err)
	}
	return nil
}

// AppendStagePrompt appends a rendered stage prompt for an iteration.
func (w *Workspace) AppendStagePrompt(iteration int, stage, prompt string) error {
	f, err := os.OpenFile(w.promptPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open prompt file: %w", err)
	}
	defer f.Close()

	entry := fmt.Sprintf("\n## Iteration %d - %s Prompt\n\n```text\n%s\n```\n", iteration, stage, prompt)
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("append stage prompt: %w", err)
	}
	return nil
}

// Dir returns the workspace directory path.
func (w *Workspace) Dir() string {
	return w.dir
}
