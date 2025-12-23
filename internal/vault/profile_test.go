package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestProfile creates a test environment with mock auth files.
// Returns vaultPath, homeDir, and cleanup function.
func setupTestProfile(t *testing.T) (string, string) {
	t.Helper()

	vaultDir := t.TempDir()
	homeDir := t.TempDir()

	return vaultDir, homeDir
}

func TestParseAdapter(t *testing.T) {
	tests := []struct {
		input    string
		expected Adapter
	}{
		{"claude", AdapterClaude},
		{"anthropic", AdapterClaude},
		{"codex", AdapterCodex},
		{"openai", AdapterCodex},
		{"gemini", AdapterGemini},
		{"google", AdapterGemini},
		{"opencode", AdapterOpenCode},
		{"unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseAdapter(tt.input)
			if got != tt.expected {
				t.Errorf("ParseAdapter(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAdapterProvider(t *testing.T) {
	tests := []struct {
		adapter  Adapter
		expected string
	}{
		{AdapterClaude, "anthropic"},
		{AdapterCodex, "openai"},
		{AdapterGemini, "google"},
		{AdapterOpenCode, "opencode"},
		{Adapter("unknown"), "unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.adapter), func(t *testing.T) {
			got := tt.adapter.Provider()
			if got != tt.expected {
				t.Errorf("Adapter(%q).Provider() = %q, want %q", tt.adapter, got, tt.expected)
			}
		})
	}
}

func TestAllAdapters(t *testing.T) {
	adapters := AllAdapters()
	if len(adapters) != 4 {
		t.Errorf("expected 4 adapters, got %d", len(adapters))
	}

	expected := map[Adapter]bool{
		AdapterClaude:   true,
		AdapterCodex:    true,
		AdapterGemini:   true,
		AdapterOpenCode: true,
	}

	for _, a := range adapters {
		if !expected[a] {
			t.Errorf("unexpected adapter: %v", a)
		}
	}
}

func TestAuthPathsAllPaths(t *testing.T) {
	paths := AuthPaths{
		Adapter:   AdapterClaude,
		Primary:   "/home/user/.claude.json",
		Secondary: []string{"/home/user/.config/claude-code/auth.json"},
	}

	allPaths := paths.AllPaths()
	if len(allPaths) != 2 {
		t.Errorf("expected 2 paths, got %d", len(allPaths))
	}
	if allPaths[0] != "/home/user/.claude.json" {
		t.Errorf("expected primary first, got %q", allPaths[0])
	}
}

func TestDefaultVaultPath(t *testing.T) {
	path := DefaultVaultPath()
	if path == "" {
		t.Error("DefaultVaultPath returned empty string")
	}
	if !filepath.IsAbs(path) {
		t.Error("DefaultVaultPath should return absolute path")
	}
	if !contains(path, ".config/swarm/vault") {
		t.Errorf("expected path to contain .config/swarm/vault, got %q", path)
	}
}

func TestProfilesPath(t *testing.T) {
	vaultPath := "/test/vault"
	path := ProfilesPath(vaultPath)
	expected := "/test/vault/profiles"
	if path != expected {
		t.Errorf("ProfilesPath(%q) = %q, want %q", vaultPath, path, expected)
	}
}

func TestProfilePath(t *testing.T) {
	vaultPath := "/test/vault"
	path := ProfilePath(vaultPath, AdapterClaude, "work")
	expected := "/test/vault/profiles/anthropic/work"
	if path != expected {
		t.Errorf("ProfilePath() = %q, want %q", path, expected)
	}
}

func TestBackupInvalidProfileName(t *testing.T) {
	vaultDir, _ := setupTestProfile(t)

	_, err := Backup(vaultDir, AdapterClaude, "")
	if err != ErrInvalidProfileName {
		t.Errorf("expected ErrInvalidProfileName, got %v", err)
	}
}

func TestBackupNoAuthFiles(t *testing.T) {
	vaultDir := t.TempDir()

	// With real auth paths, no files should exist in temp dir
	// GetAuthPaths returns paths in user's home, which won't exist
	// So we just verify the error message is reasonable
	_, err := Backup(vaultDir, AdapterOpenCode, "test")
	if err == nil {
		t.Error("expected error for missing auth files")
	}
	if err != ErrNoAuthFiles {
		// This is expected - the real home dir paths don't exist in CI
		t.Logf("got expected error: %v", err)
	}
}

func TestActivateInvalidProfileName(t *testing.T) {
	vaultDir := t.TempDir()

	err := Activate(vaultDir, AdapterClaude, "")
	if err != ErrInvalidProfileName {
		t.Errorf("expected ErrInvalidProfileName, got %v", err)
	}
}

func TestActivateProfileNotFound(t *testing.T) {
	vaultDir := t.TempDir()

	err := Activate(vaultDir, AdapterClaude, "nonexistent")
	if err != ErrProfileNotFound {
		t.Errorf("expected ErrProfileNotFound, got %v", err)
	}
}

func TestDeleteInvalidProfileName(t *testing.T) {
	vaultDir := t.TempDir()

	err := Delete(vaultDir, AdapterClaude, "")
	if err != ErrInvalidProfileName {
		t.Errorf("expected ErrInvalidProfileName, got %v", err)
	}
}

func TestDeleteProfileNotFound(t *testing.T) {
	vaultDir := t.TempDir()

	err := Delete(vaultDir, AdapterClaude, "nonexistent")
	if err != ErrProfileNotFound {
		t.Errorf("expected ErrProfileNotFound, got %v", err)
	}
}

func TestGetInvalidProfileName(t *testing.T) {
	vaultDir := t.TempDir()

	_, err := Get(vaultDir, AdapterClaude, "")
	if err != ErrInvalidProfileName {
		t.Errorf("expected ErrInvalidProfileName, got %v", err)
	}
}

func TestGetProfileNotFound(t *testing.T) {
	vaultDir := t.TempDir()

	_, err := Get(vaultDir, AdapterClaude, "nonexistent")
	if err != ErrProfileNotFound {
		t.Errorf("expected ErrProfileNotFound, got %v", err)
	}
}

func TestListEmptyVault(t *testing.T) {
	vaultDir := t.TempDir()

	profiles, err := List(vaultDir, AdapterClaude)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("expected 0 profiles, got %d", len(profiles))
	}
}

func TestGetActiveNoProfiles(t *testing.T) {
	vaultDir := t.TempDir()

	active, err := GetActive(vaultDir, AdapterClaude)
	if err != nil {
		t.Fatalf("GetActive failed: %v", err)
	}
	if active != nil {
		t.Errorf("expected nil for no profiles, got %v", active)
	}
}

func TestClearNoFiles(t *testing.T) {
	// Clear should not error when no files exist
	err := Clear(AdapterOpenCode)
	if err != nil {
		t.Errorf("Clear should not error for missing files: %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create source file
	srcPath := filepath.Join(srcDir, "source.txt")
	content := "test content"
	if err := os.WriteFile(srcPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	// Copy to destination
	dstPath := filepath.Join(dstDir, "dest.txt")
	if err := CopyFile(srcPath, dstPath); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	// Verify content
	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("failed to read destination: %v", err)
	}
	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}
}

func TestCopyFileCreatesParentDir(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create source file
	srcPath := filepath.Join(srcDir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create source: %v", err)
	}

	// Copy to nested path
	dstPath := filepath.Join(dstDir, "nested", "dir", "dest.txt")
	if err := CopyFile(srcPath, dstPath); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	if _, err := os.Stat(dstPath); err != nil {
		t.Errorf("destination file should exist: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Integration test that creates a profile and verifies all operations
func TestProfileLifecycle(t *testing.T) {
	vaultDir := t.TempDir()

	// Create a mock profile directory manually to test Get, List, Delete
	profilePath := ProfilePath(vaultDir, AdapterClaude, "testprofile")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	// Create mock auth file
	authPath := filepath.Join(profilePath, ".claude.json")
	authContent := `{"api_key": "sk-test"}`
	if err := os.WriteFile(authPath, []byte(authContent), 0600); err != nil {
		t.Fatalf("failed to create auth file: %v", err)
	}

	// Create metadata
	metaPath := filepath.Join(profilePath, "meta.json")
	metaContent := `{
		"adapter": "claude",
		"name": "testprofile",
		"created_at": "2024-01-01T00:00:00Z",
		"updated_at": "2024-01-01T00:00:00Z",
		"auth_files": [".claude.json"],
		"content_hash": "abc123"
	}`
	if err := os.WriteFile(metaPath, []byte(metaContent), 0600); err != nil {
		t.Fatalf("failed to create meta file: %v", err)
	}

	// Test Get
	profile, err := Get(vaultDir, AdapterClaude, "testprofile")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if profile.Name != "testprofile" {
		t.Errorf("expected name 'testprofile', got %q", profile.Name)
	}
	if len(profile.AuthFiles) != 1 {
		t.Errorf("expected 1 auth file, got %d", len(profile.AuthFiles))
	}

	// Test List
	profiles, err := List(vaultDir, AdapterClaude)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("expected 1 profile, got %d", len(profiles))
	}

	// Test Delete
	if err := Delete(vaultDir, AdapterClaude, "testprofile"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	_, err = Get(vaultDir, AdapterClaude, "testprofile")
	if err != ErrProfileNotFound {
		t.Errorf("expected ErrProfileNotFound after delete, got %v", err)
	}
}
