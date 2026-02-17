package localtools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// WorkspaceFileTool exposes a single file operation sandboxed to a workspace directory.
type WorkspaceFileTool struct {
	name     string
	desc     string
	params   map[string]*schema.ParameterInfo
	handler  func(baseDir string, args json.RawMessage) (string, error)
	baseDir  string
	observer Observer
}

var _ tool.InvokableTool = (*WorkspaceFileTool)(nil)

// WithObserver returns a copy with the given observer attached.
func (t *WorkspaceFileTool) WithObserver(obs Observer) *WorkspaceFileTool {
	cp := *t
	cp.observer = obs
	return &cp
}

// Info returns tool metadata for model planning.
func (t *WorkspaceFileTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.name,
		Desc:        t.desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(t.params),
	}, nil
}

// InvokableRun executes the file operation.
func (t *WorkspaceFileTool) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	out, err := t.handler(t.baseDir, json.RawMessage(argumentsInJSON))
	status := "ok"
	if err != nil {
		status = "error"
		resp, _ := json.Marshal(map[string]any{"status": "error", "error": err.Error()})
		out = string(resp)
	}
	if t.observer != nil {
		t.observer(t.name, argumentsInJSON, out, status)
	}
	return out, nil
}

// sanitizePath validates and resolves a relative path within baseDir.
func sanitizePath(baseDir, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	joined := filepath.Join(baseDir, relPath)
	cleaned := filepath.Clean(joined)
	if !strings.HasPrefix(cleaned, filepath.Clean(baseDir)) {
		return "", fmt.Errorf("path escapes workspace directory")
	}
	return cleaned, nil
}

// BuildWorkspaceTools returns all workspace file tools sandboxed to baseDir.
func BuildWorkspaceTools(baseDir string) []*WorkspaceFileTool {
	tools := []*WorkspaceFileTool{
		{
			name: "workspace_write",
			desc: "Create or overwrite a file in the workspace. Creates parent directories as needed.",
			params: map[string]*schema.ParameterInfo{
				"path":    {Type: schema.String, Desc: "Relative path within the workspace"},
				"content": {Type: schema.String, Desc: "File content to write"},
			},
			handler: handleWrite,
		},
		{
			name: "workspace_read",
			desc: "Read the contents of a file in the workspace.",
			params: map[string]*schema.ParameterInfo{
				"path":      {Type: schema.String, Desc: "Relative path within the workspace"},
				"max_lines": {Type: schema.Integer, Desc: "Maximum lines to return (default 200)"},
			},
			handler: handleRead,
		},
		{
			name: "workspace_list",
			desc: "List entries in a workspace directory.",
			params: map[string]*schema.ParameterInfo{
				"path": {Type: schema.String, Desc: "Relative directory path (default '.')"},
			},
			handler: handleList,
		},
		{
			name: "workspace_append",
			desc: "Append content to a file in the workspace. Creates the file if it does not exist.",
			params: map[string]*schema.ParameterInfo{
				"path":    {Type: schema.String, Desc: "Relative path within the workspace"},
				"content": {Type: schema.String, Desc: "Content to append"},
			},
			handler: handleAppend,
		},
		{
			name: "workspace_delete",
			desc: "Delete a file in the workspace.",
			params: map[string]*schema.ParameterInfo{
				"path": {Type: schema.String, Desc: "Relative path within the workspace"},
			},
			handler: handleDelete,
		},
		{
			name: "workspace_mkdir",
			desc: "Create a directory (and parents) in the workspace.",
			params: map[string]*schema.ParameterInfo{
				"path": {Type: schema.String, Desc: "Relative directory path to create"},
			},
			handler: handleMkdir,
		},
	}
	for _, t := range tools {
		t.baseDir = baseDir
	}
	return tools
}

func handleWrite(baseDir string, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	abs, err := sanitizePath(baseDir, p.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("create parent dirs: %w", err)
	}
	if err := os.WriteFile(abs, []byte(p.Content), 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	out, _ := json.Marshal(map[string]any{
		"status":        "ok",
		"path":          p.Path,
		"bytes_written": len(p.Content),
	})
	return string(out), nil
}

func handleRead(baseDir string, args json.RawMessage) (string, error) {
	var p struct {
		Path     string `json:"path"`
		MaxLines int    `json:"max_lines"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	if p.MaxLines <= 0 {
		p.MaxLines = 200
	}
	abs, err := sanitizePath(baseDir, p.Path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	truncated := false
	for scanner.Scan() {
		if len(lines) >= p.MaxLines {
			truncated = true
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	out, _ := json.Marshal(map[string]any{
		"status":    "ok",
		"path":      p.Path,
		"content":   strings.Join(lines, "\n"),
		"lines":     len(lines),
		"truncated": truncated,
	})
	return string(out), nil
}

func handleList(baseDir string, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	if p.Path == "" {
		p.Path = "."
	}
	abs, err := sanitizePath(baseDir, p.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("read directory: %w", err)
	}
	type entry struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		IsDir bool   `json:"is_dir"`
	}
	result := make([]entry, 0, len(entries))
	for _, e := range entries {
		info, infoErr := e.Info()
		var size int64
		if infoErr == nil {
			size = info.Size()
		}
		result = append(result, entry{
			Name:  e.Name(),
			Size:  size,
			IsDir: e.IsDir(),
		})
	}
	out, _ := json.Marshal(map[string]any{
		"status":  "ok",
		"path":    p.Path,
		"entries": result,
	})
	return string(out), nil
}

func handleAppend(baseDir string, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	abs, err := sanitizePath(baseDir, p.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("create parent dirs: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("open file for append: %w", err)
	}
	defer f.Close()
	n, err := f.WriteString(p.Content)
	if err != nil {
		return "", fmt.Errorf("append to file: %w", err)
	}
	out, _ := json.Marshal(map[string]any{
		"status":        "ok",
		"path":          p.Path,
		"bytes_written": n,
	})
	return string(out), nil
}

func handleDelete(baseDir string, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	abs, err := sanitizePath(baseDir, p.Path)
	if err != nil {
		return "", err
	}
	if err := os.Remove(abs); err != nil {
		return "", fmt.Errorf("delete file: %w", err)
	}
	out, _ := json.Marshal(map[string]any{
		"status":  "ok",
		"path":    p.Path,
		"deleted": true,
	})
	return string(out), nil
}

func handleMkdir(baseDir string, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	abs, err := sanitizePath(baseDir, p.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	out, _ := json.Marshal(map[string]any{
		"status":  "ok",
		"path":    p.Path,
		"created": true,
	})
	return string(out), nil
}
