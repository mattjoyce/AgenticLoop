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
	dir        string
	memoryPath string
	promptPath string
}

// NewWorkspace creates a workspace directory for a run.
func NewWorkspace(baseDir, runID string) (*Workspace, error) {
	dir := filepath.Join(baseDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	return &Workspace{
		dir:        dir,
		memoryPath: filepath.Join(dir, "memory.md"),
		promptPath: filepath.Join(dir, "prompt.md"),
	}, nil
}

// AppendToolCall records a tool invocation and its result to the memory file.
func (w *Workspace) AppendToolCall(tool, input, output, status string) error {
	f, err := os.OpenFile(w.memoryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open memory file: %w", err)
	}
	defer f.Close()

	entry := fmt.Sprintf("## %s — %s\n**Status:** %s\n**Input:**\n```json\n%s\n```\n**Output:**\n```json\n%s\n```\n\n",
		time.Now().UTC().Format(time.RFC3339), tool, status, input, output)

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("write memory entry: %w", err)
	}
	return nil
}

// ReadMemory returns the full contents of the memory file, or empty string if none.
func (w *Workspace) ReadMemory() string {
	data, err := os.ReadFile(w.memoryPath)
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
