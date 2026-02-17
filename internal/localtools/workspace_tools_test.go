package localtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
