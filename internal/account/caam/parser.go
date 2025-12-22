// Package caam provides parsing for coding_agent_account_manager configuration.
package caam

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

// DefaultVaultPath returns the default caam vault location.
func DefaultVaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "caam", "vault")
}

// ProfileMeta contains metadata about a caam profile.
type ProfileMeta struct {
	// BackedUpAt is when the profile was backed up.
	BackedUpAt time.Time `json:"backed_up_at,omitempty"`

	// OriginalPaths maps file names to their original locations.
	OriginalPaths map[string]string `json:"original_paths,omitempty"`
}

// Profile represents a caam account profile.
type Profile struct {
	// Provider is the tool provider (claude, codex, gemini).
	Provider string

	// Email is the account email address.
	Email string

	// Path is the profile directory path.
	Path string

	// Meta contains profile metadata from meta.json.
	Meta *ProfileMeta

	// AuthFiles lists the auth file names present in the profile.
	AuthFiles []string
}

// VaultConfig represents a parsed caam vault.
type VaultConfig struct {
	// Path is the vault root directory.
	Path string

	// Profiles contains all discovered profiles.
	Profiles []*Profile
}

// ParseVault reads and parses a caam vault directory.
func ParseVault(vaultPath string) (*VaultConfig, error) {
	info, err := os.Stat(vaultPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("caam vault not found at %s", vaultPath)
		}
		return nil, fmt.Errorf("failed to access vault: %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("vault path is not a directory: %s", vaultPath)
	}

	config := &VaultConfig{
		Path:     vaultPath,
		Profiles: make([]*Profile, 0),
	}

	// List provider directories
	providers, err := os.ReadDir(vaultPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read vault: %w", err)
	}

	for _, provider := range providers {
		if !provider.IsDir() {
			continue
		}

		providerName := provider.Name()
		providerPath := filepath.Join(vaultPath, providerName)

		// List profile directories within provider
		profiles, err := os.ReadDir(providerPath)
		if err != nil {
			continue // Skip unreadable provider directories
		}

		for _, profile := range profiles {
			if !profile.IsDir() {
				continue
			}

			email := profile.Name()
			profilePath := filepath.Join(providerPath, email)

			p := &Profile{
				Provider:  providerName,
				Email:     email,
				Path:      profilePath,
				AuthFiles: make([]string, 0),
			}

			// Parse meta.json if present
			metaPath := filepath.Join(profilePath, "meta.json")
			if metaData, err := os.ReadFile(metaPath); err == nil {
				var meta ProfileMeta
				if err := json.Unmarshal(metaData, &meta); err == nil {
					p.Meta = &meta
				}
			}

			// List auth files
			files, err := os.ReadDir(profilePath)
			if err != nil {
				continue
			}

			for _, f := range files {
				if f.IsDir() || f.Name() == "meta.json" {
					continue
				}
				p.AuthFiles = append(p.AuthFiles, f.Name())
			}

			config.Profiles = append(config.Profiles, p)
		}
	}

	return config, nil
}

// ToSwarmAccount converts a caam profile to a Swarm Account model.
func (p *Profile) ToSwarmAccount() *models.Account {
	provider := mapCaamProvider(p.Provider)

	// Use email as profile name (caam convention)
	profileName := p.Email

	// Create credential reference pointing to caam vault
	credRef := fmt.Sprintf("caam:%s/%s", p.Provider, p.Email)

	account := &models.Account{
		Provider:      provider,
		ProfileName:   profileName,
		CredentialRef: credRef,
		IsActive:      true,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	// If we have metadata with backup time, use that as created time
	if p.Meta != nil && !p.Meta.BackedUpAt.IsZero() {
		account.CreatedAt = p.Meta.BackedUpAt
	}

	return account
}

// mapCaamProvider maps caam provider names to Swarm provider types.
func mapCaamProvider(caamProvider string) models.Provider {
	switch strings.ToLower(caamProvider) {
	case "claude":
		return models.ProviderAnthropic
	case "codex":
		return models.ProviderOpenAI
	case "gemini":
		return models.ProviderGoogle
	default:
		return models.ProviderCustom
	}
}

// GetAuthFilePath returns the path to a specific auth file in the profile.
func (p *Profile) GetAuthFilePath(filename string) string {
	return filepath.Join(p.Path, filename)
}

// HasAuthFile checks if the profile has a specific auth file.
func (p *Profile) HasAuthFile(filename string) bool {
	for _, f := range p.AuthFiles {
		if f == filename {
			return true
		}
	}
	return false
}

// ReadAuthFile reads the contents of an auth file from the profile.
func (p *Profile) ReadAuthFile(filename string) ([]byte, error) {
	if !p.HasAuthFile(filename) {
		return nil, fmt.Errorf("auth file %q not found in profile", filename)
	}
	return os.ReadFile(p.GetAuthFilePath(filename))
}

// IsValid checks if the profile has the required auth files for its provider.
func (p *Profile) IsValid() bool {
	switch strings.ToLower(p.Provider) {
	case "claude":
		// Claude needs at least one of the auth files
		return p.HasAuthFile(".claude.json") || p.HasAuthFile("auth.json")
	case "codex":
		return p.HasAuthFile("auth.json")
	case "gemini":
		return p.HasAuthFile("settings.json")
	default:
		// Unknown providers just need at least one auth file
		return len(p.AuthFiles) > 0
	}
}

// ListProviders returns a list of unique providers in the vault.
func (c *VaultConfig) ListProviders() []string {
	providerSet := make(map[string]struct{})
	for _, p := range c.Profiles {
		providerSet[p.Provider] = struct{}{}
	}

	providers := make([]string, 0, len(providerSet))
	for p := range providerSet {
		providers = append(providers, p)
	}
	return providers
}

// GetProfilesByProvider returns profiles for a specific provider.
func (c *VaultConfig) GetProfilesByProvider(provider string) []*Profile {
	var profiles []*Profile
	for _, p := range c.Profiles {
		if strings.EqualFold(p.Provider, provider) {
			profiles = append(profiles, p)
		}
	}
	return profiles
}

// GetProfile returns a specific profile by provider and email.
func (c *VaultConfig) GetProfile(provider, email string) *Profile {
	for _, p := range c.Profiles {
		if strings.EqualFold(p.Provider, provider) && p.Email == email {
			return p
		}
	}
	return nil
}

// ToSwarmAccounts converts all valid profiles to Swarm Account models.
func (c *VaultConfig) ToSwarmAccounts() []*models.Account {
	accounts := make([]*models.Account, 0, len(c.Profiles))
	for _, p := range c.Profiles {
		if p.IsValid() {
			accounts = append(accounts, p.ToSwarmAccount())
		}
	}
	return accounts
}
