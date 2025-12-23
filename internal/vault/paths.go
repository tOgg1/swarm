// Package vault provides credential storage and instant profile switching for AI coding agents.
package vault

import (
	"os"
	"path/filepath"
)

// Adapter represents a supported AI coding agent CLI.
type Adapter string

// Supported adapters.
const (
	AdapterClaude   Adapter = "claude"
	AdapterCodex    Adapter = "codex"
	AdapterGemini   Adapter = "gemini"
	AdapterOpenCode Adapter = "opencode"
)

// AllAdapters returns all supported adapters.
func AllAdapters() []Adapter {
	return []Adapter{
		AdapterClaude,
		AdapterCodex,
		AdapterGemini,
		AdapterOpenCode,
	}
}

// ParseAdapter converts a string to an Adapter, returning empty string if invalid.
func ParseAdapter(s string) Adapter {
	switch s {
	case "claude", "anthropic":
		return AdapterClaude
	case "codex", "openai":
		return AdapterCodex
	case "gemini", "google":
		return AdapterGemini
	case "opencode":
		return AdapterOpenCode
	default:
		return ""
	}
}

// Provider returns the provider name for this adapter.
func (a Adapter) Provider() string {
	switch a {
	case AdapterClaude:
		return "anthropic"
	case AdapterCodex:
		return "openai"
	case AdapterGemini:
		return "google"
	case AdapterOpenCode:
		return "opencode"
	default:
		return string(a)
	}
}

// String returns the adapter name.
func (a Adapter) String() string {
	return string(a)
}

// AuthPaths represents the auth file locations for an adapter.
type AuthPaths struct {
	// Adapter is the adapter these paths belong to.
	Adapter Adapter

	// Primary is the main auth file path.
	Primary string

	// Secondary contains additional auth file paths (some adapters use multiple files).
	Secondary []string
}

// AllPaths returns all auth file paths (primary + secondary).
func (p AuthPaths) AllPaths() []string {
	paths := make([]string, 0, 1+len(p.Secondary))
	if p.Primary != "" {
		paths = append(paths, p.Primary)
	}
	paths = append(paths, p.Secondary...)
	return paths
}

// ExistingPaths returns only the paths that currently exist on disk.
func (p AuthPaths) ExistingPaths() []string {
	var existing []string
	for _, path := range p.AllPaths() {
		if _, err := os.Stat(path); err == nil {
			existing = append(existing, path)
		}
	}
	return existing
}

// HasAuth returns true if any auth files exist.
func (p AuthPaths) HasAuth() bool {
	return len(p.ExistingPaths()) > 0
}

// GetAuthPaths returns the auth file paths for an adapter.
func GetAuthPaths(adapter Adapter) AuthPaths {
	home := homeDir()

	switch adapter {
	case AdapterClaude:
		return AuthPaths{
			Adapter: adapter,
			Primary: filepath.Join(home, ".claude.json"),
			Secondary: []string{
				filepath.Join(home, ".config", "claude-code", "auth.json"),
			},
		}

	case AdapterCodex:
		// Codex respects CODEX_HOME environment variable
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return AuthPaths{
			Adapter: adapter,
			Primary: filepath.Join(codexHome, "auth.json"),
		}

	case AdapterGemini:
		return AuthPaths{
			Adapter: adapter,
			Primary: filepath.Join(home, ".gemini", "settings.json"),
		}

	case AdapterOpenCode:
		return AuthPaths{
			Adapter: adapter,
			Primary: filepath.Join(home, ".opencode", "auth.json"),
		}

	default:
		return AuthPaths{Adapter: adapter}
	}
}

// GetAllAuthPaths returns auth paths for all supported adapters.
func GetAllAuthPaths() map[Adapter]AuthPaths {
	paths := make(map[Adapter]AuthPaths)
	for _, adapter := range AllAdapters() {
		paths[adapter] = GetAuthPaths(adapter)
	}
	return paths
}

// homeDir returns the user's home directory.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// DefaultVaultPath returns the default vault directory path.
func DefaultVaultPath() string {
	home := homeDir()
	return filepath.Join(home, ".config", "swarm", "vault")
}

// ProfilesPath returns the profiles subdirectory within a vault.
func ProfilesPath(vaultPath string) string {
	return filepath.Join(vaultPath, "profiles")
}

// ProfilePath returns the path for a specific profile within a vault.
func ProfilePath(vaultPath string, adapter Adapter, profileName string) string {
	return filepath.Join(ProfilesPath(vaultPath), adapter.Provider(), profileName)
}
