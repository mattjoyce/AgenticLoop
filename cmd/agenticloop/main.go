package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mattjoyce/agenticloop/internal/agent"
	"github.com/mattjoyce/agenticloop/internal/api"
	"github.com/mattjoyce/agenticloop/internal/config"
	"github.com/mattjoyce/agenticloop/internal/ductile"
	"github.com/mattjoyce/agenticloop/internal/localtools"
	"github.com/mattjoyce/agenticloop/internal/provider"
	"github.com/mattjoyce/agenticloop/internal/storage"
	"github.com/mattjoyce/agenticloop/internal/store"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		if err := runStart(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "watch":
		if err := runWatch(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Printf("agenticloop %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: agenticloop <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  start     Start the AgenticLoop service")
	fmt.Fprintln(os.Stderr, "  watch     Watch a run event stream in a TUI")
	fmt.Fprintln(os.Stderr, "  version   Print version")
}

func runStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Setup logger
	logLevel := slog.LevelInfo
	switch cfg.Service.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	logger.Info("starting agenticloop", "version", version, "config", *configPath)

	// Open SQLite
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := storage.OpenSQLite(ctx, cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Create stores
	runStore := store.NewRunStore(db)
	stepStore := store.NewStepStore(db)

	// Create Ductile client
	dc := ductile.NewClient(cfg.Ductile.BaseURL, cfg.Ductile.Token, logger)

	// Create LLM provider
	chatModel, err := provider.NewChatModel(ctx, cfg.LLM)
	if err != nil {
		return fmt.Errorf("create llm provider: %w", err)
	}

	// Create tools from allowlist
	tools := ductile.BuildTools(dc, cfg.Ductile.Allowlist, nil)
	tools = append(tools, localtools.BuildDefaultTools()...)

	// Create agent runner
	runner := agent.NewRunner(runStore, stepStore, chatModel, tools, cfg.Agent, dc, cfg.Ductile.CallbackURL, logger)

	// Recover interrupted runs
	if err := runner.RecoverRuns(ctx); err != nil {
		logger.Error("run recovery failed", "error", err)
	}

	// Start runner worker
	go runner.Start(ctx)

	// Create and start API server
	srv := api.New(api.Config{
		Listen:                  cfg.API.Listen,
		Token:                   cfg.API.Token,
		StreamPollInterval:      cfg.API.StreamPollInterval,
		StreamHeartbeatInterval: cfg.API.StreamHeartbeatInterval,
	}, runStore, runner, logger)

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig)
		cancel()
		select {
		case <-runner.Done():
			logger.Info("runner stopped gracefully")
		case <-time.After(10 * time.Second):
			logger.Warn("runner did not stop within 10s, exiting anyway")
		}
		return nil
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			return err
		}
		return nil
	}
}
