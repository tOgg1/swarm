// Package account provides account management with cooldown tracking.
package account

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/events"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/vault"
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
	vaultPath       string // Path to the native credential vault
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

// WithVaultPath configures the path to the native credential vault.
// If not set, the default vault path (~/.config/swarm/vault) is used.
func WithVaultPath(path string) ServiceOption {
	return func(s *Service) {
		s.vaultPath = path
	}
}

// NewService creates a new account service from config.
func NewService(cfg *config.Config, opts ...ServiceOption) *Service {
	s := &Service{
		accounts:        make(map[string]*models.Account),
		defaultCooldown: cfg.Scheduler.DefaultCooldownDuration,
		logger:          logging.Component("account"),
		vaultPath:       vault.DefaultVaultPath(),
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
	account, exists := s.accounts[id]
	if !exists {
		s.mu.Unlock()
		return ErrAccountNotFound
	}
	hadCooldown := account.CooldownUntil != nil
	account.CooldownUntil = nil
	account.UpdatedAt = time.Now().UTC()
	snapshot := cloneAccount(account)
	s.mu.Unlock()

	if s.repo != nil {
		if err := s.repo.ClearCooldown(ctx, id); err != nil {
			return err
		}
	}

	if hadCooldown {
		s.publishCooldownEndedEvent(ctx, snapshot)
	}

	s.logger.Debug().Str("account_id", id).Msg("account cooldown cleared")
	return nil
}

// SweepExpiredCooldowns clears any cooldowns that have expired.
// Returns the number of accounts cleared and the first error encountered.
func (s *Service) SweepExpiredCooldowns(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	s.mu.RLock()
	var expired []string
	for id, account := range s.accounts {
		if account.CooldownUntil != nil && now.After(*account.CooldownUntil) {
			expired = append(expired, id)
		}
	}
	s.mu.RUnlock()

	var cleared int
	var firstErr error
	for _, id := range expired {
		if err := s.ClearCooldown(ctx, id); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.logger.Warn().Err(err).Str("account_id", id).Msg("failed to clear expired cooldown")
			continue
		}
		cleared++
	}

	return cleared, firstErr
}

// StartCooldownMonitor launches a background loop that clears expired cooldowns.
func (s *Service) StartCooldownMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.SweepExpiredCooldowns(ctx); err != nil && !errors.Is(err, context.Canceled) {
					s.logger.Warn().Err(err).Msg("cooldown sweep failed")
				}
			}
		}
	}()
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
//   - vault:adapter/profile - reads from native Swarm vault (recommended)
//   - caam:provider/email - reads from legacy caam vault (deprecated)
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

	// Check for vault: prefix (native Swarm vault - recommended)
	if strings.HasPrefix(credentialRef, "vault:") {
		return resolveVaultCredential(strings.TrimPrefix(credentialRef, "vault:"))
	}

	// Check for caam: prefix (coding_agent_account_manager vault - deprecated)
	if strings.HasPrefix(credentialRef, "caam:") {
		return resolveCaamCredential(strings.TrimPrefix(credentialRef, "caam:"))
	}

	// Treat as literal value (for backwards compatibility)
	return credentialRef, nil
}

// resolveVaultCredential resolves a credential from the native Swarm vault.
// The ref format is "adapter/profile", e.g., "claude/work" or "anthropic/personal".
func resolveVaultCredential(ref string) (string, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid vault credential reference format: expected adapter/profile")
	}
	adapterName := parts[0]
	profileName := parts[1]

	// Import vault package functions inline to avoid circular dependency
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("failed to determine home directory: " + err.Error())
	}
	vaultPath := filepath.Join(home, ".config", "swarm", "vault")

	// Map adapter name to provider directory
	var providerDir string
	switch strings.ToLower(adapterName) {
	case "claude", "anthropic":
		providerDir = "anthropic"
	case "codex", "openai":
		providerDir = "openai"
	case "gemini", "google":
		providerDir = "google"
	case "opencode":
		providerDir = "opencode"
	default:
		return "", errors.New("unknown adapter: " + adapterName)
	}

	profilePath := filepath.Join(vaultPath, "profiles", providerDir, profileName)

	// Check if profile exists
	info, err := os.Stat(profilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("vault profile not found: " + ref + " (run 'swarm vault backup " + adapterName + " " + profileName + "' first)")
		}
		return "", errors.New("failed to access vault profile: " + err.Error())
	}
	if !info.IsDir() {
		return "", errors.New("vault profile path is not a directory: " + ref)
	}

	// Try to read auth files and extract API key
	var authFiles []string
	switch providerDir {
	case "anthropic":
		authFiles = []string{".claude.json", "auth.json"}
	case "openai":
		authFiles = []string{"auth.json"}
	case "google":
		authFiles = []string{"settings.json"}
	case "opencode":
		authFiles = []string{"auth.json"}
	}

	// Try each auth file
	for _, authFile := range authFiles {
		authPath := filepath.Join(profilePath, authFile)
		data, err := os.ReadFile(authPath)
		if err != nil {
			continue // Try next file
		}

		// Parse JSON to extract API key/token
		var authData map[string]interface{}
		if err := json.Unmarshal(data, &authData); err != nil {
			continue // Try next file
		}

		// Look for common credential field names
		for _, key := range []string{"api_key", "apiKey", "token", "accessToken", "access_token", "key", "claudeApiKey"} {
			if val, ok := authData[key]; ok {
				if s, ok := val.(string); ok && s != "" {
					return s, nil
				}
			}
		}

		// For Claude, also check nested structures
		if providerDir == "anthropic" {
			// Check for oauthAccount.claudeApiKey pattern
			if oauth, ok := authData["oauthAccount"].(map[string]interface{}); ok {
				if apiKey, ok := oauth["claudeApiKey"].(string); ok && apiKey != "" {
					return apiKey, nil
				}
			}
		}
	}

	return "", errors.New("no API key found in vault profile auth files for: " + ref)
}

// resolveCaamCredential resolves a credential from a caam vault.
// The ref format is "provider/email", e.g., "claude/user@example.com".
func resolveCaamCredential(ref string) (string, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid caam credential reference format: expected provider/email")
	}
	provider := parts[0]
	email := parts[1]

	// Get caam vault path
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("failed to determine home directory: " + err.Error())
	}
	vaultPath := filepath.Join(home, ".local", "share", "caam", "vault")
	profilePath := filepath.Join(vaultPath, provider, email)

	// Check if profile exists
	info, err := os.Stat(profilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("caam profile not found: " + ref)
		}
		return "", errors.New("failed to access caam profile: " + err.Error())
	}
	if !info.IsDir() {
		return "", errors.New("caam profile path is not a directory: " + ref)
	}

	// Try to read auth files based on provider
	var authFile string
	switch strings.ToLower(provider) {
	case "claude":
		// Claude uses auth.json or .claude.json
		authFile = filepath.Join(profilePath, "auth.json")
		if _, err := os.Stat(authFile); os.IsNotExist(err) {
			authFile = filepath.Join(profilePath, ".claude.json")
		}
	case "codex":
		authFile = filepath.Join(profilePath, "auth.json")
	case "gemini":
		authFile = filepath.Join(profilePath, "settings.json")
	default:
		// Unknown provider - try auth.json
		authFile = filepath.Join(profilePath, "auth.json")
	}

	data, err := os.ReadFile(authFile)
	if err != nil {
		return "", errors.New("failed to read caam auth file: " + err.Error())
	}

	// Parse JSON to extract API key/token
	var authData map[string]interface{}
	if err := json.Unmarshal(data, &authData); err != nil {
		return "", errors.New("failed to parse caam auth file: " + err.Error())
	}

	// Look for common credential field names
	for _, key := range []string{"api_key", "apiKey", "token", "accessToken", "access_token", "key"} {
		if val, ok := authData[key]; ok {
			if s, ok := val.(string); ok && s != "" {
				return s, nil
			}
		}
	}

	return "", errors.New("no API key found in caam auth file")
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

func (s *Service) publishCooldownEndedEvent(ctx context.Context, account *models.Account) {
	if s.publisher == nil {
		return
	}
	if account == nil {
		return
	}

	s.publisher.Publish(ctx, &models.Event{
		Type:       models.EventTypeCooldownEnded,
		EntityType: models.EntityTypeAccount,
		EntityID:   account.ID,
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

// VaultPath returns the configured vault path.
func (s *Service) VaultPath() string {
	return s.vaultPath
}

// SyncFromVault imports vault profiles as accounts.
// This creates account entries for each vault profile with credential references
// in the format "vault:adapter/profile".
// Returns the number of accounts synced and any error encountered.
func (s *Service) SyncFromVault(ctx context.Context) (int, error) {
	profiles, err := vault.List(s.vaultPath, "")
	if err != nil {
		return 0, err
	}

	var synced int
	for _, profile := range profiles {
		provider := adapterToProvider(profile.Adapter)
		if provider == "" {
			s.logger.Warn().
				Str("adapter", string(profile.Adapter)).
				Str("profile", profile.Name).
				Msg("unknown adapter, skipping vault profile")
			continue
		}

		// Generate account ID: provider/profile
		accountID := string(profile.Adapter) + "/" + profile.Name
		credentialRef := "vault:" + string(profile.Adapter) + "/" + profile.Name

		s.mu.Lock()
		if existing, exists := s.accounts[accountID]; exists {
			// Update existing account
			existing.CredentialRef = credentialRef
			existing.UpdatedAt = time.Now().UTC()
			s.mu.Unlock()
			continue
		}

		// Create new account
		account := &models.Account{
			ID:            accountID,
			Provider:      provider,
			ProfileName:   profile.Name,
			CredentialRef: credentialRef,
			IsActive:      true,
			CreatedAt:     profile.CreatedAt,
			UpdatedAt:     time.Now().UTC(),
		}
		s.accounts[accountID] = account
		s.mu.Unlock()
		synced++

		s.logger.Info().
			Str("account_id", accountID).
			Str("provider", string(provider)).
			Str("profile", profile.Name).
			Msg("synced vault profile as account")
	}

	return synced, nil
}

// ActivateVaultProfile activates a vault profile for an adapter.
// This is typically called before spawning an agent to ensure the correct
// credentials are in place.
func (s *Service) ActivateVaultProfile(ctx context.Context, adapter vault.Adapter, profileName string) error {
	if err := vault.Activate(s.vaultPath, adapter, profileName); err != nil {
		return err
	}

	s.logger.Debug().
		Str("adapter", string(adapter)).
		Str("profile", profileName).
		Msg("activated vault profile")

	return nil
}

// ActivateForAgent activates the vault profile for an account and returns
// the credential environment variables needed for the agent.
// If the account uses a vault: credential reference, the profile is activated first.
func (s *Service) ActivateForAgent(ctx context.Context, accountID, agentID string) (map[string]string, error) {
	s.mu.RLock()
	account, exists := s.accounts[accountID]
	if !exists {
		s.mu.RUnlock()
		return nil, ErrAccountNotFound
	}
	credRef := account.CredentialRef
	s.mu.RUnlock()

	// Check if this is a vault credential reference
	if strings.HasPrefix(credRef, "vault:") {
		ref := strings.TrimPrefix(credRef, "vault:")
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) == 2 {
			adapter := vault.ParseAdapter(parts[0])
			profileName := parts[1]
			if adapter != "" {
				if err := s.ActivateVaultProfile(ctx, adapter, profileName); err != nil {
					return nil, err
				}
				s.logger.Info().
					Str("account_id", accountID).
					Str("agent_id", agentID).
					Str("adapter", string(adapter)).
					Str("profile", profileName).
					Msg("activated vault profile for agent")
			}
		}
	}

	// Get credential environment
	return s.GetCredentialEnv(ctx, accountID)
}

// GetActiveVaultProfile returns the currently active vault profile for an adapter.
func (s *Service) GetActiveVaultProfile(ctx context.Context, adapter vault.Adapter) (*vault.Profile, error) {
	return vault.GetActive(s.vaultPath, adapter)
}

// ListVaultProfiles returns all vault profiles, optionally filtered by adapter.
func (s *Service) ListVaultProfiles(ctx context.Context, adapter vault.Adapter) ([]*vault.Profile, error) {
	return vault.List(s.vaultPath, adapter)
}

// adapterToProvider maps vault adapters to account providers.
func adapterToProvider(adapter vault.Adapter) models.Provider {
	switch adapter {
	case vault.AdapterClaude:
		return models.ProviderAnthropic
	case vault.AdapterCodex:
		return models.ProviderOpenAI
	case vault.AdapterGemini:
		return models.ProviderGoogle
	case vault.AdapterOpenCode:
		return models.ProviderCustom
	default:
		return ""
	}
}
