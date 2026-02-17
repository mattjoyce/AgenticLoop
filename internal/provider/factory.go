package provider

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino-ext/components/model/openai"

	"github.com/mattjoyce/agenticloop/internal/config"
)

// NewChatModel creates an Eino ChatModel from config.
func NewChatModel(ctx context.Context, cfg config.LLMConfig) (model.ToolCallingChatModel, error) {
	switch cfg.Provider {
	case "anthropic":
		return newAnthropicModel(ctx, cfg)
	case "openai":
		return newOpenAIModel(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported llm provider: %q (supported: anthropic, openai)", cfg.Provider)
	}
}

func newAnthropicModel(ctx context.Context, cfg config.LLMConfig) (model.ToolCallingChatModel, error) {
	claudeCfg := &claude.Config{
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		MaxTokens: 4096,
	}
	if cfg.BaseURL != "" {
		claudeCfg.BaseURL = &cfg.BaseURL
	}

	m, err := claude.NewChatModel(ctx, claudeCfg)
	if err != nil {
		return nil, fmt.Errorf("create anthropic model: %w", err)
	}
	return m, nil
}

func newOpenAIModel(ctx context.Context, cfg config.LLMConfig) (model.ToolCallingChatModel, error) {
	openAICfg := &openai.ChatModelConfig{
		APIKey: cfg.APIKey,
		Model:  cfg.Model,
	}
	if cfg.BaseURL != "" {
		openAICfg.BaseURL = cfg.BaseURL
	}

	m, err := openai.NewChatModel(ctx, openAICfg)
	if err != nil {
		return nil, fmt.Errorf("create openai model: %w", err)
	}
	return m, nil
}
