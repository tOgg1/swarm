// Package cli implements the Swarm command-line interface using Cobra.
package cli

import (
	"fmt"
	"os"

	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	cfgFile     string
	jsonOutput  bool
	jsonlOutput bool
	watchMode   bool
	verbose     bool
	logLevel    string
	logFormat   string

	// Global config loader and config
	configLoader *config.Loader
	appConfig    *config.Config
	logger       zerolog.Logger
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Control plane for AI coding agents",
	Long: `Swarm is a control plane for running and supervising AI coding agents
across multiple repositories and servers.

It provides:
  - A fast TUI dashboard for monitoring agent progress
  - A CLI for automation and scripting
  - Deep integration with tmux and SSH
  - Multi-account orchestration with cooldown management

Run 'swarm' without arguments to launch the TUI dashboard.`,
	// Default action is to launch TUI
	RunE: func(cmd *cobra.Command, args []string) error {
		// TODO: Launch TUI
		cmd.Println("Swarm TUI - Coming soon!")
		cmd.Println("Use 'swarm --help' to see available commands.")
		return nil
	},
}

// Execute runs the root command
func Execute(version, commit, date string) error {
	rootCmd.Version = formatVersion(version, commit, date)
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.config/swarm/config.yaml)")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output in JSON format")
	rootCmd.PersistentFlags().BoolVar(&jsonlOutput, "jsonl", false, "output in JSON Lines format (for streaming)")
	rootCmd.PersistentFlags().BoolVar(&watchMode, "watch", false, "watch for changes and stream updates")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "override logging level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "", "override logging format (json, console)")
}

// initConfig loads configuration using Viper with proper precedence:
// defaults < config file < env vars < CLI flags
func initConfig() {
	configLoader = config.NewLoader()

	// Set explicit config file if provided via CLI flag
	if cfgFile != "" {
		configLoader.SetConfigFile(cfgFile)
	}

	// Load configuration
	var err error
	appConfig, err = configLoader.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	applyCLIOverrides()

	// Initialize logging based on config
	initLogging()

	// Ensure directories exist
	if err := appConfig.EnsureDirectories(); err != nil {
		logger.Warn().Err(err).Msg("failed to create directories")
	}

	// Log config file used (if any)
	if cfgUsed := configLoader.ConfigFileUsed(); cfgUsed != "" {
		logger.Debug().Str("config_file", cfgUsed).Msg("loaded config file")
	}
}

func applyCLIOverrides() {
	flags := rootCmd.PersistentFlags()

	if flags.Changed("log-level") {
		appConfig.Logging.Level = logLevel
	} else if verbose {
		appConfig.Logging.Level = "debug"
	}

	if flags.Changed("log-format") {
		appConfig.Logging.Format = logFormat
	}
}

// initLogging sets up the logger based on configuration
func initLogging() {
	logCfg := logging.Config{
		Level:        appConfig.Logging.Level,
		Format:       appConfig.Logging.Format,
		EnableCaller: appConfig.Logging.EnableCaller,
	}

	// TODO: Add file output support when needed
	// if appConfig.Logging.File != "" {
	//     logCfg.Output = ... open file ...
	// }

	logging.Init(logCfg)
	logger = logging.Component("cli")
}

// GetConfig returns the loaded configuration.
// Returns nil if called before initConfig.
func GetConfig() *config.Config {
	return appConfig
}

// GetLogger returns the CLI logger.
func GetLogger() zerolog.Logger {
	return logger
}

// IsJSONOutput returns true if JSON output mode is enabled.
func IsJSONOutput() bool {
	return jsonOutput
}

// IsJSONLOutput returns true if JSONL output mode is enabled.
func IsJSONLOutput() bool {
	return jsonlOutput
}

// IsWatchMode returns true if watch mode is enabled.
func IsWatchMode() bool {
	return watchMode
}

// IsVerbose returns true if verbose mode is enabled.
func IsVerbose() bool {
	return verbose
}

func formatVersion(version, commit, date string) string {
	return version + " (commit: " + commit + ", built: " + date + ")"
}
