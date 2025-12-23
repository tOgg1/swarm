// Package cli provides the init command for first-run setup.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/spf13/cobra"
)

var (
	initForce bool
)

func init() {
	rootCmd.AddCommand(initCmd)

	initCmd.Flags().BoolVarP(&initForce, "force", "f", false, "overwrite existing config file")
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Swarm for first use",
	Long: `Initialize Swarm by setting up configuration, database, and local node.

This command performs the following steps:
  1. Verify prerequisites (tmux, git)
  2. Create configuration file from template
  3. Run database migrations
  4. Register local node (if configured)
  5. Print next steps

Use --yes to skip all confirmations for automation.`,
	Example: `  # Interactive setup
  swarm init

  # Non-interactive with defaults
  swarm init --yes

  # Force overwrite existing config
  swarm init --force`,
	RunE: runInit,
}

// initStep represents a step in the init process.
type initStep struct {
	name    string
	check   func() error
	run     func() error
	skipped bool
}

// initResult holds the result of an init step.
type initResult struct {
	name    string
	status  string // "done", "skipped", "failed"
	message string
}

func runInit(cmd *cobra.Command, args []string) error {
	results := make([]initResult, 0)

	// Print header
	if !IsJSONOutput() {
		fmt.Println("Initializing Swarm...")
		fmt.Println()
	}

	// Step 1: Check prerequisites
	result := checkPrerequisites()
	results = append(results, result)
	if result.status == "failed" {
		return printInitSummary(results, fmt.Errorf("prerequisite check failed: %s", result.message))
	}

	// Step 2: Create config file
	result = createConfigFile()
	results = append(results, result)
	if result.status == "failed" {
		return printInitSummary(results, fmt.Errorf("config creation failed: %s", result.message))
	}

	// Re-initialize config after creating file
	if result.status == "done" {
		initConfig()
	}

	// Step 3: Run migrations
	result = runMigrations()
	results = append(results, result)
	if result.status == "failed" {
		return printInitSummary(results, fmt.Errorf("migration failed: %s", result.message))
	}

	// Step 4: Register local node
	result = registerLocalNode()
	results = append(results, result)
	if result.status == "failed" {
		// Local node registration is optional, don't fail
		logger.Warn().Str("error", result.message).Msg("local node registration failed")
	}

	// Print summary
	return printInitSummary(results, nil)
}

func checkPrerequisites() initResult {
	result := initResult{name: "Check prerequisites"}

	// Check tmux
	tmuxPath, tmuxErr := exec.LookPath("tmux")
	tmuxVersion := ""
	if tmuxErr == nil {
		out, err := exec.Command("tmux", "-V").Output()
		if err == nil {
			tmuxVersion = strings.TrimSpace(string(out))
		}
	}

	// Check git
	gitPath, gitErr := exec.LookPath("git")
	gitVersion := ""
	if gitErr == nil {
		out, err := exec.Command("git", "--version").Output()
		if err == nil {
			gitVersion = strings.TrimSpace(string(out))
		}
	}

	var missing []string
	if tmuxErr != nil {
		missing = append(missing, "tmux")
	}
	if gitErr != nil {
		missing = append(missing, "git")
	}

	if len(missing) > 0 {
		result.status = "failed"
		result.message = fmt.Sprintf("missing required tools: %s", strings.Join(missing, ", "))
		return result
	}

	result.status = "done"
	var found []string
	if tmuxVersion != "" {
		found = append(found, tmuxVersion)
	} else {
		found = append(found, fmt.Sprintf("tmux at %s", tmuxPath))
	}
	if gitVersion != "" {
		found = append(found, gitVersion)
	} else {
		found = append(found, fmt.Sprintf("git at %s", gitPath))
	}
	result.message = strings.Join(found, ", ")

	return result
}

func createConfigFile() initResult {
	result := initResult{name: "Create config file"}

	// Determine config path
	configDir := getConfigDir()
	configPath := filepath.Join(configDir, "config.yaml")

	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil {
		if !initForce {
			result.status = "skipped"
			result.message = fmt.Sprintf("config exists at %s (use --force to overwrite)", configPath)
			return result
		}
	}

	// Ask for confirmation if interactive
	if IsInteractive() && !initForce {
		if _, err := os.Stat(configPath); err == nil {
			fmt.Printf("Config file already exists at %s\n", configPath)
			if !confirm("Overwrite?") {
				result.status = "skipped"
				result.message = "user declined overwrite"
				return result
			}
		} else {
			fmt.Printf("Will create config at %s\n", configPath)
			if !confirm("Continue?") {
				result.status = "skipped"
				result.message = "user declined"
				return result
			}
		}
	}

	// Create config directory
	if err := os.MkdirAll(configDir, 0755); err != nil {
		result.status = "failed"
		result.message = fmt.Sprintf("failed to create directory: %v", err)
		return result
	}

	// Write config template
	if err := os.WriteFile(configPath, []byte(configTemplate), 0644); err != nil {
		result.status = "failed"
		result.message = fmt.Sprintf("failed to write config: %v", err)
		return result
	}

	result.status = "done"
	result.message = configPath
	return result
}

func runMigrations() initResult {
	result := initResult{name: "Run migrations"}

	ctx := context.Background()

	database, err := openDatabase()
	if err != nil {
		result.status = "failed"
		result.message = fmt.Sprintf("failed to open database: %v", err)
		return result
	}
	defer database.Close()

	// Check current version
	version, err := database.SchemaVersion(ctx)
	if err != nil && !isMissingSchemaTable(err) {
		result.status = "failed"
		result.message = fmt.Sprintf("failed to check schema version: %v", err)
		return result
	}

	// Apply migrations
	applied, err := database.MigrateUp(ctx)
	if err != nil {
		result.status = "failed"
		result.message = fmt.Sprintf("migration failed: %v", err)
		return result
	}

	if applied == 0 {
		result.status = "skipped"
		result.message = fmt.Sprintf("already at version %d", version)
	} else {
		newVersion, _ := database.SchemaVersion(ctx)
		result.status = "done"
		result.message = fmt.Sprintf("applied %d migration(s), now at version %d", applied, newVersion)
	}

	return result
}

func registerLocalNode() initResult {
	result := initResult{name: "Register local node"}

	// Check if auto-register is enabled
	if appConfig == nil || !appConfig.Global.AutoRegisterLocalNode {
		result.status = "skipped"
		result.message = "auto_register_local_node is disabled"
		return result
	}

	ctx := context.Background()

	database, err := openDatabase()
	if err != nil {
		result.status = "failed"
		result.message = fmt.Sprintf("failed to open database: %v", err)
		return result
	}
	defer database.Close()

	repo := db.NewNodeRepository(database)
	service := node.NewService(repo)

	// Check if a local node already exists
	nodes, err := service.ListNodes(ctx, nil)
	if err != nil {
		result.status = "failed"
		result.message = fmt.Sprintf("failed to list nodes: %v", err)
		return result
	}

	for _, n := range nodes {
		if n.IsLocal {
			result.status = "skipped"
			result.message = fmt.Sprintf("local node '%s' already registered", n.Name)
			return result
		}
	}

	// Determine hostname for node name
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}

	// Create local node
	localNode := &models.Node{
		Name:    hostname,
		IsLocal: true,
		Status:  models.NodeStatusOnline,
	}

	if err := service.AddNode(ctx, localNode, false); err != nil {
		if errors.Is(err, node.ErrNodeAlreadyExists) {
			result.status = "skipped"
			result.message = fmt.Sprintf("node '%s' already exists", hostname)
			return result
		}
		result.status = "failed"
		result.message = fmt.Sprintf("failed to register: %v", err)
		return result
	}

	result.status = "done"
	result.message = fmt.Sprintf("registered '%s' (ID: %s)", localNode.Name, shortID(localNode.ID))
	return result
}

func printInitSummary(results []initResult, err error) error {
	if IsJSONOutput() {
		type jsonResult struct {
			Success bool         `json:"success"`
			Steps   []initResult `json:"steps"`
			Error   string       `json:"error,omitempty"`
		}
		jr := jsonResult{
			Success: err == nil,
			Steps:   results,
		}
		if err != nil {
			jr.Error = err.Error()
		}
		return WriteOutput(os.Stdout, jr)
	}

	fmt.Println()
	fmt.Println("Summary:")
	for _, r := range results {
		icon := "✓"
		if r.status == "skipped" {
			icon = "○"
		} else if r.status == "failed" {
			icon = "✗"
		}
		fmt.Printf("  %s %s: %s\n", icon, r.name, r.message)
	}
	fmt.Println()

	if err != nil {
		fmt.Println("Initialization failed.")
		fmt.Println()
		fmt.Println("Hints:")
		fmt.Println("  - Check that tmux and git are installed and in PATH")
		fmt.Println("  - Ensure ~/.config/swarm is writable")
		fmt.Println("  - Ensure ~/.local/share/swarm is writable")
		fmt.Println()
		return err
	}

	fmt.Println("Swarm initialized successfully!")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Edit config at ~/.config/swarm/config.yaml")
	fmt.Println("  2. Add API keys for your AI providers")
	fmt.Println("  3. Create a workspace: swarm ws create --path /path/to/repo")
	fmt.Println("  4. Launch the TUI: swarm")
	fmt.Println()

	return nil
}

// configDirFunc is a function that returns the config directory.
// It can be overridden in tests.
var configDirFunc = defaultConfigDir

func getConfigDir() string {
	return configDirFunc()
}

func defaultConfigDir() string {
	// Check XDG_CONFIG_HOME first
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "swarm")
	}

	// Fall back to ~/.config/swarm
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "swarm")
	}
	return filepath.Join(homeDir, ".config", "swarm")
}

func confirm(prompt string) bool {
	if SkipConfirmation() {
		return true
	}

	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// configTemplate is the default configuration file content.
const configTemplate = `# Swarm Configuration File
# Generated by 'swarm init'

# Global settings
global:
  # Directory for Swarm data (database, logs, transcripts)
  data_dir: ~/.local/share/swarm
  
  # Directory for configuration files
  config_dir: ~/.config/swarm
  
  # Automatically register the local machine as a node
  auto_register_local_node: true

# Database settings
database:
  # Maximum database connections
  max_connections: 10
  
  # Timeout for locked database (milliseconds)
  busy_timeout_ms: 5000

# Logging settings
logging:
  # Minimum log level: debug, info, warn, error
  level: info
  
  # Output format: json, console
  format: console

# Accounts configuration
# Add your AI provider API keys here
# accounts:
#   - provider: anthropic
#     profile_name: default
#     credential_ref: "$ANTHROPIC_API_KEY"
#     is_active: true

# Default settings for nodes
node_defaults:
  # SSH backend: native (Go SSH), system (ssh binary), auto
  ssh_backend: auto
  
  # SSH connection timeout
  ssh_timeout: 30s

# Default settings for workspaces
workspace_defaults:
  # Prefix for generated tmux session names
  tmux_prefix: swarm
  
  # Default agent type to spawn
  default_agent_type: opencode

# Default settings for agents
agent_defaults:
  # Default agent CLI type: opencode, claude-code, codex, generic
  default_type: opencode
  
  # How often to poll agent state
  state_polling_interval: 2s
  
  # Approval policy: strict (always ask), permissive (auto-approve)
  approval_policy: strict

# Scheduler settings
scheduler:
  # How often the scheduler runs dispatch checks
  dispatch_interval: 1s
  
  # Maximum retry attempts for failed dispatches
  max_retries: 3
  
  # Automatically rotate to another account on rate limit
  auto_rotate_on_rate_limit: true

# TUI settings
tui:
  # How often to refresh the display
  refresh_interval: 500ms
  
  # Color theme: default, dark, light
  theme: default
`
