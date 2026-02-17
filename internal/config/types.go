package config

import "time"

// Config represents the complete agenticloop configuration.
type Config struct {
	Service  ServiceConfig  `yaml:"service"`
	Database DatabaseConfig `yaml:"database"`
	API      APIConfig      `yaml:"api"`
	Ductile  DuctileConfig  `yaml:"ductile"`
	LLM      LLMConfig      `yaml:"llm"`
	Agent    AgentConfig    `yaml:"agent"`
}

// ServiceConfig defines core service settings.
type ServiceConfig struct {
	Name     string `yaml:"name"`
	LogLevel string `yaml:"log_level"`
}

// DatabaseConfig defines SQLite storage settings.
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// APIConfig defines HTTP API server settings.
type APIConfig struct {
	Listen string `yaml:"listen"`
	Token  string `yaml:"token"`
}

// DuctileConfig defines the connection to the Ductile gateway.
type DuctileConfig struct {
	BaseURL   string   `yaml:"base_url"`
	Token     string   `yaml:"token"`
	Allowlist []string `yaml:"allowlist"`
}

// LLMConfig defines the LLM provider settings.
type LLMConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url,omitempty"`
}

// AgentConfig defines default agent behavior.
type AgentConfig struct {
	DefaultMaxLoops  int           `yaml:"default_max_loops"`
	DefaultDeadline  time.Duration `yaml:"default_deadline"`
	StepTimeout      time.Duration `yaml:"step_timeout"`
	MaxRetryPerStep  int           `yaml:"max_retry_per_step"`
}
