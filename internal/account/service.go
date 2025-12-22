// Package account provides account management with cooldown tracking.
package account

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/events"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/rs/zerolog"
)

// Service errors.
var (
	ErrAccountNotFound      = errors.New("account not found")
	ErrAccountAlreadyExists = errors.New("account already exists")
	ErrNoAvailableAccount   = errors.New("no available account")
	ErrAccountOnCooldown    = errors.New("account is on cooldown")
)

// Service manages accounts and their cooldown status.
type Service struct {
	mu              sync.RWMutex
	accounts        map[string]*models.Account
	defaultCooldown time.Duration
	repo            *db.AccountRepository
	publisher       events.Publisher
	logger          zerolog.Logger
}

// ServiceOption configures an account Service.
type ServiceOption func(*Service)

// WithRepository configures a repository for persistence.
func WithRepository(repo *db.AccountRepository) ServiceOption {
	return func(s *Service) {
		s.repo = repo
	}
}

// WithPublisher configures a publisher for event emission.
func WithPublisher(publisher events.Publisher) ServiceOption {
	return func(s *Service) {
		s.publisher = publisher
	}
}

// NewService creates a new account service from config.
func NewService(cfg *config.Config, opts ...ServiceOption) *Service {
	s := &Service{
		accounts:        make(map[string]*models.Account),
		defaultCooldown: cfg.Scheduler.DefaultCooldownDuration,
		logger:          logging.Component("account"),
	}
	for _, opt := range opts {
		opt(s)
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

// AddAccount adds a new account to the service.
func (s *Service) AddAccount(ctx context.Context, account *models.Account) error {
	if account == nil {
		return errors.New("account is nil")
	}
	if err := account.Validate(); err != nil {
		return err
	}

	if account.ID == "" {
		account.ID = account.ProfileName
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.accounts[account.ID]; exists {
		return ErrAccountAlreadyExists
	}

	now := time.Now().UTC()
	if account.CreatedAt.IsZero() {
		account.CreatedAt = now
	}
	account.UpdatedAt = now

	s.accounts[account.ID] = account
	return nil
}

// ListAccounts returns accounts filtered by provider when provided.
func (s *Service) ListAccounts(ctx context.Context, provider models.Provider) ([]*models.Account, error) {
	if provider == "" {
		return s.List(ctx), nil
	}
	return s.ListByProvider(ctx, provider), nil
}

// GetAccount retrieves an account by ID.
func (s *Service) GetAccount(ctx context.Context, id string) (*models.Account, error) {
	return s.Get(ctx, id)
}

// DeleteAccount removes an account by ID.
func (s *Service) DeleteAccount(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.accounts[id]; !exists {
		return ErrAccountNotFound
	}

	delete(s.accounts, id)
	return nil
}

// GetNextAvailable returns the next available account for a provider.
func (s *Service) GetNextAvailable(ctx context.Context, provider models.Provider) (*models.Account, error) {
	if provider == "" {
		return nil, models.ErrInvalidProvider
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var candidates []*models.Account
	for _, account := range s.accounts {
		if account.Provider == provider && account.IsAvailable() {
			candidates = append(candidates, account)
		}
	}

	if len(candidates) == 0 {
		return nil, ErrNoAvailableAccount
	}

	return selectLeastRecentlyUsed(candidates), nil
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
	_, err := s.applyCooldown(ctx, id, duration, true)
	return err
}

// SetCooldownForRateLimit applies the default cooldown after a rate limit.
func (s *Service) SetCooldownForRateLimit(ctx context.Context, id, reason string) error {
	account, err := s.applyCooldown(ctx, id, s.defaultCooldown, true)
	if err != nil {
		return err
	}

	s.publishRateLimitEvent(ctx, account, s.defaultCooldown, reason)
	return nil
}

func (s *Service) applyCooldown(ctx context.Context, id string, duration time.Duration, incrementRateLimit bool) (*models.Account, error) {
	if duration <= 0 {
		return nil, errors.New("cooldown duration must be positive")
	}

	s.mu.Lock()
	account, exists := s.accounts[id]
	if !exists {
		s.mu.Unlock()
		return nil, ErrAccountNotFound
	}

	now := time.Now().UTC()
	cooldownUntil := now.Add(duration)
	account.CooldownUntil = &cooldownUntil
	account.UpdatedAt = now

	if incrementRateLimit {
		if account.UsageStats == nil {
			account.UsageStats = &models.UsageStats{}
		}
		account.UsageStats.RateLimitCount++
	}

	snapshot := cloneAccount(account)
	s.mu.Unlock()

	if s.repo != nil {
		if err := s.repo.SetCooldown(ctx, id, cooldownUntil); err != nil {
			return snapshot, err
		}
	}

	s.logger.Info().
		Str("account_id", id).
		Dur("duration", duration).
		Time("cooldown_until", cooldownUntil).
		Msg("account placed on cooldown")

	return snapshot, nil
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
	return s.RotateAccountForAgent(ctx, currentID, "", "")
}

// RotateAccountForAgent finds an alternative account and emits a rotation event.
// If agentID is provided, it will be included in the event payload.
// The reason describes why the rotation occurred (e.g., "cooldown", "rate_limit").
func (s *Service) RotateAccountForAgent(ctx context.Context, currentID, agentID, reason string) (*models.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	current, exists := s.accounts[currentID]
	if !exists {
		return nil, ErrAccountNotFound
	}

	// Find another available account for the same provider
	var candidates []*models.Account
	for _, account := range s.accounts {
		if account.ID != currentID &&
			account.Provider == current.Provider &&
			account.IsAvailable() {
			candidates = append(candidates, account)
		}
	}

	if len(candidates) == 0 {
		return nil, ErrNoAvailableAccount
	}

	next := selectLeastRecentlyUsed(candidates)
	s.logger.Info().
		Str("from_account", currentID).
		Str("to_account", next.ID).
		Str("agent_id", agentID).
		Str("reason", reason).
		Msg("rotating account")

	// Emit rotation event
	if reason == "" {
		reason = "cooldown"
	}
	s.publishRotationEvent(ctx, agentID, currentID, next.ID, reason)

	return next, nil
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
//   - $VAR_NAME or ${VAR_NAME} - reads from environment variable VAR_NAME
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

	// Check for $VAR or ${VAR} env var reference
	if strings.HasPrefix(credentialRef, "$") {
		envVar := strings.TrimPrefix(credentialRef, "$")
		if strings.HasPrefix(envVar, "{") && strings.HasSuffix(envVar, "}") {
			envVar = strings.TrimPrefix(envVar, "{")
			envVar = strings.TrimSuffix(envVar, "}")
		}
		envVar = strings.TrimSpace(envVar)
		if envVar == "" {
			return "", errors.New("environment variable reference is empty")
		}
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

func (s *Service) publishRateLimitEvent(ctx context.Context, account *models.Account, duration time.Duration, reason string) {
	if s.publisher == nil {
		return
	}
	if account == nil {
		return
	}

	payload, err := json.Marshal(models.RateLimitPayload{
		AccountID:       account.ID,
		Provider:        account.Provider,
		CooldownSeconds: int(duration.Seconds()),
		Reason:          reason,
	})
	if err != nil {
		s.logger.Warn().Err(err).Str("account_id", account.ID).Msg("failed to marshal rate limit payload")
		return
	}

	s.publisher.Publish(ctx, &models.Event{
		Type:       models.EventTypeRateLimitDetected,
		EntityType: models.EntityTypeAccount,
		EntityID:   account.ID,
		Payload:    payload,
	})
}

func (s *Service) publishRotationEvent(ctx context.Context, agentID, oldAccountID, newAccountID, reason string) {
	if s.publisher == nil {
		return
	}

	payload, err := json.Marshal(models.AccountRotatedPayload{
		AgentID:      agentID,
		OldAccountID: oldAccountID,
		NewAccountID: newAccountID,
		Reason:       reason,
	})
	if err != nil {
		s.logger.Warn().Err(err).
			Str("old_account_id", oldAccountID).
			Str("new_account_id", newAccountID).
			Msg("failed to marshal rotation payload")
		return
	}

	// Use the new account ID as the entity ID since that's the active account now
	s.publisher.Publish(ctx, &models.Event{
		Type:       models.EventTypeAccountRotated,
		EntityType: models.EntityTypeAccount,
		EntityID:   newAccountID,
		Payload:    payload,
	})
}

func cloneAccount(account *models.Account) *models.Account {
	if account == nil {
		return nil
	}

	cloned := *account
	if account.CooldownUntil != nil {
		cooldown := *account.CooldownUntil
		cloned.CooldownUntil = &cooldown
	}
	if account.UsageStats != nil {
		usage := *account.UsageStats
		if account.UsageStats.LastUsed != nil {
			lastUsed := *account.UsageStats.LastUsed
			usage.LastUsed = &lastUsed
		}
		cloned.UsageStats = &usage
	}
	return &cloned
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

func selectLeastRecentlyUsed(accounts []*models.Account) *models.Account {
	if len(accounts) == 0 {
		return nil
	}

	sort.Slice(accounts, func(i, j int) bool {
		left := lastUsedTime(accounts[i])
		right := lastUsedTime(accounts[j])
		if left.Equal(right) {
			return accounts[i].ProfileName < accounts[j].ProfileName
		}
		return left.Before(right)
	})

	return accounts[0]
}

func lastUsedTime(account *models.Account) time.Time {
	if account == nil || account.UsageStats == nil || account.UsageStats.LastUsed == nil {
		return time.Time{}
	}
	return account.UsageStats.LastUsed.UTC()
}
