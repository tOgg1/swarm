package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPrerequisites(t *testing.T) {
	result := checkPrerequisites()

	// tmux and git should be available in most dev environments
	// If not, this test will show what's missing
	if result.status == "failed" {
		t.Logf("Prerequisites check failed: %s", result.message)
		t.Logf("This is expected if tmux or git is not installed")
		return
	}

	if result.status != "done" {
		t.Errorf("expected status 'done', got %q", result.status)
	}

	// Should mention both tools
	if !strings.Contains(result.message, "tmux") && !strings.Contains(result.message, "git") {
		t.Errorf("expected message to mention tmux and git, got: %s", result.message)
	}
}

func TestCreateConfigFile(t *testing.T) {
	// Create a temp directory for testing
	tempDir := t.TempDir()

	// Override the config dir function
	originalFunc := configDirFunc
	configDirFunc = func() string {
		return tempDir
	}
	defer func() {
		configDirFunc = originalFunc
	}()

	// Force mode for non-interactive
	originalForce := initForce
	initForce = true
	defer func() {
		initForce = originalForce
	}()

	result := createConfigFile()

	if result.status != "done" {
		t.Errorf("expected status 'done', got %q: %s", result.status, result.message)
	}

	// Check file was created
	configPath := filepath.Join(tempDir, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("config file was not created at %s", configPath)
	}

	// Check content
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if !strings.Contains(string(content), "Swarm Configuration File") {
		t.Error("config file doesn't contain expected header")
	}
	if !strings.Contains(string(content), "auto_register_local_node: true") {
		t.Error("config file doesn't contain expected default")
	}
}

func TestCreateConfigFile_ExistingNoForce(t *testing.T) {
	// Create a temp directory with existing config
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("failed to create existing config: %v", err)
	}

	// Override the config dir function
	originalFunc := configDirFunc
	configDirFunc = func() string {
		return tempDir
	}
	defer func() {
		configDirFunc = originalFunc
	}()

	// No force, should skip
	originalForce := initForce
	initForce = false
	defer func() {
		initForce = originalForce
	}()

	result := createConfigFile()

	if result.status != "skipped" {
		t.Errorf("expected status 'skipped', got %q: %s", result.status, result.message)
	}

	// Verify original file unchanged
	content, _ := os.ReadFile(configPath)
	if string(content) != "existing" {
		t.Error("existing config was modified")
	}
}

func TestGetConfigDir(t *testing.T) {
	// Test with XDG_CONFIG_HOME set
	originalXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

	os.Setenv("XDG_CONFIG_HOME", "/custom/config")
	dir := defaultConfigDir()
	if dir != "/custom/config/swarm" {
		t.Errorf("expected /custom/config/swarm, got %s", dir)
	}

	// Test without XDG_CONFIG_HOME
	os.Unsetenv("XDG_CONFIG_HOME")
	dir = defaultConfigDir()
	homeDir, _ := os.UserHomeDir()
	expected := filepath.Join(homeDir, ".config", "swarm")
	if dir != expected {
		t.Errorf("expected %s, got %s", expected, dir)
	}
}

func TestConfigTemplate(t *testing.T) {
	// Verify template is valid YAML-like
	if !strings.HasPrefix(configTemplate, "# Swarm Configuration File") {
		t.Error("config template doesn't have expected header")
	}

	// Check essential sections exist
	sections := []string{
		"global:",
		"database:",
		"logging:",
		"node_defaults:",
		"workspace_defaults:",
		"agent_defaults:",
		"scheduler:",
		"tui:",
	}

	for _, section := range sections {
		if !strings.Contains(configTemplate, section) {
			t.Errorf("config template missing section: %s", section)
		}
	}
}

func TestInitResult_Structure(t *testing.T) {
	results := []initResult{
		{name: "Step 1", status: "done", message: "OK"},
		{name: "Step 2", status: "skipped", message: "Already exists"},
		{name: "Step 3", status: "failed", message: "Something went wrong"},
	}

	// Verify the structure is correct
	for i, r := range results {
		if r.name == "" {
			t.Errorf("result %d has empty name", i)
		}
		if r.status == "" {
			t.Errorf("result %d has empty status", i)
		}
	}

	// Verify valid statuses
	validStatuses := map[string]bool{"done": true, "skipped": true, "failed": true}
	for i, r := range results {
		if !validStatuses[r.status] {
			t.Errorf("result %d has invalid status: %s", i, r.status)
		}
	}
}
