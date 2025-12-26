package account

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/events"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/vault"
)

func TestResolveCredential_EnvPrefix(t *testing.T) {
	t.Setenv("TEST_KEY", "value")

	got, err := ResolveCredential("env:TEST_KEY")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "value" {
		t.Fatalf("expected value, got %q", got)
	}
}

func TestResolveCredential_DollarVar(t *testing.T) {
	t.Setenv("TEST_KEY", "value")

	got, err := ResolveCredential("$TEST_KEY")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "value" {
		t.Fatalf("expected value, got %q", got)
	}
}

func TestResolveCredential_DollarVarBraced(t *testing.T) {
	t.Setenv("TEST_KEY", "value")

	got, err := ResolveCredential("${TEST_KEY}")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "value" {
		t.Fatalf("expected value, got %q", got)
	}
}

func TestResolveCredential_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(path, []byte("secret\n"), 0600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	got, err := ResolveCredential("file:" + path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "secret" {
		t.Fatalf("expected secret, got %q", got)
	}
}

func TestResolveCredential_Literal(t *testing.T) {
	got, err := ResolveCredential("literal")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "literal" {
		t.Fatalf("expected literal, got %q", got)
	}
}

func TestService_AddGetListDelete(t *testing.T) {
	cfg := config.DefaultConfig()
	service := NewService(cfg)
	ctx := context.Background()

	account := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "primary",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
	}

	if err := service.AddAccount(ctx, account); err != nil {
		t.Fatalf("AddAccount failed: %v", err)
	}

	if err := service.AddAccount(ctx, account); !errors.Is(err, ErrAccountAlreadyExists) {
		t.Fatalf("expected ErrAccountAlreadyExists, got %v", err)
	}

	got, err := service.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if got.ProfileName != "primary" {
		t.Fatalf("unexpected profile name: %s", got.ProfileName)
	}

	all, err := service.ListAccounts(ctx, "")
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 account, got %d", len(all))
	}

	if err := service.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatalf("DeleteAccount failed: %v", err)
	}

	if _, err := service.GetAccount(ctx, account.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound, got %v", err)
	}
}

func TestService_GetNextAvailable(t *testing.T) {
	cfg := config.DefaultConfig()
	service := NewService(cfg)
	ctx := context.Background()

	now := time.Now().UTC()

	account1 := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "a",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
		UsageStats: &models.UsageStats{
			LastUsed: &now,
		},
	}
	account2 := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "b",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
		UsageStats: &models.UsageStats{
			LastUsed: timePtr(now.Add(-1 * time.Hour)),
		},
	}
	account3 := &models.Account{
		Provider:      models.ProviderAnthropic,
		ProfileName:   "x",
		CredentialRef: "env:ANTHROPIC_API_KEY",
		IsActive:      true,
	}

	for _, acct := range []*models.Account{account1, account2, account3} {
		if err := service.AddAccount(ctx, acct); err != nil {
			t.Fatalf("AddAccount failed: %v", err)
		}
	}

	got, err := service.GetNextAvailable(ctx, models.ProviderOpenAI)
	if err != nil {
		t.Fatalf("GetNextAvailable failed: %v", err)
	}
	if got.ProfileName != "b" {
		t.Fatalf("expected profile b, got %s", got.ProfileName)
	}

	if _, err := service.GetNextAvailable(ctx, models.Provider("")); !errors.Is(err, models.ErrInvalidProvider) {
		t.Fatalf("expected ErrInvalidProvider, got %v", err)
	}
}

func TestService_RotateAccount_LRU(t *testing.T) {
	cfg := config.DefaultConfig()
	service := NewService(cfg)
	ctx := context.Background()

	now := time.Now().UTC()

	current := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "current",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
		UsageStats: &models.UsageStats{
			LastUsed: timePtr(now.Add(-30 * time.Minute)),
		},
	}
	oldest := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "old",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
		UsageStats: &models.UsageStats{
			LastUsed: timePtr(now.Add(-2 * time.Hour)),
		},
	}
	newer := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "new",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
		UsageStats: &models.UsageStats{
			LastUsed: timePtr(now.Add(-1 * time.Hour)),
		},
	}

	for _, acct := range []*models.Account{current, oldest, newer} {
		if err := service.AddAccount(ctx, acct); err != nil {
			t.Fatalf("AddAccount failed: %v", err)
		}
	}

	got, err := service.RotateAccount(ctx, current.ID)
	if err != nil {
		t.Fatalf("RotateAccount failed: %v", err)
	}
	if got.ProfileName != "old" {
		t.Fatalf("expected profile old, got %s", got.ProfileName)
	}
}

type testPublisher struct {
	events []*models.Event
}

func (p *testPublisher) Publish(ctx context.Context, event *models.Event) {
	p.events = append(p.events, event)
}

func (p *testPublisher) Subscribe(id string, filter events.Filter, handler events.EventHandler) error {
	return nil
}

func (p *testPublisher) Unsubscribe(id string) error {
	return nil
}

func (p *testPublisher) SubscriberCount() int {
	return len(p.events)
}

func TestService_SetCooldownForRateLimit(t *testing.T) {
	cfg := config.DefaultConfig()
	ctx := context.Background()

	database, err := db.OpenInMemory()
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("failed to migrate database: %v", err)
	}

	repo := db.NewAccountRepository(database)
	publisher := &testPublisher{}
	service := NewService(cfg, WithRepository(repo), WithPublisher(publisher))

	account := &models.Account{
		ID:            "acct-1",
		Provider:      models.ProviderOpenAI,
		ProfileName:   "acct-1",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
	}

	if err := repo.Create(ctx, account); err != nil {
		t.Fatalf("failed to create account in repo: %v", err)
	}
	if err := service.AddAccount(ctx, account); err != nil {
		t.Fatalf("AddAccount failed: %v", err)
	}

	reason := "HTTP 429 rate limit"
	if err := service.SetCooldownForRateLimit(ctx, account.ID, reason); err != nil {
		t.Fatalf("SetCooldownForRateLimit failed: %v", err)
	}

	if account.CooldownUntil == nil || !account.IsOnCooldown() {
		t.Fatal("expected account to be on cooldown")
	}
	if account.UsageStats == nil || account.UsageStats.RateLimitCount != 1 {
		t.Fatalf("expected RateLimitCount to be 1, got %#v", account.UsageStats)
	}

	stored, err := repo.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("failed to get account from repo: %v", err)
	}
	if stored.CooldownUntil == nil || !stored.IsOnCooldown() {
		t.Fatal("expected stored account to be on cooldown")
	}

	if len(publisher.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(publisher.events))
	}
	event := publisher.events[0]
	if event.Type != models.EventTypeRateLimitDetected {
		t.Fatalf("expected rate limit event, got %s", event.Type)
	}
	if event.EntityType != models.EntityTypeAccount || event.EntityID != account.ID {
		t.Fatalf("unexpected event entity: %s %s", event.EntityType, event.EntityID)
	}

	var payload models.RateLimitPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.AccountID != account.ID || payload.Provider != account.Provider {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if payload.Reason != reason {
		t.Fatalf("expected reason %q, got %q", reason, payload.Reason)
	}
	expectedCooldown := int(cfg.Scheduler.DefaultCooldownDuration.Seconds())
	if payload.CooldownSeconds != expectedCooldown {
		t.Fatalf("expected cooldown seconds %d, got %d", expectedCooldown, payload.CooldownSeconds)
	}
}

func TestService_SweepExpiredCooldowns(t *testing.T) {
	cfg := config.DefaultConfig()
	ctx := context.Background()

	database, err := db.OpenInMemory()
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("failed to migrate database: %v", err)
	}

	repo := db.NewAccountRepository(database)
	publisher := &testPublisher{}
	service := NewService(cfg, WithRepository(repo), WithPublisher(publisher))

	expired := time.Now().UTC().Add(-1 * time.Minute)
	account := &models.Account{
		ID:            "acct-expired",
		Provider:      models.ProviderOpenAI,
		ProfileName:   "acct-expired",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
		CooldownUntil: &expired,
	}

	if err := repo.Create(ctx, account); err != nil {
		t.Fatalf("failed to create account in repo: %v", err)
	}
	if err := service.AddAccount(ctx, account); err != nil {
		t.Fatalf("AddAccount failed: %v", err)
	}

	cleared, err := service.SweepExpiredCooldowns(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredCooldowns failed: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("expected 1 cooldown cleared, got %d", cleared)
	}
	if account.CooldownUntil != nil {
		t.Fatal("expected cooldown to be cleared in service")
	}

	stored, err := repo.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("failed to get account from repo: %v", err)
	}
	if stored.CooldownUntil != nil {
		t.Fatal("expected cooldown to be cleared in repo")
	}

	if len(publisher.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(publisher.events))
	}
	if publisher.events[0].Type != models.EventTypeCooldownEnded {
		t.Fatalf("expected cooldown ended event, got %s", publisher.events[0].Type)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestService_RotateAccountForAgent_EmitsEvent(t *testing.T) {
	cfg := config.DefaultConfig()
	ctx := context.Background()

	publisher := &testPublisher{}
	service := NewService(cfg, WithPublisher(publisher))

	now := time.Now().UTC()

	// Create current account (on cooldown)
	current := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "current",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
		CooldownUntil: timePtr(now.Add(1 * time.Hour)),
		UsageStats: &models.UsageStats{
			LastUsed: timePtr(now.Add(-30 * time.Minute)),
		},
	}

	// Create target account (available)
	target := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "target",
		CredentialRef: "env:OPENAI_API_KEY_2",
		IsActive:      true,
		UsageStats: &models.UsageStats{
			LastUsed: timePtr(now.Add(-2 * time.Hour)),
		},
	}

	for _, acct := range []*models.Account{current, target} {
		if err := service.AddAccount(ctx, acct); err != nil {
			t.Fatalf("AddAccount failed: %v", err)
		}
	}

	// Rotate with agent ID and reason
	agentID := "agent-123"
	reason := "cooldown"
	rotated, err := service.RotateAccountForAgent(ctx, current.ID, agentID, reason)
	if err != nil {
		t.Fatalf("RotateAccountForAgent failed: %v", err)
	}

	if rotated.ProfileName != "target" {
		t.Fatalf("expected rotated to target, got %s", rotated.ProfileName)
	}

	// Check event was emitted
	if len(publisher.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(publisher.events))
	}

	event := publisher.events[0]
	if event.Type != models.EventTypeAccountRotated {
		t.Fatalf("expected account.rotated event, got %s", event.Type)
	}
	if event.EntityType != models.EntityTypeAccount {
		t.Fatalf("expected account entity type, got %s", event.EntityType)
	}
	if event.EntityID != target.ID {
		t.Fatalf("expected entity ID to be new account ID %s, got %s", target.ID, event.EntityID)
	}

	var payload models.AccountRotatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.AgentID != agentID {
		t.Errorf("expected agent_id %q, got %q", agentID, payload.AgentID)
	}
	if payload.OldAccountID != current.ID {
		t.Errorf("expected old_account_id %q, got %q", current.ID, payload.OldAccountID)
	}
	if payload.NewAccountID != target.ID {
		t.Errorf("expected new_account_id %q, got %q", target.ID, payload.NewAccountID)
	}
	if payload.Reason != reason {
		t.Errorf("expected reason %q, got %q", reason, payload.Reason)
	}
}

func TestService_RotateAccountForAgent_DefaultReason(t *testing.T) {
	cfg := config.DefaultConfig()
	ctx := context.Background()

	publisher := &testPublisher{}
	service := NewService(cfg, WithPublisher(publisher))

	now := time.Now().UTC()

	current := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "current",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
	}
	target := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "target",
		CredentialRef: "env:OPENAI_API_KEY_2",
		IsActive:      true,
		UsageStats: &models.UsageStats{
			LastUsed: timePtr(now.Add(-1 * time.Hour)),
		},
	}

	for _, acct := range []*models.Account{current, target} {
		if err := service.AddAccount(ctx, acct); err != nil {
			t.Fatalf("AddAccount failed: %v", err)
		}
	}

	// Rotate with empty reason - should default to "cooldown"
	_, err := service.RotateAccountForAgent(ctx, current.ID, "agent-1", "")
	if err != nil {
		t.Fatalf("RotateAccountForAgent failed: %v", err)
	}

	if len(publisher.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(publisher.events))
	}

	var payload models.AccountRotatedPayload
	if err := json.Unmarshal(publisher.events[0].Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.Reason != "cooldown" {
		t.Errorf("expected default reason 'cooldown', got %q", payload.Reason)
	}
}

func TestService_RotateAccount_NoEventWithoutPublisher(t *testing.T) {
	cfg := config.DefaultConfig()
	ctx := context.Background()

	// Service without publisher
	service := NewService(cfg)

	current := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "current",
		CredentialRef: "env:OPENAI_API_KEY",
		IsActive:      true,
	}
	target := &models.Account{
		Provider:      models.ProviderOpenAI,
		ProfileName:   "target",
		CredentialRef: "env:OPENAI_API_KEY_2",
		IsActive:      true,
	}

	for _, acct := range []*models.Account{current, target} {
		if err := service.AddAccount(ctx, acct); err != nil {
			t.Fatalf("AddAccount failed: %v", err)
		}
	}

	// Rotate should succeed even without publisher (no panic)
	_, err := service.RotateAccount(ctx, current.ID)
	if err != nil {
		t.Fatalf("RotateAccount failed: %v", err)
	}
}

func TestService_WithVaultPath(t *testing.T) {
	cfg := config.DefaultConfig()
	customPath := "/custom/vault/path"

	service := NewService(cfg, WithVaultPath(customPath))

	if service.VaultPath() != customPath {
		t.Errorf("expected vault path %q, got %q", customPath, service.VaultPath())
	}
}

func TestService_SyncFromVault(t *testing.T) {
	// Create temp vault directory
	vaultPath := t.TempDir()
	profilesPath := filepath.Join(vaultPath, "profiles")

	// Create a mock profile for anthropic/testprofile
	profilePath := filepath.Join(profilesPath, "anthropic", "testprofile")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	// Write auth file
	authData := `{"api_key": "sk-ant-test123"}`
	if err := os.WriteFile(filepath.Join(profilePath, ".claude.json"), []byte(authData), 0600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	// Write metadata
	meta := map[string]interface{}{
		"adapter":      "claude",
		"name":         "testprofile",
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
		"auth_files":   []string{".claude.json"},
		"content_hash": "abc123",
	}
	metaJSON, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(profilePath, "meta.json"), metaJSON, 0600); err != nil {
		t.Fatalf("failed to write meta file: %v", err)
	}

	cfg := config.DefaultConfig()
	ctx := context.Background()

	service := NewService(cfg, WithVaultPath(vaultPath))

	synced, err := service.SyncFromVault(ctx)
	if err != nil {
		t.Fatalf("SyncFromVault failed: %v", err)
	}

	if synced != 1 {
		t.Errorf("expected 1 account synced, got %d", synced)
	}

	// Verify account was created
	accounts := service.List(ctx)
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}

	acct := accounts[0]
	if acct.Provider != models.ProviderAnthropic {
		t.Errorf("expected provider anthropic, got %s", acct.Provider)
	}
	if acct.ProfileName != "testprofile" {
		t.Errorf("expected profile name testprofile, got %s", acct.ProfileName)
	}
	if acct.CredentialRef != "vault:claude/testprofile" {
		t.Errorf("expected credential ref vault:claude/testprofile, got %s", acct.CredentialRef)
	}
	if !acct.IsActive {
		t.Error("expected account to be active")
	}
}

func TestService_SyncFromVault_UpdatesExisting(t *testing.T) {
	vaultPath := t.TempDir()
	profilesPath := filepath.Join(vaultPath, "profiles")

	// Create a mock profile
	profilePath := filepath.Join(profilesPath, "anthropic", "existing")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	authData := `{"api_key": "sk-ant-test123"}`
	if err := os.WriteFile(filepath.Join(profilePath, ".claude.json"), []byte(authData), 0600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	meta := map[string]interface{}{
		"adapter":      "claude",
		"name":         "existing",
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
		"auth_files":   []string{".claude.json"},
		"content_hash": "abc123",
	}
	metaJSON, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(profilePath, "meta.json"), metaJSON, 0600); err != nil {
		t.Fatalf("failed to write meta file: %v", err)
	}

	cfg := config.DefaultConfig()
	ctx := context.Background()

	service := NewService(cfg, WithVaultPath(vaultPath))

	// Add existing account with old credential ref
	existing := &models.Account{
		ID:            "claude/existing",
		Provider:      models.ProviderAnthropic,
		ProfileName:   "existing",
		CredentialRef: "old-ref",
		IsActive:      true,
	}
	if err := service.AddAccount(ctx, existing); err != nil {
		t.Fatalf("AddAccount failed: %v", err)
	}

	// Sync should update the existing account
	synced, err := service.SyncFromVault(ctx)
	if err != nil {
		t.Fatalf("SyncFromVault failed: %v", err)
	}

	// Should be 0 new accounts (just updated existing)
	if synced != 0 {
		t.Errorf("expected 0 new accounts synced, got %d", synced)
	}

	// Verify credential ref was updated
	acct, err := service.Get(ctx, "claude/existing")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if acct.CredentialRef != "vault:claude/existing" {
		t.Errorf("expected credential ref vault:claude/existing, got %s", acct.CredentialRef)
	}
}

func TestService_ActivateForAgent_NonVaultCredential(t *testing.T) {
	cfg := config.DefaultConfig()
	ctx := context.Background()

	t.Setenv("TEST_API_KEY", "test-key-123")

	service := NewService(cfg)

	account := &models.Account{
		ID:            "test-account",
		Provider:      models.ProviderOpenAI,
		ProfileName:   "test",
		CredentialRef: "env:TEST_API_KEY",
		IsActive:      true,
	}
	if err := service.AddAccount(ctx, account); err != nil {
		t.Fatalf("AddAccount failed: %v", err)
	}

	env, err := service.ActivateForAgent(ctx, account.ID, "agent-1")
	if err != nil {
		t.Fatalf("ActivateForAgent failed: %v", err)
	}

	if env["OPENAI_API_KEY"] != "test-key-123" {
		t.Errorf("expected OPENAI_API_KEY=test-key-123, got %s", env["OPENAI_API_KEY"])
	}
}

func TestService_ActivateForAgent_NotFound(t *testing.T) {
	cfg := config.DefaultConfig()
	ctx := context.Background()

	service := NewService(cfg)

	_, err := service.ActivateForAgent(ctx, "nonexistent", "agent-1")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("expected ErrAccountNotFound, got %v", err)
	}
}

func TestAdapterToProvider(t *testing.T) {
	tests := []struct {
		adapter  vault.Adapter
		expected models.Provider
	}{
		{vault.AdapterClaude, models.ProviderAnthropic},
		{vault.AdapterCodex, models.ProviderOpenAI},
		{vault.AdapterGemini, models.ProviderGoogle},
		{vault.AdapterOpenCode, models.ProviderCustom},
		{vault.Adapter("unknown"), ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.adapter), func(t *testing.T) {
			got := adapterToProvider(tt.adapter)
			if got != tt.expected {
				t.Errorf("adapterToProvider(%s) = %s, want %s", tt.adapter, got, tt.expected)
			}
		})
	}
}
