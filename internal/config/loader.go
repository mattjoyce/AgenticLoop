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
	if cfg.LLM.APIKey == "" {
		return fmt.Errorf("llm.api_key is required")
	}
	if envVarPattern.MatchString(cfg.LLM.APIKey) {
		matches := envVarPattern.FindStringSubmatch(cfg.LLM.APIKey)
		if len(matches) > 1 {
			return fmt.Errorf("llm.api_key: environment variable ${%s} is not set", matches[1])
		}
	}
	if cfg.Ductile.BaseURL == "" {
		return fmt.Errorf("ductile.base_url is required")
	}
	if cfg.Agent.DefaultMaxLoops <= 0 {
		return fmt.Errorf("agent.default_max_loops must be positive")
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
