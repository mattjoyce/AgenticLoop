package localtools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
			name: "workspace_edit",
			desc: "Edit an existing file using either a single-match regex replacement or a line-range replacement. Preview by default; apply requires explicit confirmation hash.",
			params: map[string]*schema.ParameterInfo{
				"path":                     {Type: schema.String, Desc: "Relative path within the workspace"},
				"mode":                     {Type: schema.String, Desc: "Edit mode: regex_replace or line_replace"},
				"search":                   {Type: schema.String, Desc: "Regex pattern for regex_replace mode; must match exactly once"},
				"replace":                  {Type: schema.String, Desc: "Replacement content"},
				"start_line":               {Type: schema.Integer, Desc: "1-based start line for line_replace mode"},
				"end_line":                 {Type: schema.Integer, Desc: "1-based end line for line_replace mode (inclusive)"},
				"apply":                    {Type: schema.Boolean, Desc: "Whether to apply edit (defaults to false for preview)"},
				"expected_original_sha256": {Type: schema.String, Desc: "Required when apply=true; must match preview original_sha256"},
			},
			handler: handleEdit,
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

func handleEdit(baseDir string, args json.RawMessage) (string, error) {
	var p struct {
		Path                 string `json:"path"`
		Mode                 string `json:"mode"`
		Search               string `json:"search"`
		Replace              string `json:"replace"`
		StartLine            int    `json:"start_line"`
		EndLine              int    `json:"end_line"`
		Apply                bool   `json:"apply"`
		ExpectedOriginalHash string `json:"expected_original_sha256"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}

	abs, err := sanitizePath(baseDir, p.Path)
	if err != nil {
		return "", err
	}

	originalBytes, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	original := string(originalBytes)
	originalHash := sha256Hex(original)

	mode := strings.TrimSpace(p.Mode)
	if mode == "" {
		mode = "regex_replace"
	}

	var (
		edited     string
		matchCount int
	)
	switch mode {
	case "regex_replace":
		if p.Search == "" {
			return "", fmt.Errorf("search is required for regex_replace mode")
		}
		re, err := regexp.Compile(p.Search)
		if err != nil {
			return "", fmt.Errorf("compile regex: %w", err)
		}
		matches := re.FindAllStringIndex(original, -1)
		matchCount = len(matches)
		if matchCount != 1 {
			return "", fmt.Errorf("regex must match exactly once; got %d matches", matchCount)
		}
		edited = re.ReplaceAllString(original, p.Replace)
	case "line_replace":
		if p.StartLine <= 0 {
			return "", fmt.Errorf("start_line must be >= 1 for line_replace mode")
		}
		if p.EndLine == 0 {
			p.EndLine = p.StartLine
		}
		if p.EndLine < p.StartLine {
			return "", fmt.Errorf("end_line must be >= start_line")
		}
		lineRanges := computeLineRanges(original)
		if len(lineRanges) == 0 {
			return "", fmt.Errorf("line_replace requires a non-empty file")
		}
		if p.EndLine > len(lineRanges) {
			return "", fmt.Errorf("line range %d-%d exceeds file length %d", p.StartLine, p.EndLine, len(lineRanges))
		}
		startByte := lineRanges[p.StartLine-1].start
		endByte := lineRanges[p.EndLine-1].end
		edited = original[:startByte] + p.Replace + original[endByte:]
	default:
		return "", fmt.Errorf("unknown mode %q; expected regex_replace or line_replace", mode)
	}

	changed := edited != original
	proposedHash := sha256Hex(edited)
	resp := map[string]any{
		"status":          "ok",
		"path":            p.Path,
		"mode":            mode,
		"changed":         changed,
		"no_change":       !changed,
		"apply_requested": p.Apply,
		"applied":         false,
		"bytes_before":    len(original),
		"bytes_after":     len(edited),
		"original_sha256": originalHash,
		"proposed_sha256": proposedHash,
		"diff_preview":    buildDiffPreview(original, edited),
	}
	if mode == "regex_replace" {
		resp["match_count"] = matchCount
	}

	if !p.Apply || !changed {
		out, _ := json.Marshal(resp)
		return string(out), nil
	}
	if p.ExpectedOriginalHash == "" {
		return "", fmt.Errorf("expected_original_sha256 is required when apply=true")
	}
	if p.ExpectedOriginalHash != originalHash {
		return "", fmt.Errorf("expected_original_sha256 mismatch")
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if err := atomicWriteFile(abs, []byte(edited), info.Mode().Perm()); err != nil {
		return "", err
	}
	resp["applied"] = true

	out, _ := json.Marshal(resp)
	return string(out), nil
}

type lineRange struct {
	start int
	end   int
}

func computeLineRanges(content string) []lineRange {
	if content == "" {
		return nil
	}
	ranges := make([]lineRange, 0, strings.Count(content, "\n")+1)
	lineStart := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			ranges = append(ranges, lineRange{start: lineStart, end: i + 1})
			lineStart = i + 1
		}
	}
	if lineStart < len(content) {
		ranges = append(ranges, lineRange{start: lineStart, end: len(content)})
	}
	return ranges
}

func buildDiffPreview(before, after string) map[string]any {
	if before == after {
		return map[string]any{
			"line_start":      0,
			"before_line_end": 0,
			"after_line_end":  0,
			"before_excerpt":  "",
			"after_excerpt":   "",
		}
	}

	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")

	firstDiff := 0
	for firstDiff < len(beforeLines) && firstDiff < len(afterLines) && beforeLines[firstDiff] == afterLines[firstDiff] {
		firstDiff++
	}

	beforeEnd := len(beforeLines) - 1
	afterEnd := len(afterLines) - 1
	for beforeEnd >= firstDiff && afterEnd >= firstDiff && beforeLines[beforeEnd] == afterLines[afterEnd] {
		beforeEnd--
		afterEnd--
	}
	if beforeEnd < firstDiff {
		beforeEnd = firstDiff
	}
	if afterEnd < firstDiff {
		afterEnd = firstDiff
	}

	beforeExcerpt := strings.Join(beforeLines[firstDiff:beforeEnd+1], "\n")
	afterExcerpt := strings.Join(afterLines[firstDiff:afterEnd+1], "\n")

	return map[string]any{
		"line_start":      firstDiff + 1,
		"before_line_end": beforeEnd + 1,
		"after_line_end":  afterEnd + 1,
		"before_excerpt":  clipPreview(beforeExcerpt, 800),
		"after_excerpt":   clipPreview(afterExcerpt, 800),
	}
}

func clipPreview(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".workspace_edit_*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace file atomically: %w", err)
	}
	return nil
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
