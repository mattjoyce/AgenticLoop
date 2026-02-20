package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizePath(t *testing.T) {
	base := t.TempDir()

	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"simple file", "foo.txt", false},
		{"nested", "a/b/c.txt", false},
		{"dot path", ".", false},
		{"empty", "", true},
		{"absolute", "/etc/passwd", true},
		{"escape", "../outside", true},
		{"sneaky escape", "a/../../outside", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizePath(base, tt.rel)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got path %q", tt.rel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !filepath.IsAbs(got) {
				t.Fatalf("expected absolute path, got %q", got)
			}
		})
	}

	// Prefix-collision escape: /tmp/base_evil starts with /tmp/base as a string,
	// but it is outside the workspace and must be rejected.
	siblingName := filepath.Base(base) + "_evil"
	prefixCollision := filepath.Join("..", siblingName, "file.txt")
	if _, err := sanitizePath(base, prefixCollision); err == nil {
		t.Fatalf("expected prefix-collision path %q to be rejected", prefixCollision)
	}
}

func TestWorkspaceWriteAndRead(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	// Find write and read tools.
	var writeTool, readTool *WorkspaceFileTool
	for _, tt := range tools {
		switch tt.name {
		case "workspace_write":
			writeTool = tt
		case "workspace_read":
			readTool = tt
		}
	}

	// Write a file.
	args, _ := json.Marshal(map[string]any{"path": "sub/test.txt", "content": "hello\nworld"})
	out, err := writeTool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	var writeResp map[string]any
	json.Unmarshal([]byte(out), &writeResp)
	if writeResp["status"] != "ok" {
		t.Fatalf("write status: %v", writeResp)
	}
	if int(writeResp["bytes_written"].(float64)) != 11 {
		t.Fatalf("bytes_written: %v", writeResp["bytes_written"])
	}

	// Read it back.
	args, _ = json.Marshal(map[string]any{"path": "sub/test.txt"})
	out, err = readTool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	var readResp map[string]any
	json.Unmarshal([]byte(out), &readResp)
	if readResp["content"] != "hello\nworld" {
		t.Fatalf("content mismatch: %v", readResp["content"])
	}
	if readResp["truncated"] != false {
		t.Fatalf("unexpected truncation")
	}
}

func TestWorkspaceReadTruncation(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	var writeTool, readTool *WorkspaceFileTool
	for _, tt := range tools {
		switch tt.name {
		case "workspace_write":
			writeTool = tt
		case "workspace_read":
			readTool = tt
		}
	}

	// Write 10 lines.
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	args, _ := json.Marshal(map[string]any{"path": "big.txt", "content": content})
	writeTool.InvokableRun(ctx, string(args))

	// Read with max_lines=3.
	args, _ = json.Marshal(map[string]any{"path": "big.txt", "max_lines": 3})
	out, _ := readTool.InvokableRun(ctx, string(args))
	var resp map[string]any
	json.Unmarshal([]byte(out), &resp)
	if int(resp["lines"].(float64)) != 3 {
		t.Fatalf("expected 3 lines, got %v", resp["lines"])
	}
	if resp["truncated"] != true {
		t.Fatalf("expected truncated=true")
	}
}

func TestWorkspaceList(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	// Create some files.
	os.MkdirAll(filepath.Join(base, "subdir"), 0o755)
	os.WriteFile(filepath.Join(base, "a.txt"), []byte("hello"), 0o644)

	var listTool *WorkspaceFileTool
	for _, tt := range tools {
		if tt.name == "workspace_list" {
			listTool = tt
		}
	}

	args, _ := json.Marshal(map[string]any{})
	out, err := listTool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	var resp struct {
		Status  string `json:"status"`
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
		} `json:"entries"`
	}
	json.Unmarshal([]byte(out), &resp)
	if len(resp.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.Entries))
	}
}

func TestWorkspaceAppend(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	var appendTool, readTool *WorkspaceFileTool
	for _, tt := range tools {
		switch tt.name {
		case "workspace_append":
			appendTool = tt
		case "workspace_read":
			readTool = tt
		}
	}

	// Append to non-existent file (creates it).
	args, _ := json.Marshal(map[string]any{"path": "log.txt", "content": "first\n"})
	appendTool.InvokableRun(ctx, string(args))

	// Append more.
	args, _ = json.Marshal(map[string]any{"path": "log.txt", "content": "second\n"})
	appendTool.InvokableRun(ctx, string(args))

	// Read.
	args, _ = json.Marshal(map[string]any{"path": "log.txt"})
	out, _ := readTool.InvokableRun(ctx, string(args))
	var resp map[string]any
	json.Unmarshal([]byte(out), &resp)
	if resp["content"] != "first\nsecond" {
		t.Fatalf("unexpected content: %q", resp["content"])
	}
}

func TestWorkspaceEditRegexPreviewThenApply(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "doc.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	editTool := findWorkspaceTool(t, BuildWorkspaceTools(base), "workspace_edit")
	ctx := context.Background()

	previewArgs, _ := json.Marshal(map[string]any{
		"path":    "doc.txt",
		"mode":    "regex_replace",
		"search":  "beta",
		"replace": "gamma",
	})
	previewOut, err := editTool.InvokableRun(ctx, string(previewArgs))
	if err != nil {
		t.Fatalf("preview error: %v", err)
	}
	var previewResp struct {
		Status       string `json:"status"`
		Changed      bool   `json:"changed"`
		Applied      bool   `json:"applied"`
		MatchCount   int    `json:"match_count"`
		OriginalHash string `json:"original_sha256"`
	}
	if err := json.Unmarshal([]byte(previewOut), &previewResp); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if previewResp.Status != "ok" || !previewResp.Changed || previewResp.Applied || previewResp.MatchCount != 1 {
		t.Fatalf("unexpected preview response: %+v", previewResp)
	}

	unchanged, _ := os.ReadFile(filepath.Join(base, "doc.txt"))
	if string(unchanged) != "alpha\nbeta\n" {
		t.Fatalf("preview should not change file, got %q", string(unchanged))
	}

	applyArgs, _ := json.Marshal(map[string]any{
		"path":                     "doc.txt",
		"mode":                     "regex_replace",
		"search":                   "beta",
		"replace":                  "gamma",
		"apply":                    true,
		"expected_original_sha256": previewResp.OriginalHash,
	})
	applyOut, err := editTool.InvokableRun(ctx, string(applyArgs))
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	var applyResp struct {
		Status  string `json:"status"`
		Changed bool   `json:"changed"`
		Applied bool   `json:"applied"`
	}
	if err := json.Unmarshal([]byte(applyOut), &applyResp); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}
	if applyResp.Status != "ok" || !applyResp.Changed || !applyResp.Applied {
		t.Fatalf("unexpected apply response: %+v", applyResp)
	}

	updated, _ := os.ReadFile(filepath.Join(base, "doc.txt"))
	if string(updated) != "alpha\ngamma\n" {
		t.Fatalf("unexpected file after apply: %q", string(updated))
	}
}

func TestWorkspaceEditRegexRequiresSingleMatch(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "doc.txt"), []byte("dup dup\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	editTool := findWorkspaceTool(t, BuildWorkspaceTools(base), "workspace_edit")

	args, _ := json.Marshal(map[string]any{
		"path":    "doc.txt",
		"mode":    "regex_replace",
		"search":  "dup",
		"replace": "one",
	})
	out, err := editTool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	var resp struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "error" || resp.Error == "" {
		t.Fatalf("expected tool error response, got %+v", resp)
	}
}

func TestWorkspaceEditLineReplace(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "doc.txt"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	editTool := findWorkspaceTool(t, BuildWorkspaceTools(base), "workspace_edit")
	ctx := context.Background()

	previewArgs, _ := json.Marshal(map[string]any{
		"path":       "doc.txt",
		"mode":       "line_replace",
		"start_line": 2,
		"end_line":   3,
		"replace":    "x\ny\n",
	})
	previewOut, err := editTool.InvokableRun(ctx, string(previewArgs))
	if err != nil {
		t.Fatalf("preview error: %v", err)
	}
	var previewResp struct {
		Status       string `json:"status"`
		Changed      bool   `json:"changed"`
		OriginalHash string `json:"original_sha256"`
	}
	if err := json.Unmarshal([]byte(previewOut), &previewResp); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if previewResp.Status != "ok" || !previewResp.Changed {
		t.Fatalf("unexpected preview response: %+v", previewResp)
	}

	applyArgs, _ := json.Marshal(map[string]any{
		"path":                     "doc.txt",
		"mode":                     "line_replace",
		"start_line":               2,
		"end_line":                 3,
		"replace":                  "x\ny\n",
		"apply":                    true,
		"expected_original_sha256": previewResp.OriginalHash,
	})
	applyOut, err := editTool.InvokableRun(ctx, string(applyArgs))
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	var applyResp struct {
		Status  string `json:"status"`
		Applied bool   `json:"applied"`
	}
	if err := json.Unmarshal([]byte(applyOut), &applyResp); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}
	if applyResp.Status != "ok" || !applyResp.Applied {
		t.Fatalf("unexpected apply response: %+v", applyResp)
	}

	updated, _ := os.ReadFile(filepath.Join(base, "doc.txt"))
	if string(updated) != "a\nx\ny\n" {
		t.Fatalf("unexpected file after line replace: %q", string(updated))
	}
}

func TestWorkspaceEditNoChange(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "doc.txt"), []byte("unchanged\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	editTool := findWorkspaceTool(t, BuildWorkspaceTools(base), "workspace_edit")

	args, _ := json.Marshal(map[string]any{
		"path":    "doc.txt",
		"mode":    "regex_replace",
		"search":  "unchanged",
		"replace": "unchanged",
		"apply":   true,
	})
	out, err := editTool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	var resp struct {
		Status   string `json:"status"`
		Changed  bool   `json:"changed"`
		NoChange bool   `json:"no_change"`
		Applied  bool   `json:"applied"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" || resp.Changed || !resp.NoChange || resp.Applied {
		t.Fatalf("unexpected no-change response: %+v", resp)
	}
}

func TestWorkspaceEditApplyRequiresHash(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "doc.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	editTool := findWorkspaceTool(t, BuildWorkspaceTools(base), "workspace_edit")

	args, _ := json.Marshal(map[string]any{
		"path":    "doc.txt",
		"mode":    "regex_replace",
		"search":  "beta",
		"replace": "gamma",
		"apply":   true,
	})
	out, err := editTool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	var resp struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "error" || resp.Error == "" {
		t.Fatalf("expected error response, got %+v", resp)
	}
}

func TestWorkspaceDelete(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	var writeTool, deleteTool *WorkspaceFileTool
	for _, tt := range tools {
		switch tt.name {
		case "workspace_write":
			writeTool = tt
		case "workspace_delete":
			deleteTool = tt
		}
	}

	// Write then delete.
	args, _ := json.Marshal(map[string]any{"path": "temp.txt", "content": "x"})
	writeTool.InvokableRun(ctx, string(args))

	args, _ = json.Marshal(map[string]any{"path": "temp.txt"})
	out, err := deleteTool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	var resp map[string]any
	json.Unmarshal([]byte(out), &resp)
	if resp["status"] != "ok" {
		t.Fatalf("delete status: %v", resp)
	}

	// File should be gone.
	if _, err := os.Stat(filepath.Join(base, "temp.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after delete")
	}

	// Delete non-existent should return error in response (not Go error).
	args, _ = json.Marshal(map[string]any{"path": "nope.txt"})
	out, err = deleteTool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	json.Unmarshal([]byte(out), &resp)
	if resp["status"] != "error" {
		t.Fatalf("expected error status for missing file")
	}
}

func TestWorkspaceMkdir(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	var mkdirTool *WorkspaceFileTool
	for _, tt := range tools {
		if tt.name == "workspace_mkdir" {
			mkdirTool = tt
		}
	}

	args, _ := json.Marshal(map[string]any{"path": "a/b/c"})
	out, err := mkdirTool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("mkdir error: %v", err)
	}
	var resp map[string]any
	json.Unmarshal([]byte(out), &resp)
	if resp["status"] != "ok" {
		t.Fatalf("mkdir status: %v", resp)
	}

	info, err := os.Stat(filepath.Join(base, "a", "b", "c"))
	if err != nil || !info.IsDir() {
		t.Fatalf("directory should exist")
	}
}

func TestWorkspacePathEscape(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	// All tools should reject path escape attempts.
	for _, tt := range tools {
		args, _ := json.Marshal(map[string]any{"path": "../escape", "content": "bad"})
		out, err := tt.InvokableRun(ctx, string(args))
		if err != nil {
			t.Fatalf("%s: unexpected Go error: %v", tt.name, err)
		}
		var resp map[string]any
		json.Unmarshal([]byte(out), &resp)
		if resp["status"] != "error" {
			t.Fatalf("%s: expected error for path escape, got %v", tt.name, resp)
		}
	}
}

func TestWorkspaceSymlinkEscapeRejected(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()

	seedPath := filepath.Join(outside, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	linkPath := filepath.Join(base, "linkout")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink setup unsupported on this environment: %v", err)
	}

	tools := BuildWorkspaceTools(base)
	ctx := context.Background()

	for _, tt := range tools {
		args := symlinkEscapeArgs(tt.name)
		payload, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("%s: marshal args: %v", tt.name, err)
		}

		out, err := tt.InvokableRun(ctx, string(payload))
		if err != nil {
			t.Fatalf("%s: unexpected Go error: %v", tt.name, err)
		}

		var resp map[string]any
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("%s: decode response: %v", tt.name, err)
		}
		if resp["status"] != "error" {
			t.Fatalf("%s: expected error response, got %v", tt.name, resp)
		}
		errText := fmt.Sprint(resp["error"])
		if !strings.Contains(errText, "escapes workspace directory") {
			t.Fatalf("%s: expected workspace escape error, got %q", tt.name, errText)
		}
	}
}

func TestWorkspaceObserver(t *testing.T) {
	base := t.TempDir()
	tools := BuildWorkspaceTools(base)

	var called bool
	obs := func(toolName, input, output, status string) {
		called = true
		if toolName != "workspace_mkdir" {
			t.Fatalf("expected workspace_mkdir, got %s", toolName)
		}
		if status != "ok" {
			t.Fatalf("expected ok status, got %s", status)
		}
	}

	var mkdirTool *WorkspaceFileTool
	for _, tt := range tools {
		if tt.name == "workspace_mkdir" {
			mkdirTool = tt.WithObserver(obs)
		}
	}

	args, _ := json.Marshal(map[string]any{"path": "observed"})
	mkdirTool.InvokableRun(context.Background(), string(args))
	if !called {
		t.Fatalf("observer was not called")
	}
}

func findWorkspaceTool(t *testing.T, tools []*WorkspaceFileTool, name string) *WorkspaceFileTool {
	t.Helper()
	for _, tt := range tools {
		if tt.name == name {
			return tt
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func symlinkEscapeArgs(toolName string) map[string]any {
	switch toolName {
	case "workspace_write":
		return map[string]any{"path": "linkout/new.txt", "content": "x"}
	case "workspace_read":
		return map[string]any{"path": "linkout/seed.txt"}
	case "workspace_list":
		return map[string]any{"path": "linkout"}
	case "workspace_append":
		return map[string]any{"path": "linkout/new.txt", "content": "x"}
	case "workspace_edit":
		return map[string]any{
			"path":    "linkout/seed.txt",
			"mode":    "regex_replace",
			"search":  "seed",
			"replace": "x",
		}
	case "workspace_delete":
		return map[string]any{"path": "linkout/seed.txt"}
	case "workspace_mkdir":
		return map[string]any{"path": "linkout/newdir"}
	default:
		return map[string]any{"path": "linkout"}
	}
}
