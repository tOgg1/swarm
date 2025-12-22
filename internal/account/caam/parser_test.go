package caam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

func TestDefaultVaultPath(t *testing.T) {
	path := DefaultVaultPath()

	// Should contain .local/share/caam/vault
	if !filepath.IsAbs(path) {
		// May be relative if home dir lookup fails
		if path != filepath.Join(".", ".local", "share", "caam", "vault") {
			t.Errorf("unexpected default path: %s", path)
		}
	}
}

func TestParseVault(t *testing.T) {
	// Create a temporary vault structure
	tmpDir := t.TempDir()

	// Create provider directories
	claudeDir := filepath.Join(tmpDir, "claude", "alice@example.com")
	codexDir := filepath.Join(tmpDir, "codex", "bob@example.com")
	geminiDir := filepath.Join(tmpDir, "gemini", "carol@example.com")

	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create auth files
	writeFile(t, filepath.Join(claudeDir, ".claude.json"), `{"version": 1}`)
	writeFile(t, filepath.Join(claudeDir, "auth.json"), `{"token": "secret"}`)
	writeFile(t, filepath.Join(claudeDir, "meta.json"), `{"backed_up_at": "2024-01-15T10:30:00Z"}`)

	writeFile(t, filepath.Join(codexDir, "auth.json"), `{"api_key": "sk-xxx"}`)

	writeFile(t, filepath.Join(geminiDir, "settings.json"), `{"project_id": "my-project"}`)

	// Parse the vault
	config, err := ParseVault(tmpDir)
	if err != nil {
		t.Fatalf("ParseVault failed: %v", err)
	}

	// Verify vault config
	if config.Path != tmpDir {
		t.Errorf("expected path %s, got %s", tmpDir, config.Path)
	}

	if len(config.Profiles) != 3 {
		t.Errorf("expected 3 profiles, got %d", len(config.Profiles))
	}

	// Check providers list
	providers := config.ListProviders()
	if len(providers) != 3 {
		t.Errorf("expected 3 providers, got %d", len(providers))
	}
}

func TestParseVaultNotFound(t *testing.T) {
	_, err := ParseVault("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent vault")
	}
}

func TestParseVaultNotDirectory(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "file.txt")
	writeFile(t, tmpFile, "not a directory")

	_, err := ParseVault(tmpFile)
	if err == nil {
		t.Error("expected error for file path")
	}
}

func TestParseVaultEmpty(t *testing.T) {
	tmpDir := t.TempDir()

	config, err := ParseVault(tmpDir)
	if err != nil {
		t.Fatalf("ParseVault failed: %v", err)
	}

	if len(config.Profiles) != 0 {
		t.Errorf("expected 0 profiles, got %d", len(config.Profiles))
	}
}

func TestProfileIsValid(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		authFiles []string
		expected  bool
	}{
		{
			name:      "claude with .claude.json",
			provider:  "claude",
			authFiles: []string{".claude.json"},
			expected:  true,
		},
		{
			name:      "claude with auth.json",
			provider:  "claude",
			authFiles: []string{"auth.json"},
			expected:  true,
		},
		{
			name:      "claude with both",
			provider:  "claude",
			authFiles: []string{".claude.json", "auth.json"},
			expected:  true,
		},
		{
			name:      "claude without auth files",
			provider:  "claude",
			authFiles: []string{"random.txt"},
			expected:  false,
		},
		{
			name:      "codex with auth.json",
			provider:  "codex",
			authFiles: []string{"auth.json"},
			expected:  true,
		},
		{
			name:      "codex without auth.json",
			provider:  "codex",
			authFiles: []string{"other.json"},
			expected:  false,
		},
		{
			name:      "gemini with settings.json",
			provider:  "gemini",
			authFiles: []string{"settings.json"},
			expected:  true,
		},
		{
			name:      "gemini without settings.json",
			provider:  "gemini",
			authFiles: []string{"config.json"},
			expected:  false,
		},
		{
			name:      "unknown provider with files",
			provider:  "unknown",
			authFiles: []string{"something.json"},
			expected:  true,
		},
		{
			name:      "unknown provider empty",
			provider:  "unknown",
			authFiles: []string{},
			expected:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Profile{
				Provider:  tc.provider,
				Email:     "test@example.com",
				AuthFiles: tc.authFiles,
			}
			if got := p.IsValid(); got != tc.expected {
				t.Errorf("IsValid() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestProfileHasAuthFile(t *testing.T) {
	p := &Profile{
		AuthFiles: []string{"auth.json", ".claude.json"},
	}

	if !p.HasAuthFile("auth.json") {
		t.Error("expected HasAuthFile to return true for auth.json")
	}
	if !p.HasAuthFile(".claude.json") {
		t.Error("expected HasAuthFile to return true for .claude.json")
	}
	if p.HasAuthFile("missing.json") {
		t.Error("expected HasAuthFile to return false for missing file")
	}
}

func TestProfileGetAuthFilePath(t *testing.T) {
	p := &Profile{
		Path: "/path/to/profile",
	}

	expected := "/path/to/profile/auth.json"
	if got := p.GetAuthFilePath("auth.json"); got != expected {
		t.Errorf("GetAuthFilePath() = %s, want %s", got, expected)
	}
}

func TestProfileReadAuthFile(t *testing.T) {
	tmpDir := t.TempDir()
	authContent := `{"token": "secret123"}`
	writeFile(t, filepath.Join(tmpDir, "auth.json"), authContent)

	p := &Profile{
		Path:      tmpDir,
		AuthFiles: []string{"auth.json"},
	}

	// Read existing file
	data, err := p.ReadAuthFile("auth.json")
	if err != nil {
		t.Fatalf("ReadAuthFile failed: %v", err)
	}
	if string(data) != authContent {
		t.Errorf("ReadAuthFile() = %s, want %s", string(data), authContent)
	}

	// Read missing file
	_, err = p.ReadAuthFile("missing.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestMapCaamProvider(t *testing.T) {
	tests := []struct {
		caamProvider string
		expected     models.Provider
	}{
		{"claude", models.ProviderAnthropic},
		{"Claude", models.ProviderAnthropic},
		{"CLAUDE", models.ProviderAnthropic},
		{"codex", models.ProviderOpenAI},
		{"Codex", models.ProviderOpenAI},
		{"gemini", models.ProviderGoogle},
		{"Gemini", models.ProviderGoogle},
		{"unknown", models.ProviderCustom},
		{"other", models.ProviderCustom},
	}

	for _, tc := range tests {
		t.Run(tc.caamProvider, func(t *testing.T) {
			got := mapCaamProvider(tc.caamProvider)
			if got != tc.expected {
				t.Errorf("mapCaamProvider(%s) = %s, want %s", tc.caamProvider, got, tc.expected)
			}
		})
	}
}

func TestProfileToSwarmAccount(t *testing.T) {
	backupTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	p := &Profile{
		Provider: "claude",
		Email:    "alice@example.com",
		Path:     "/path/to/profile",
		Meta: &ProfileMeta{
			BackedUpAt: backupTime,
		},
		AuthFiles: []string{".claude.json", "auth.json"},
	}

	account := p.ToSwarmAccount()

	// Verify account fields
	if account.Provider != models.ProviderAnthropic {
		t.Errorf("expected provider %s, got %s", models.ProviderAnthropic, account.Provider)
	}

	if account.ProfileName != "alice@example.com" {
		t.Errorf("expected profile name 'alice@example.com', got %s", account.ProfileName)
	}

	if account.CredentialRef != "caam:claude/alice@example.com" {
		t.Errorf("expected credential ref 'caam:claude/alice@example.com', got %s", account.CredentialRef)
	}

	if !account.IsActive {
		t.Error("expected account to be active")
	}

	if !account.CreatedAt.Equal(backupTime) {
		t.Errorf("expected created_at %v, got %v", backupTime, account.CreatedAt)
	}
}

func TestProfileToSwarmAccountWithoutMeta(t *testing.T) {
	p := &Profile{
		Provider:  "codex",
		Email:     "bob@example.com",
		Path:      "/path/to/profile",
		AuthFiles: []string{"auth.json"},
	}

	account := p.ToSwarmAccount()

	if account.Provider != models.ProviderOpenAI {
		t.Errorf("expected provider %s, got %s", models.ProviderOpenAI, account.Provider)
	}

	if account.CredentialRef != "caam:codex/bob@example.com" {
		t.Errorf("expected credential ref 'caam:codex/bob@example.com', got %s", account.CredentialRef)
	}

	// CreatedAt should be recent (within 1 minute)
	if time.Since(account.CreatedAt) > time.Minute {
		t.Errorf("expected recent created_at, got %v", account.CreatedAt)
	}
}

func TestVaultConfigGetProfilesByProvider(t *testing.T) {
	config := &VaultConfig{
		Profiles: []*Profile{
			{Provider: "claude", Email: "a@example.com"},
			{Provider: "claude", Email: "b@example.com"},
			{Provider: "codex", Email: "c@example.com"},
		},
	}

	claudeProfiles := config.GetProfilesByProvider("claude")
	if len(claudeProfiles) != 2 {
		t.Errorf("expected 2 claude profiles, got %d", len(claudeProfiles))
	}

	// Case insensitive
	claudeProfiles = config.GetProfilesByProvider("CLAUDE")
	if len(claudeProfiles) != 2 {
		t.Errorf("expected 2 claude profiles (case insensitive), got %d", len(claudeProfiles))
	}

	codexProfiles := config.GetProfilesByProvider("codex")
	if len(codexProfiles) != 1 {
		t.Errorf("expected 1 codex profile, got %d", len(codexProfiles))
	}

	missingProfiles := config.GetProfilesByProvider("missing")
	if len(missingProfiles) != 0 {
		t.Errorf("expected 0 missing profiles, got %d", len(missingProfiles))
	}
}

func TestVaultConfigGetProfile(t *testing.T) {
	config := &VaultConfig{
		Profiles: []*Profile{
			{Provider: "claude", Email: "alice@example.com"},
			{Provider: "codex", Email: "bob@example.com"},
		},
	}

	// Find existing profile
	p := config.GetProfile("claude", "alice@example.com")
	if p == nil {
		t.Fatal("expected to find profile")
	}
	if p.Email != "alice@example.com" {
		t.Errorf("expected email 'alice@example.com', got %s", p.Email)
	}

	// Case insensitive provider
	p = config.GetProfile("CLAUDE", "alice@example.com")
	if p == nil {
		t.Fatal("expected to find profile (case insensitive)")
	}

	// Missing profile
	p = config.GetProfile("claude", "missing@example.com")
	if p != nil {
		t.Error("expected nil for missing profile")
	}
}

func TestVaultConfigToSwarmAccounts(t *testing.T) {
	config := &VaultConfig{
		Profiles: []*Profile{
			{
				Provider:  "claude",
				Email:     "valid@example.com",
				AuthFiles: []string{".claude.json"},
			},
			{
				Provider:  "claude",
				Email:     "invalid@example.com",
				AuthFiles: []string{}, // No auth files - invalid
			},
			{
				Provider:  "codex",
				Email:     "another@example.com",
				AuthFiles: []string{"auth.json"},
			},
		},
	}

	accounts := config.ToSwarmAccounts()

	// Should only include valid profiles
	if len(accounts) != 2 {
		t.Errorf("expected 2 accounts, got %d", len(accounts))
	}
}

func TestProfileMetaParsing(t *testing.T) {
	tmpDir := t.TempDir()
	profileDir := filepath.Join(tmpDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create meta.json with backup time and original paths
	meta := ProfileMeta{
		BackedUpAt: time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC),
		OriginalPaths: map[string]string{
			".claude.json": "/home/user/.claude.json",
			"auth.json":    "/home/user/.config/claude/auth.json",
		},
	}
	metaData, _ := json.Marshal(meta)
	writeFile(t, filepath.Join(profileDir, "meta.json"), string(metaData))
	writeFile(t, filepath.Join(profileDir, ".claude.json"), `{}`)

	config, err := ParseVault(tmpDir)
	if err != nil {
		t.Fatalf("ParseVault failed: %v", err)
	}

	if len(config.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(config.Profiles))
	}

	p := config.Profiles[0]
	if p.Meta == nil {
		t.Fatal("expected profile meta to be parsed")
	}

	if !p.Meta.BackedUpAt.Equal(meta.BackedUpAt) {
		t.Errorf("expected BackedUpAt %v, got %v", meta.BackedUpAt, p.Meta.BackedUpAt)
	}

	if len(p.Meta.OriginalPaths) != 2 {
		t.Errorf("expected 2 original paths, got %d", len(p.Meta.OriginalPaths))
	}
}

func TestParseVaultSkipsFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file at provider level (should be skipped)
	writeFile(t, filepath.Join(tmpDir, "readme.txt"), "should be ignored")

	// Create a file at profile level (should be skipped)
	providerDir := filepath.Join(tmpDir, "claude")
	if err := os.MkdirAll(providerDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(providerDir, "not-a-profile.txt"), "should be ignored")

	// Create an actual profile
	profileDir := filepath.Join(providerDir, "user@example.com")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(profileDir, ".claude.json"), `{}`)

	config, err := ParseVault(tmpDir)
	if err != nil {
		t.Fatalf("ParseVault failed: %v", err)
	}

	// Should only have 1 profile, not the files
	if len(config.Profiles) != 1 {
		t.Errorf("expected 1 profile, got %d", len(config.Profiles))
	}
}

// Helper function to write a file
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
