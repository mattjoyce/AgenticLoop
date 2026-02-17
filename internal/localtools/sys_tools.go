package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Observer is called after each tool invocation.
type Observer func(tool, input, output, status string)

// CommandTool executes a fixed local command as an Eino tool.
type CommandTool struct {
	name        string
	description string
	runner      func(ctx context.Context) (string, string, error)
	observer    Observer
}

var _ tool.InvokableTool = (*CommandTool)(nil)

// BuildDefaultTools returns the built-in local diagnostic tools.
func BuildDefaultTools() []tool.BaseTool {
	return []tool.BaseTool{
		&CommandTool{
			name:        "sys_internal_ip",
			description: "Get internal network interfaces and IP addresses from this host.",
			runner:      runInternalIP,
		},
		&CommandTool{
			name:        "sys_external_ip",
			description: "Get external/public IP info via curl ifconfig.me/all.json.",
			runner:      runExternalIP,
		},
	}
}

// WithObserver returns a copy of the tool with the given observer attached.
func (t *CommandTool) WithObserver(obs Observer) *CommandTool {
	return &CommandTool{
		name:        t.name,
		description: t.description,
		runner:      t.runner,
		observer:    obs,
	}
}

// Info returns tool metadata for model planning.
func (t *CommandTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.name,
		Desc:        t.description,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun executes the fixed command and returns JSON output.
func (t *CommandTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	command, output, err := t.runner(ctx)
	status := "ok"
	resp := map[string]any{
		"status":  status,
		"command": command,
		"output":  output,
	}

	if err != nil {
		status = "error"
		resp["status"] = status
		resp["error"] = err.Error()
	}

	out, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		return "", fmt.Errorf("marshal tool output: %w", marshalErr)
	}

	if t.observer != nil {
		t.observer(t.name, argumentsInJSON, string(out), status)
	}

	return string(out), nil
}

func runInternalIP(ctx context.Context) (string, string, error) {
	out, err := runCommand(ctx, "ip", "addr")
	if err == nil {
		return "ip addr", out, nil
	}
	if !isCmdNotFound(err) {
		return "ip addr", out, err
	}

	out, err = runCommand(ctx, "ifconfig")
	if err != nil {
		return "ifconfig", out, err
	}
	return "ifconfig", out, nil
}

func runExternalIP(ctx context.Context) (string, string, error) {
	out, err := runCommand(ctx, "curl", "-sS", "ifconfig.me/all.json")
	if err != nil {
		return "curl -sS ifconfig.me/all.json", out, err
	}
	return "curl -sS ifconfig.me/all.json", out, nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func isCmdNotFound(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	return false
}
