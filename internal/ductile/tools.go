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

// ToolCallObserver is called after each tool invocation with the tool name, input, output, and status.
type ToolCallObserver func(tool, input, output, status string)

// DuctileTool wraps a Ductile plugin/command as an Eino InvokableTool.
type DuctileTool struct {
	client   *Client
	plugin   string
	command  string
	observer ToolCallObserver
}

var _ tool.InvokableTool = (*DuctileTool)(nil)

// Info returns the tool metadata for LLM intent recognition.
// It fetches the plugin's input schema from the Ductile discovery endpoint
// so the LLM receives typed parameters rather than a generic payload object.
func (t *DuctileTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	name := fmt.Sprintf("ductile_%s_%s", t.plugin, t.command)

	detail, err := t.client.GetPluginDetail(ctx, t.plugin)
	if err == nil {
		for _, cmd := range detail.Commands {
			if cmd.Name != t.command {
				continue
			}
			desc := cmd.Description
			if desc == "" {
				desc = fmt.Sprintf("Execute Ductile plugin '%s' command '%s'.", t.plugin, t.command)
			}
			params := jsonSchemaToParams(cmd.InputSchema)
			if params != nil {
				return &schema.ToolInfo{
					Name:        name,
					Desc:        desc,
					ParamsOneOf: schema.NewParamsOneOfByParams(params),
				}, nil
			}
			return &schema.ToolInfo{Name: name, Desc: desc}, nil
		}
	}

	// Fallback when discovery is unavailable or command not found.
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

// jsonSchemaToParams converts a JSON Schema object (from Ductile plugin discovery)
// into Eino ParameterInfo, which the LLM uses to understand what fields to supply.
func jsonSchemaToParams(s map[string]any) map[string]*schema.ParameterInfo {
	if s == nil {
		return nil
	}
	props, _ := s["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	required := schemaRequiredSet(s)
	out := make(map[string]*schema.ParameterInfo, len(props))
	for k, v := range props {
		prop, ok := v.(map[string]any)
		if !ok {
			continue
		}
		out[k] = jsonSchemaPropToParam(prop, required[k])
	}
	return out
}

func jsonSchemaPropToParam(prop map[string]any, required bool) *schema.ParameterInfo {
	typeStr, _ := prop["type"].(string)
	desc, _ := prop["description"].(string)
	info := &schema.ParameterInfo{
		Type:     jsonSchemaDataType(typeStr),
		Desc:     desc,
		Required: required,
	}
	if typeStr == "object" {
		if subProps, ok := prop["properties"].(map[string]any); ok {
			subReq := schemaRequiredSet(prop)
			info.SubParams = make(map[string]*schema.ParameterInfo, len(subProps))
			for k, v := range subProps {
				sp, ok := v.(map[string]any)
				if ok {
					info.SubParams[k] = jsonSchemaPropToParam(sp, subReq[k])
				}
			}
		}
	}
	if typeStr == "array" {
		if items, ok := prop["items"].(map[string]any); ok {
			info.ElemInfo = jsonSchemaPropToParam(items, false)
		}
	}
	return info
}

func schemaRequiredSet(s map[string]any) map[string]bool {
	out := map[string]bool{}
	req, _ := s["required"].([]any)
	for _, r := range req {
		if k, ok := r.(string); ok {
			out[k] = true
		}
	}
	return out
}

func jsonSchemaDataType(t string) schema.DataType {
	switch t {
	case "string":
		return schema.String
	case "integer":
		return schema.Integer
	case "number":
		return schema.Number
	case "boolean":
		return schema.Boolean
	case "array":
		return schema.Array
	case "object":
		return schema.Object
	default:
		return schema.String
	}
}

// InvokableRun triggers the Ductile plugin and polls for the result.
func (t *DuctileTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Payload json.RawMessage `json:"payload"`
	}
	var rawPayload json.RawMessage
	if argumentsInJSON != "" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", fmt.Errorf("parse tool arguments: %w", err)
		}
		if len(args.Payload) > 0 {
			// Explicit {"payload": {...}} wrapping — use inner object.
			rawPayload = args.Payload
		} else {
			// LLM passed fields directly (the common case); forward as-is.
			rawPayload = json.RawMessage(argumentsInJSON)
		}
	}

	jobID, err := t.client.Trigger(ctx, t.plugin, t.command, rawPayload)
	if err != nil {
		return "", fmt.Errorf("trigger %s/%s: %w", t.plugin, t.command, err)
	}

	result, err := t.client.PollJob(ctx, jobID, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("poll job %s: %w", jobID, err)
	}

	var out string
	if result.Status != "succeeded" {
		out = fmt.Sprintf(`{"status":"%s","job_id":"%s","error":"job did not succeed"}`, result.Status, jobID)
	} else {
		outBytes, _ := json.Marshal(map[string]any{
			"status": result.Status,
			"job_id": jobID,
			"result": result.Result,
		})
		out = string(outBytes)
	}

	if t.observer != nil {
		toolName := fmt.Sprintf("%s/%s", t.plugin, t.command)
		t.observer(toolName, argumentsInJSON, out, result.Status)
	}

	return out, nil
}

// WithObserver returns a copy of the tool with the given observer attached.
func (t *DuctileTool) WithObserver(obs ToolCallObserver) *DuctileTool {
	return &DuctileTool{
		client:   t.client,
		plugin:   t.plugin,
		command:  t.command,
		observer: obs,
	}
}

// BuildTools creates Eino tools from the Ductile allowlist.
// Each entry is "plugin/command" (e.g. "echo/poll").
// If observer is non-nil, it is called after each tool invocation.
func BuildTools(client *Client, allowlist []string, observer ToolCallObserver) []tool.BaseTool {
	var tools []tool.BaseTool
	for _, entry := range allowlist {
		parts := strings.SplitN(entry, "/", 2)
		if len(parts) != 2 {
			continue
		}
		tools = append(tools, &DuctileTool{
			client:   client,
			plugin:   parts[0],
			command:  parts[1],
			observer: observer,
		})
	}
	return tools
}
