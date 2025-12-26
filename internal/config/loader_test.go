package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefault(t *testing.T) {
	cfg, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault() error = %v", err)
	}

	if cfg == nil {
		t.Fatal("LoadDefault() returned nil config")
	}

	// Check some defaults
	if cfg.Logging.Level != "info" {
		t.Errorf("Expected logging.level = 'info', got %q", cfg.Logging.Level)
	}

	if cfg.Database.MaxConnections != 10 {
		t.Errorf("Expected database.max_connections = 10, got %d", cfg.Database.MaxConnections)
	}

	if cfg.WorkspaceDefaults.TmuxPrefix != "swarm" {
		t.Errorf("Expected workspace_defaults.tmux_prefix = 'swarm', got %q", cfg.WorkspaceDefaults.TmuxPrefix)
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create a temp config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
logging:
  level: debug
  format: json
database:
  max_connections: 20
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	// Check overridden values
	if cfg.Logging.Level != "debug" {
		t.Errorf("Expected logging.level = 'debug', got %q", cfg.Logging.Level)
	}

	if cfg.Logging.Format != "json" {
		t.Errorf("Expected logging.format = 'json', got %q", cfg.Logging.Format)
	}

	if cfg.Database.MaxConnections != 20 {
		t.Errorf("Expected database.max_connections = 20, got %d", cfg.Database.MaxConnections)
	}

	// Check defaults are still applied
	if cfg.WorkspaceDefaults.TmuxPrefix != "swarm" {
		t.Errorf("Expected workspace_defaults.tmux_prefix = 'swarm', got %q", cfg.WorkspaceDefaults.TmuxPrefix)
	}
}

func TestEnvironmentOverride(t *testing.T) {
	// Set env var
	t.Setenv("SWARM_LOGGING_LEVEL", "warn")
	t.Setenv("SWARM_DATABASE_MAX_CONNECTIONS", "5")

	loader := NewLoader()
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Logging.Level != "warn" {
		t.Errorf("Expected logging.level = 'warn' from env, got %q", cfg.Logging.Level)
	}

	if cfg.Database.MaxConnections != 5 {
		t.Errorf("Expected database.max_connections = 5 from env, got %d", cfg.Database.MaxConnections)
	}
}

func TestValidation(t *testing.T) {
	cfg := DefaultConfig()

	// Valid config should pass
	if err := cfg.Validate(); err != nil {
		t.Errorf("Valid config failed validation: %v", err)
	}

	// Invalid max_connections
	cfg.Database.MaxConnections = 0
	if err := cfg.Validate(); err == nil {
		t.Error("Expected validation error for max_connections = 0")
	}

	// Reset and test invalid polling interval
	cfg = DefaultConfig()
	cfg.AgentDefaults.StatePollingInterval = 0
	if err := cfg.Validate(); err == nil {
		t.Error("Expected validation error for state_polling_interval = 0")
	}
}

func TestConfigFileNotFound(t *testing.T) {
	// Should not error when config file doesn't exist (uses defaults)
	cfg, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault() should not error when config file missing: %v", err)
	}

	if cfg == nil {
		t.Fatal("Config should not be nil")
	}
}

func TestExplicitConfigFileNotFound(t *testing.T) {
	// Should error when explicitly specified config file doesn't exist
	_, err := LoadFromFile("/nonexistent/config.yaml")
	if err == nil {
		t.Error("LoadFromFile() should error for nonexistent file")
	}
}

func TestDatabasePath(t *testing.T) {
	cfg := DefaultConfig()

	// Default should use DataDir
	expectedPath := filepath.Join(cfg.Global.DataDir, "swarm.db")
	if cfg.DatabasePath() != expectedPath {
		t.Errorf("DatabasePath() = %q, want %q", cfg.DatabasePath(), expectedPath)
	}

	// Explicit path should be used
	cfg.Database.Path = "/custom/path.db"
	if cfg.DatabasePath() != "/custom/path.db" {
		t.Errorf("DatabasePath() = %q, want '/custom/path.db'", cfg.DatabasePath())
	}
}

func TestEnsureDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := DefaultConfig()
	cfg.Global.DataDir = filepath.Join(tmpDir, "data")
	cfg.Global.ConfigDir = filepath.Join(tmpDir, "config")

	if err := cfg.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	// Check directories exist
	if _, err := os.Stat(cfg.Global.DataDir); os.IsNotExist(err) {
		t.Error("DataDir was not created")
	}

	if _, err := os.Stat(cfg.Global.ConfigDir); os.IsNotExist(err) {
		t.Error("ConfigDir was not created")
	}
}

func TestTUIThemeValidation(t *testing.T) {
	tests := []struct {
		name    string
		theme   string
		wantErr bool
	}{
		{name: "default theme", theme: "default", wantErr: false},
		{name: "high-contrast theme", theme: "high-contrast", wantErr: false},
		{name: "empty uses default", theme: "", wantErr: true}, // empty fails validation
		{name: "invalid theme", theme: "invalid", wantErr: true},
		{name: "old dark theme", theme: "dark", wantErr: true},
		{name: "old light theme", theme: "light", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.TUI.Theme = tt.theme

			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTUIThemeFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
tui:
  theme: high-contrast
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	if cfg.TUI.Theme != "high-contrast" {
		t.Errorf("Expected tui.theme = 'high-contrast', got %q", cfg.TUI.Theme)
	}
}

func TestExpandTilde(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty", input: "", expected: ""},
		{name: "absolute path", input: "/var/log/test", expected: "/var/log/test"},
		{name: "relative path", input: "data/file", expected: "data/file"},
		{name: "tilde only", input: "~", expected: home},
		{name: "tilde with path", input: "~/data/swarm", expected: filepath.Join(home, "data/swarm")},
		{name: "tilde in middle", input: "/var/~/data", expected: "/var/~/data"}, // should not expand
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandTilde(tt.input)
			if result != tt.expected {
				t.Errorf("expandTilde(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExpandPathsInConfig(t *testing.T) {
	home, _ := os.UserHomeDir()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
global:
  data_dir: ~/.local/share/swarm
  config_dir: ~/.config/swarm
database:
  path: ~/custom/db.sqlite
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	expectedDataDir := filepath.Join(home, ".local/share/swarm")
	if cfg.Global.DataDir != expectedDataDir {
		t.Errorf("DataDir = %q, want %q", cfg.Global.DataDir, expectedDataDir)
	}

	expectedConfigDir := filepath.Join(home, ".config/swarm")
	if cfg.Global.ConfigDir != expectedConfigDir {
		t.Errorf("ConfigDir = %q, want %q", cfg.Global.ConfigDir, expectedConfigDir)
	}

	expectedDBPath := filepath.Join(home, "custom/db.sqlite")
	if cfg.Database.Path != expectedDBPath {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, expectedDBPath)
	}
}
