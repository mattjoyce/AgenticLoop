package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads and parses configuration from a YAML file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	interpolated := interpolateEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(interpolated), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Service.Name == "" {
		cfg.Service.Name = "agenticloop"
	}
	if cfg.Service.LogLevel == "" {
		cfg.Service.LogLevel = "info"
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "./data/agenticloop.db"
	}
	if cfg.API.Listen == "" {
		cfg.API.Listen = "127.0.0.1:8090"
	}
	if cfg.Agent.DefaultMaxLoops == 0 {
		cfg.Agent.DefaultMaxLoops = 10
	}
	if cfg.Agent.DefaultDeadline == 0 {
		cfg.Agent.DefaultDeadline = 5 * time.Minute
	}
	if cfg.Agent.StepTimeout == 0 {
		cfg.Agent.StepTimeout = 60 * time.Second
	}
	if cfg.Agent.MaxRetryPerStep == 0 {
		cfg.Agent.MaxRetryPerStep = 3
	}
	if cfg.Agent.MaxActRounds == 0 {
		cfg.Agent.MaxActRounds = 6
	}
	if cfg.Agent.WorkspaceDir == "" {
		cfg.Agent.WorkspaceDir = "./data/workspaces"
	}
	if cfg.Agent.Prompts.Frame == "" {
		cfg.Agent.Prompts.Frame = `<stage name="frame">
<role>You are in the FRAME stage for an autonomous run.</role>
<run_context version="1">
<goal source="run.goal">{{.Goal}}</goal>
<static_context source="run.context">{{.Context}}</static_context>
<constraints source="run.constraints">{{.Constraints}}</constraints>
<loop_state iteration="{{.Iteration}}" max_loops="{{.MaxLoops}}"></loop_state>
<next_focus source="stage.reflect">{{.NextFocus}}</next_focus>
<run_memory source="workspace.run_memory">{{.Memory}}</run_memory>
</run_context>
<output_contract format="markdown">
Return concise markdown with:
- objective
- known facts
- unknowns to resolve
- success condition
</output_contract>
</stage>`
	}
	if cfg.Agent.Prompts.Plan == "" {
		cfg.Agent.Prompts.Plan = `<stage name="plan">
<role>You are in the PLAN stage for an autonomous run.</role>
<run_context version="1">
<goal source="run.goal">{{.Goal}}</goal>
<static_context source="run.context">{{.Context}}</static_context>
<constraints source="run.constraints">{{.Constraints}}</constraints>
<loop_state iteration="{{.Iteration}}" max_loops="{{.MaxLoops}}"></loop_state>
<next_focus source="stage.reflect">{{.NextFocus}}</next_focus>
<run_memory source="workspace.run_memory">{{.Memory}}</run_memory>
</run_context>
<frame_output source="stage.frame">{{.Frame}}</frame_output>
<output_contract format="markdown">
Return a short numbered plan for this loop. Prefer one concrete next action.
</output_contract>
</stage>`
	}
	if cfg.Agent.Prompts.Act == "" {
		cfg.Agent.Prompts.Act = `<stage name="act">
<role>You are in the ACT stage for an autonomous run.</role>
<run_context version="1">
<goal source="run.goal">{{.Goal}}</goal>
<static_context source="run.context">{{.Context}}</static_context>
<constraints source="run.constraints">{{.Constraints}}</constraints>
<loop_state iteration="{{.Iteration}}" max_loops="{{.MaxLoops}}"></loop_state>
<run_memory source="workspace.run_memory">{{.Memory}}</run_memory>
</run_context>
<frame_output source="stage.frame">{{.Frame}}</frame_output>
<plan_output source="stage.plan">{{.Plan}}</plan_output>
<available_tools source="runtime.bound_tools">
<tool>sys_internal_ip</tool>
<tool>sys_external_ip</tool>
<tool>report_success</tool>
<note>Workspace file tools may be available at runtime: workspace_read/write/append/edit/delete/mkdir/list.</note>
<note>workspace_edit defaults to preview (apply=false); apply requires expected_original_sha256 from preview output.</note>
<note>Additional tools may be available at runtime (for example ductile_*).</note>
<note>Completion requires calling report_success with summary and evidence.</note>
</available_tools>
<output_contract format="markdown">
Execute the best next action. Use tools when needed.
When complete for this loop, provide a concise action result.
</output_contract>
</stage>`
	}
	if cfg.Agent.Prompts.Reflect == "" {
		cfg.Agent.Prompts.Reflect = `<stage name="reflect">
<role>You are in the REFLECT stage for an autonomous run.</role>
<run_context version="1">
<goal source="run.goal">{{.Goal}}</goal>
<loop_state iteration="{{.Iteration}}" max_loops="{{.MaxLoops}}"></loop_state>
<run_memory source="workspace.run_memory">{{.Memory}}</run_memory>
<loop_memory source="workspace.loop_memory">{{.LoopMemory}}</loop_memory>
</run_context>
<frame_output source="stage.frame">{{.Frame}}</frame_output>
<plan_output source="stage.plan">{{.Plan}}</plan_output>
<act_output source="stage.act">{{.Act}}</act_output>
<completion_gate success_tool="report_success" success_tool_called="{{.SuccessReported}}">
<reported_summary>{{.SuccessSummary}}</reported_summary>
</completion_gate>
<output_contract format="json">
Return JSON only:
{"done": boolean, "summary": "string", "next_focus": "string", "memory_update": "string"}
</output_contract>
</stage>`
	}
}

func validate(cfg *Config) error {
	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[cfg.Service.LogLevel] {
		return fmt.Errorf("service.log_level must be one of: debug, info, warn, error (got %q)", cfg.Service.LogLevel)
	}
	if cfg.API.Token == "" {
		return fmt.Errorf("api.token is required")
	}
	if envVarPattern.MatchString(cfg.API.Token) {
		matches := envVarPattern.FindStringSubmatch(cfg.API.Token)
		if len(matches) > 1 {
			return fmt.Errorf("api.token: environment variable ${%s} is not set", matches[1])
		}
	}
	if cfg.LLM.Provider == "" {
		return fmt.Errorf("llm.provider is required")
	}
	// api_key required for anthropic/openai, not for ollama
	if cfg.LLM.Provider != "ollama" {
		if cfg.LLM.APIKey == "" {
			return fmt.Errorf("llm.api_key is required for provider %q", cfg.LLM.Provider)
		}
		if envVarPattern.MatchString(cfg.LLM.APIKey) {
			matches := envVarPattern.FindStringSubmatch(cfg.LLM.APIKey)
			if len(matches) > 1 {
				return fmt.Errorf("llm.api_key: environment variable ${%s} is not set", matches[1])
			}
		}
	}
	if cfg.Ductile.BaseURL == "" {
		return fmt.Errorf("ductile.base_url is required")
	}
	if cfg.Ductile.Token != "" && envVarPattern.MatchString(cfg.Ductile.Token) {
		matches := envVarPattern.FindStringSubmatch(cfg.Ductile.Token)
		if len(matches) > 1 {
			return fmt.Errorf("ductile.token: environment variable ${%s} is not set", matches[1])
		}
	}
	if cfg.Agent.DefaultMaxLoops <= 0 {
		return fmt.Errorf("agent.default_max_loops must be positive")
	}
	if cfg.Agent.DefaultDeadline <= 0 {
		return fmt.Errorf("agent.default_deadline must be positive")
	}
	if cfg.Agent.StepTimeout <= 0 {
		return fmt.Errorf("agent.step_timeout must be positive")
	}
	return nil
}

// interpolateEnv replaces ${VAR} with environment variable values.
func interpolateEnv(input string) string {
	return envVarPattern.ReplaceAllStringFunc(input, func(match string) string {
		varName := envVarPattern.FindStringSubmatch(match)[1]
		if value, exists := os.LookupEnv(varName); exists {
			return value
		}
		return match
	})
}
