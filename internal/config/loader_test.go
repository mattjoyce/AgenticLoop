package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyDefaultsSetsOperationalIntervals(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	if cfg.API.StreamPollInterval != 700*time.Millisecond {
		t.Fatalf("stream poll default = %v, want %v", cfg.API.StreamPollInterval, 700*time.Millisecond)
	}
	if cfg.API.StreamHeartbeatInterval != 15*time.Second {
		t.Fatalf("stream heartbeat default = %v, want %v", cfg.API.StreamHeartbeatInterval, 15*time.Second)
	}
	if cfg.LLM.MaxTokens != 4096 {
		t.Fatalf("llm.max_tokens default = %d, want 4096", cfg.LLM.MaxTokens)
	}
}

func TestValidateRejectsNonPositiveIntervals(t *testing.T) {
	cfg := validTestConfig()
	cfg.API.StreamPollInterval = 0
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "api.stream_poll_interval") {
		t.Fatalf("expected stream_poll_interval validation error, got %v", err)
	}

	cfg = validTestConfig()
	cfg.API.StreamHeartbeatInterval = -1 * time.Second
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "api.stream_heartbeat_interval") {
		t.Fatalf("expected stream_heartbeat_interval validation error, got %v", err)
	}

	cfg = validTestConfig()
	cfg.LLM.MaxTokens = 0
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "llm.max_tokens") {
		t.Fatalf("expected llm.max_tokens validation error, got %v", err)
	}
}

func TestConfigTemplateUsesDynamicToolCatalog(t *testing.T) {
	data, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "{{.AvailableTools}}") {
		t.Fatalf("expected config template to include {{.AvailableTools}}")
	}
	if strings.Contains(text, "<tool>workspace_write") {
		t.Fatalf("expected hardcoded workspace tool entries to be removed from act prompt template")
	}
}

func validTestConfig() *Config {
	return &Config{
		Service: ServiceConfig{
			LogLevel: "info",
		},
		API: APIConfig{
			Token:                   "token",
			StreamPollInterval:      700 * time.Millisecond,
			StreamHeartbeatInterval: 15 * time.Second,
		},
		Ductile: DuctileConfig{
			BaseURL: "http://127.0.0.1:8080",
		},
		LLM: LLMConfig{
			Provider:  "openai",
			APIKey:    "key",
			MaxTokens: 4096,
		},
		Agent: AgentConfig{
			DefaultMaxLoops: 1,
			DefaultDeadline: time.Minute,
			StepTimeout:     time.Second,
			QueueCapacity:   1,
			EnqueueTimeout:  time.Second,
			Prompts: AgentPrompts{
				Frame:   "frame",
				Plan:    "plan",
				Act:     "act",
				Reflect: "reflect",
			},
		},
	}
}
