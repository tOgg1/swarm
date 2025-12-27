// Package main is the entry point for the swarm-agent-runner binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/opencode-ai/swarm/internal/agent/runner"
	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/rs/zerolog"
)

// Version information (set by goreleaser)
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	workspaceID := flag.String("workspace", "", "workspace id (required)")
	agentID := flag.String("agent", "", "agent id (required)")
	eventSocket := flag.String("event-socket", "", "unix socket path for runner events")
	promptRegex := flag.String("prompt-regex", "", "regex to detect prompt readiness")
	busyRegex := flag.String("busy-regex", "", "regex to detect busy output")
	heartbeat := flag.Duration("heartbeat", 5*time.Second, "heartbeat interval")
	tailLines := flag.Int("tail-lines", 50, "output lines included in heartbeat")
	dbPath := flag.String("db-path", "", "database path (defaults to config)")
	configFile := flag.String("config", "", "config file (default is $HOME/.config/swarm/config.yaml)")
	logLevel := flag.String("log-level", "", "override logging level (debug, info, warn, error)")
	logFormat := flag.String("log-format", "", "override logging format (json, console)")
	flag.Parse()

	if *workspaceID == "" || *agentID == "" {
		usage("--workspace and --agent are required")
	}

	cmdArgs := flag.Args()
	if len(cmdArgs) == 0 {
		usage("agent command is required after --")
	}

	cfg, logger := loadConfig(*configFile, *logLevel, *logFormat)
	logger.Info().
		Str("version", version).
		Str("commit", commit).
		Str("built", date).
		Msg("swarm-agent-runner starting")

	if err := cfg.EnsureDirectories(); err != nil {
		logger.Warn().Err(err).Msg("failed to create directories")
	}

	sink, cleanup, err := buildEventSink(context.Background(), cfg, *workspaceID, *agentID, *eventSocket, *dbPath)
	if err != nil {
		logger.Error().Err(err).Msg("failed to initialize event sink")
		os.Exit(1)
	}
	defer cleanup()

	var promptRE *regexp.Regexp
	if *promptRegex != "" {
		promptRE, err = regexp.Compile(*promptRegex)
		if err != nil {
			usage(fmt.Sprintf("invalid --prompt-regex: %v", err))
		}
	}

	var busyRE *regexp.Regexp
	if *busyRegex != "" {
		busyRE, err = regexp.Compile(*busyRegex)
		if err != nil {
			usage(fmt.Sprintf("invalid --busy-regex: %v", err))
		}
	}

	runnerInstance := &runner.Runner{
		WorkspaceID:       *workspaceID,
		AgentID:           *agentID,
		Command:           cmdArgs,
		PromptRegex:       promptRE,
		BusyRegex:         busyRE,
		HeartbeatInterval: *heartbeat,
		TailLines:         *tailLines,
		EventSink:         sink,
		ControlReader:     os.Stdin,
		OutputWriter:      os.Stdout,
	}

	if err := runnerInstance.Run(context.Background()); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		logger.Error().Err(err).Msg("runner exited with error")
		os.Exit(1)
	}
}

func usage(message string) {
	if message != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n\n", message)
	}
	fmt.Fprintf(os.Stderr, "Usage: swarm-agent-runner --workspace W --agent A [options] -- <command>\n\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func loadConfig(path, level, format string) (*config.Config, zerolog.Logger) {
	loader := config.NewLoader()
	if path != "" {
		loader.SetConfigFile(path)
	}
	cfg, err := loader.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if level != "" {
		cfg.Logging.Level = level
	}
	if format != "" {
		cfg.Logging.Format = format
	}

	logging.Init(logging.Config{
		Level:        cfg.Logging.Level,
		Format:       cfg.Logging.Format,
		EnableCaller: cfg.Logging.EnableCaller,
	})

	logger := logging.Component("agent-runner")
	if used := loader.ConfigFileUsed(); used != "" {
		logger.Debug().Str("config_file", used).Msg("loaded config file")
	}

	return cfg, logger
}

func buildEventSink(ctx context.Context, cfg *config.Config, workspaceID, agentID, eventSocket, dbPath string) (runner.EventSink, func(), error) {
	if eventSocket != "" {
		sink, err := runner.NewSocketEventSink(eventSocket)
		if err != nil {
			return nil, func() {}, err
		}
		return sink, func() { _ = sink.Close() }, nil
	}

	if dbPath == "" {
		dbPath = cfg.DatabasePath()
	}

	dbConfig := db.DefaultConfig()
	dbConfig.Path = dbPath
	if cfg.Database.MaxConnections > 0 {
		dbConfig.MaxOpenConns = cfg.Database.MaxConnections
	}
	if cfg.Database.BusyTimeoutMs > 0 {
		dbConfig.BusyTimeoutMs = cfg.Database.BusyTimeoutMs
	}

	database, err := db.Open(dbConfig)
	if err != nil {
		return nil, func() {}, err
	}

	if err := database.Migrate(ctx); err != nil {
		_ = database.Close()
		return nil, func() {}, err
	}

	sink := runner.NewDatabaseEventSink(database, workspaceID, agentID)
	return sink, func() { _ = sink.Close() }, nil
}
