package ductile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// DuctileTool wraps a Ductile plugin/command as an Eino InvokableTool.
type DuctileTool struct {
	client  *Client
	plugin  string
	command string
}

var _ tool.InvokableTool = (*DuctileTool)(nil)

// Info returns the tool metadata for LLM intent recognition.
func (t *DuctileTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	name := fmt.Sprintf("ductile_%s_%s", t.plugin, t.command)
	desc := fmt.Sprintf("Execute Ductile plugin '%s' command '%s'. Sends a payload to the Ductile gateway and returns the result.", t.plugin, t.command)
	return &schema.ToolInfo{
		Name: name,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"payload": {
				Type: schema.Object,
				Desc: "JSON payload to send to the plugin command",
			},
		}),
	}, nil
}

// InvokableRun triggers the Ductile plugin and polls for the result.
func (t *DuctileTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Payload json.RawMessage `json:"payload"`
	}
	if argumentsInJSON != "" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", fmt.Errorf("parse tool arguments: %w", err)
		}
	}

	jobID, err := t.client.Trigger(ctx, t.plugin, t.command, args.Payload)
	if err != nil {
		return "", fmt.Errorf("trigger %s/%s: %w", t.plugin, t.command, err)
	}

	result, err := t.client.PollJob(ctx, jobID, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("poll job %s: %w", jobID, err)
	}

	if result.Status != "succeeded" {
		return fmt.Sprintf(`{"status":"%s","job_id":"%s","error":"job did not succeed"}`, result.Status, jobID), nil
	}

	out, _ := json.Marshal(map[string]any{
		"status": result.Status,
		"job_id": jobID,
		"result": result.Result,
	})
	return string(out), nil
}

// BuildTools creates Eino tools from the Ductile allowlist.
// Each entry is "plugin/command" (e.g. "echo/poll").
func BuildTools(client *Client, allowlist []string) []tool.BaseTool {
	var tools []tool.BaseTool
	for _, entry := range allowlist {
		parts := strings.SplitN(entry, "/", 2)
		if len(parts) != 2 {
			continue
		}
		tools = append(tools, &DuctileTool{
			client:  client,
			plugin:  parts[0],
			command: parts[1],
		})
	}
	return tools
}
