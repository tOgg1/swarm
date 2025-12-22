// Package account provides account management with cooldown tracking.
package account

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/rs/zerolog"
)

// Service errors.
var (
	ErrAccountNotFound    = errors.New("account not found")
	ErrNoAvailableAccount = errors.New("no available account")
	ErrAccountOnCooldown  = errors.New("account is on cooldown")
)

// Service manages accounts and their cooldown status.
type Service struct {
	mu              sync.RWMutex
	accounts        map[string]*models.Account
	defaultCooldown time.Duration
	logger          zerolog.Logger
}

// NewService creates a new account service from config.
func NewService(cfg *config.Config) *Service {
	s := &Service{
		accounts:        make(map[string]*models.Account),
		defaultCooldown: cfg.Scheduler.DefaultCooldownDuration,
		logger:          logging.Component("account"),
	}

	// Load accounts from config
	for _, acct := range cfg.Accounts {
		account := &models.Account{
			ID:            acct.ProfileName, // Use profile name as ID
			Provider:      acct.Provider,
			ProfileName:   acct.ProfileName,
			CredentialRef: acct.CredentialRef,
			IsActive:      acct.IsActive,
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		}
		s.accounts[account.ID] = account
	}

	return s
}

// Get retrieves an account by ID.
func (s *Service) Get(ctx context.Context, id string) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	account, exists := s.accounts[id]
	if !exists {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

// List returns all accounts.
func (s *Service) List(ctx context.Context) []*models.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()

	accounts := make([]*models.Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		accounts = append(accounts, account)
	}
	return accounts
}

// ListAvailable returns all accounts that are not on cooldown.
func (s *Service) ListAvailable(ctx context.Context) []*models.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var available []*models.Account
	for _, account := range s.accounts {
		if account.IsAvailable() {
			available = append(available, account)
		}
	}
	return available
}

// ListByProvider returns accounts for a specific provider.
func (s *Service) ListByProvider(ctx context.Context, provider models.Provider) []*models.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var accounts []*models.Account
	for _, account := range s.accounts {
		if account.Provider == provider {
			accounts = append(accounts, account)
		}
	}
	return accounts
}

// IsOnCooldown checks if an account is currently on cooldown.
func (s *Service) IsOnCooldown(ctx context.Context, id string) (bool, time.Duration, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	account, exists := s.accounts[id]
	if !exists {
		return false, 0, ErrAccountNotFound
	}

	if account.IsOnCooldown() {
		return true, account.CooldownRemaining(), nil
	}
	return false, 0, nil
}

// SetCooldown puts an account on cooldown.
func (s *Service) SetCooldown(ctx context.Context, id string, duration time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	account, exists := s.accounts[id]
	if !exists {
		return ErrAccountNotFound
	}

	cooldownUntil := time.Now().Add(duration)
	account.CooldownUntil = &cooldownUntil
	account.UpdatedAt = time.Now().UTC()

	if account.UsageStats == nil {
		account.UsageStats = &models.UsageStats{}
	}
	account.UsageStats.RateLimitCount++

	s.logger.Info().
		Str("account_id", id).
		Dur("duration", duration).
		Time("cooldown_until", cooldownUntil).
		Msg("account placed on cooldown")

	return nil
}

// ClearCooldown removes cooldown from an account.
func (s *Service) ClearCooldown(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	account, exists := s.accounts[id]
	if !exists {
		return ErrAccountNotFound
	}

	account.CooldownUntil = nil
	account.UpdatedAt = time.Now().UTC()

	s.logger.Debug().Str("account_id", id).Msg("account cooldown cleared")
	return nil
}

// GetAvailable returns an available account for a provider.
// Returns ErrNoAvailableAccount if all accounts are on cooldown.
func (s *Service) GetAvailable(ctx context.Context, provider models.Provider) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, account := range s.accounts {
		if account.Provider == provider && account.IsAvailable() {
			return account, nil
		}
	}
	return nil, ErrNoAvailableAccount
}

// RotateAccount finds an alternative account when the current one is on cooldown.
// Returns the new account or ErrNoAvailableAccount if none available.
func (s *Service) RotateAccount(ctx context.Context, currentID string) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	current, exists := s.accounts[currentID]
	if !exists {
		return nil, ErrAccountNotFound
	}

	// Find another available account for the same provider
	for _, account := range s.accounts {
		if account.ID != currentID &&
			account.Provider == current.Provider &&
			account.IsAvailable() {
			s.logger.Info().
				Str("from_account", currentID).
				Str("to_account", account.ID).
				Msg("rotating account")
			return account, nil
		}
	}

	return nil, ErrNoAvailableAccount
}

// RecordUsage records usage for an account.
func (s *Service) RecordUsage(ctx context.Context, id string, tokens int64, costCents int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	account, exists := s.accounts[id]
	if !exists {
		return ErrAccountNotFound
	}

	if account.UsageStats == nil {
		account.UsageStats = &models.UsageStats{}
	}

	now := time.Now().UTC()
	account.UsageStats.TotalTokens += tokens
	account.UsageStats.TotalCostCents += costCents
	account.UsageStats.RequestCount++
	account.UsageStats.LastUsed = &now
	account.UpdatedAt = now

	return nil
}

// CheckAndWaitCooldown checks if an account is on cooldown and optionally waits.
// Returns nil if account is available, or rotates to another account if possible.
// If waitMax > 0 and no rotation possible, waits up to waitMax for cooldown to expire.
func (s *Service) CheckAndWaitCooldown(ctx context.Context, id string, waitMax time.Duration) (*models.Account, error) {
	onCooldown, remaining, err := s.IsOnCooldown(ctx, id)
	if err != nil {
		return nil, err
	}

	if !onCooldown {
		// Account is available
		account, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return account, nil
	}

	// Account is on cooldown, try to rotate
	rotated, err := s.RotateAccount(ctx, id)
	if err == nil {
		return rotated, nil
	}

	// No rotation possible, check if we should wait
	if waitMax <= 0 || remaining > waitMax {
		return nil, ErrAccountOnCooldown
	}

	// Wait for cooldown
	s.logger.Debug().
		Str("account_id", id).
		Dur("wait_time", remaining).
		Msg("waiting for cooldown to expire")

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(remaining):
		// Cooldown should be over
		account, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return account, nil
	}
}

// ProviderEnvVar returns the environment variable name for a provider's API key.
func ProviderEnvVar(provider models.Provider) string {
	switch provider {
	case models.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case models.ProviderOpenAI:
		return "OPENAI_API_KEY"
	case models.ProviderGoogle:
		return "GOOGLE_API_KEY"
	default:
		return ""
	}
}

// ResolveCredential resolves a credential reference to its actual value.
// Credential references can be:
//   - env:VAR_NAME - reads from environment variable VAR_NAME
//   - file:/path/to/file - reads from file
//   - literal value - used as-is (not recommended for production)
func ResolveCredential(credentialRef string) (string, error) {
	if credentialRef == "" {
		return "", errors.New("empty credential reference")
	}

	// Check for env: prefix
	if strings.HasPrefix(credentialRef, "env:") {
		envVar := strings.TrimPrefix(credentialRef, "env:")
		value := os.Getenv(envVar)
		if value == "" {
			return "", errors.New("environment variable " + envVar + " is not set")
		}
		return value, nil
	}

	// Check for file: prefix
	if strings.HasPrefix(credentialRef, "file:") {
		filePath := strings.TrimPrefix(credentialRef, "file:")
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", errors.New("failed to read credential file: " + err.Error())
		}
		return strings.TrimSpace(string(data)), nil
	}

	// Treat as literal value (for backwards compatibility)
	return credentialRef, nil
}

// GetCredentialEnv returns the environment variable map for an account's credentials.
// This can be passed to agent spawn to inject the correct API key.
func (s *Service) GetCredentialEnv(ctx context.Context, accountID string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	account, exists := s.accounts[accountID]
	if !exists {
		return nil, ErrAccountNotFound
	}

	envVar := ProviderEnvVar(account.Provider)
	if envVar == "" {
		// Custom provider - no standard env var
		s.logger.Debug().
			Str("account_id", accountID).
			Str("provider", string(account.Provider)).
			Msg("no standard env var for provider")
		return map[string]string{}, nil
	}

	credential, err := ResolveCredential(account.CredentialRef)
	if err != nil {
		return nil, err
	}

	return map[string]string{
		envVar: credential,
	}, nil
}

// MergeEnv merges credential environment variables with existing environment.
// Credential values take precedence over existing values.
func MergeEnv(base map[string]string, credentials map[string]string) map[string]string {
	if base == nil {
		base = make(map[string]string)
	}

	result := make(map[string]string, len(base)+len(credentials))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range credentials {
		result[k] = v
	}

	return result
}
