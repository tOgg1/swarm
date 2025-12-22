// Package cli provides account management CLI commands.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opencode-ai/swarm/internal/account"
	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	accountsListProvider  string
	accountsCooldownUntil string
	accountsRotateReason  string

	// accounts add flags
	accountsAddProvider   string
	accountsAddProfile    string
	accountsAddCredential string
	accountsAddEnvVar     string
	accountsAddSkipTest   bool
)

func init() {
	rootCmd.AddCommand(accountsCmd)
	accountsCmd.AddCommand(accountsListCmd)
	accountsCmd.AddCommand(accountsAddCmd)
	accountsCmd.AddCommand(accountsCooldownCmd)
	accountsCmd.AddCommand(accountsRotateCmd)

	accountsCooldownCmd.AddCommand(accountsCooldownListCmd)
	accountsCooldownCmd.AddCommand(accountsCooldownSetCmd)
	accountsCooldownCmd.AddCommand(accountsCooldownClearCmd)

	accountsListCmd.Flags().StringVar(&accountsListProvider, "provider", "", "filter by provider (anthropic, openai, google, custom)")
	accountsCooldownSetCmd.Flags().StringVar(&accountsCooldownUntil, "until", "", "cooldown end time (RFC3339 or duration like 30m)")
	_ = accountsCooldownSetCmd.MarkFlagRequired("until")
	accountsRotateCmd.Flags().StringVar(&accountsRotateReason, "reason", "manual", "reason for account rotation")

	// accounts add flags
	accountsAddCmd.Flags().StringVar(&accountsAddProvider, "provider", "", "provider type (anthropic, openai, google, custom)")
	accountsAddCmd.Flags().StringVar(&accountsAddProfile, "profile", "", "profile name for the account")
	accountsAddCmd.Flags().StringVar(&accountsAddCredential, "credential", "", "API key or credential value")
	accountsAddCmd.Flags().StringVar(&accountsAddEnvVar, "env-var", "", "environment variable containing the credential")
	accountsAddCmd.Flags().BoolVar(&accountsAddSkipTest, "skip-test", false, "skip credential validation")
}

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "Manage accounts",
	Long:  "Manage provider accounts and profiles used by agents.",
}

var accountsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new account",
	Long:  "Add a new provider account with credentials. Prompts interactively or accepts flags.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewAccountRepository(database)

		// Get provider
		provider, err := getAccountProvider()
		if err != nil {
			return err
		}

		// Get profile name
		profile, err := getAccountProfile(provider)
		if err != nil {
			return err
		}

		// Check if profile already exists
		existing, _ := findAccountByProfile(ctx, repo, profile)
		if existing != nil {
			return fmt.Errorf("account profile %q already exists", profile)
		}

		// Get credential reference
		credentialRef, err := getAccountCredential(provider)
		if err != nil {
			return err
		}

		// Validate credential
		if !accountsAddSkipTest {
			if err := validateCredential(ctx, provider, credentialRef); err != nil {
				return fmt.Errorf("credential validation failed: %w", err)
			}
			fmt.Fprintln(os.Stderr, "Credential validated successfully.")
		}

		// Create account
		account := &models.Account{
			Provider:      provider,
			ProfileName:   profile,
			CredentialRef: credentialRef,
			IsActive:      true,
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		}

		if err := repo.Create(ctx, account); err != nil {
			return fmt.Errorf("failed to create account: %w", err)
		}

		// Reload to get ID
		created, err := findAccountByProfile(ctx, repo, profile)
		if err != nil {
			return fmt.Errorf("failed to load created account: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, created)
		}

		fmt.Fprintf(os.Stdout, "Account %q created successfully (ID: %s)\n", profile, created.ID)
		return nil
	},
}

// getAccountProvider prompts for or returns the provider.
func getAccountProvider() (models.Provider, error) {
	if accountsAddProvider != "" {
		return parseProvider(accountsAddProvider)
	}

	if IsNonInteractive() {
		return "", fmt.Errorf("--provider is required in non-interactive mode")
	}

	providers := []string{"anthropic", "openai", "google", "custom"}
	fmt.Fprintln(os.Stderr, "Select provider:")
	for i, p := range providers {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, p)
	}
	fmt.Fprintf(os.Stderr, "Provider [1-%d]: ", len(providers))

	var choice int
	if _, err := fmt.Scanf("%d", &choice); err != nil || choice < 1 || choice > len(providers) {
		return "", fmt.Errorf("invalid selection")
	}

	return parseProvider(providers[choice-1])
}

// getAccountProfile prompts for or returns the profile name.
func getAccountProfile(provider models.Provider) (string, error) {
	if accountsAddProfile != "" {
		return accountsAddProfile, nil
	}

	if IsNonInteractive() {
		return "", fmt.Errorf("--profile is required in non-interactive mode")
	}

	defaultProfile := fmt.Sprintf("%s-default", provider)
	fmt.Fprintf(os.Stderr, "Profile name [%s]: ", defaultProfile)

	var profile string
	if _, err := fmt.Scanf("%s", &profile); err != nil || strings.TrimSpace(profile) == "" {
		return defaultProfile, nil
	}

	return strings.TrimSpace(profile), nil
}

// getAccountCredential prompts for or returns the credential reference.
func getAccountCredential(provider models.Provider) (string, error) {
	// If env var specified, use that
	if accountsAddEnvVar != "" {
		envVar := strings.TrimSpace(accountsAddEnvVar)
		if !strings.HasPrefix(envVar, "env:") {
			envVar = "env:" + envVar
		}
		// Verify the env var exists
		varName := strings.TrimPrefix(envVar, "env:")
		if _, ok := os.LookupEnv(varName); !ok {
			return "", fmt.Errorf("environment variable %q not set", varName)
		}
		return envVar, nil
	}

	// If credential specified directly
	if accountsAddCredential != "" {
		// Store as literal value (not recommended for production)
		return accountsAddCredential, nil
	}

	if IsNonInteractive() {
		return "", fmt.Errorf("--credential or --env-var is required in non-interactive mode")
	}

	// Prompt for credential type
	fmt.Fprintln(os.Stderr, "How do you want to provide the API key?")
	fmt.Fprintln(os.Stderr, "  1) Environment variable (recommended)")
	fmt.Fprintln(os.Stderr, "  2) Enter directly")
	fmt.Fprintf(os.Stderr, "Choice [1-2]: ")

	var choice int
	if _, err := fmt.Scanf("%d", &choice); err != nil {
		choice = 1 // default to env var
	}

	switch choice {
	case 1:
		defaultEnv := defaultEnvVarForProvider(provider)
		fmt.Fprintf(os.Stderr, "Environment variable name [%s]: ", defaultEnv)

		var envVar string
		if _, err := fmt.Scanf("%s", &envVar); err != nil || strings.TrimSpace(envVar) == "" {
			envVar = defaultEnv
		}
		envVar = strings.TrimSpace(envVar)

		// Verify the env var exists
		if _, ok := os.LookupEnv(envVar); !ok {
			return "", fmt.Errorf("environment variable %q not set", envVar)
		}

		return "env:" + envVar, nil

	case 2:
		fmt.Fprint(os.Stderr, "API key: ")
		var key string
		if _, err := fmt.Scanf("%s", &key); err != nil {
			return "", fmt.Errorf("failed to read API key: %w", err)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return "", fmt.Errorf("API key is required")
		}
		return key, nil

	default:
		return "", fmt.Errorf("invalid selection")
	}
}

// defaultEnvVarForProvider returns the default environment variable name for a provider.
func defaultEnvVarForProvider(provider models.Provider) string {
	switch provider {
	case models.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case models.ProviderOpenAI:
		return "OPENAI_API_KEY"
	case models.ProviderGoogle:
		return "GOOGLE_API_KEY"
	default:
		return "API_KEY"
	}
}

// validateCredential tests that the credential works with the provider.
func validateCredential(ctx context.Context, provider models.Provider, credentialRef string) error {
	// Resolve the actual credential value
	apiKey := credentialRef
	if strings.HasPrefix(credentialRef, "env:") {
		varName := strings.TrimPrefix(credentialRef, "env:")
		var ok bool
		apiKey, ok = os.LookupEnv(varName)
		if !ok {
			return fmt.Errorf("environment variable %q not set", varName)
		}
	}

	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("credential is empty")
	}

	// Basic validation: check key format
	switch provider {
	case models.ProviderAnthropic:
		if !strings.HasPrefix(apiKey, "sk-ant-") {
			return fmt.Errorf("invalid Anthropic API key format (expected sk-ant-...)")
		}
	case models.ProviderOpenAI:
		if !strings.HasPrefix(apiKey, "sk-") {
			return fmt.Errorf("invalid OpenAI API key format (expected sk-...)")
		}
	case models.ProviderGoogle:
		// Google keys have various formats, just check non-empty
		if len(apiKey) < 10 {
			return fmt.Errorf("API key appears too short")
		}
	case models.ProviderCustom:
		// No format validation for custom providers
	}

	// Note: Full API validation would require making test API calls
	// which we skip to avoid unnecessary API usage
	return nil
}

var accountsCooldownCmd = &cobra.Command{
	Use:   "cooldown",
	Short: "Manage account cooldowns",
	Long:  "List, set, or clear account cooldown windows.",
}

var accountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List accounts",
	Long:  "List available provider accounts and their status.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewAccountRepository(database)

		var provider *models.Provider
		if strings.TrimSpace(accountsListProvider) != "" {
			parsed, err := parseProvider(accountsListProvider)
			if err != nil {
				return err
			}
			provider = &parsed
		}

		accounts, err := repo.List(ctx, provider)
		if err != nil {
			return fmt.Errorf("failed to list accounts: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, accounts)
		}

		if len(accounts) == 0 {
			fmt.Fprintln(os.Stdout, "No accounts found.")
			return nil
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "PROVIDER\tPROFILE\tSTATUS\tCOOLDOWN")
		for _, account := range accounts {
			fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\n",
				account.Provider,
				account.ProfileName,
				formatAccountStatus(account),
				formatAccountCooldown(account),
			)
		}
		return writer.Flush()
	},
}

var accountsRotateCmd = &cobra.Command{
	Use:   "rotate <agent-id>",
	Short: "Rotate an agent to a new account",
	Long:  "Select the next available account for the agent's provider and restart the agent.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		agentRepo := db.NewAgentRepository(database)
		accountRepo := db.NewAccountRepository(database)
		eventRepo := db.NewEventRepository(database)
		nodeRepo := db.NewNodeRepository(database)
		wsRepo := db.NewWorkspaceRepository(database)
		queueRepo := db.NewQueueRepository(database)

		nodeService := node.NewService(nodeRepo)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		agentInfo, err := findAgent(ctx, agentRepo, args[0])
		if err != nil {
			return err
		}
		if strings.TrimSpace(agentInfo.AccountID) == "" {
			return fmt.Errorf("agent %s has no account assigned", agentInfo.ID)
		}

		currentAccount, err := findAccount(ctx, accountRepo, agentInfo.AccountID)
		if err != nil {
			return err
		}

		rotationMode := accountIDModeByAgent(agentInfo.AccountID, currentAccount)
		nextAccount, err := selectNextAccount(ctx, accountRepo, currentAccount, rotationMode)
		if err != nil {
			return err
		}

		accountService, err := buildAccountService(ctx, accountRepo, rotationMode)
		if err != nil {
			return err
		}

		tmuxClient := tmux.NewLocalClient()
		agentService := agent.NewService(agentRepo, queueRepo, wsService, accountService, tmuxClient, agentServiceOptions(database)...)

		newAccountID := accountIDForMode(nextAccount, rotationMode)
		updatedAgent, err := agentService.RestartAgentWithAccount(ctx, agentInfo.ID, newAccountID)
		if err != nil {
			return err
		}

		if err := recordAccountRotation(ctx, eventRepo, agentInfo.ID, agentInfo.AccountID, newAccountID, accountsRotateReason); err != nil {
			return err
		}

		result := AccountRotationResult{
			AgentID:      updatedAgent.ID,
			OldAccountID: agentInfo.AccountID,
			NewAccountID: newAccountID,
			Provider:     currentAccount.Provider,
			Reason:       accountsRotateReason,
			Timestamp:    time.Now().UTC(),
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, result)
		}

		fmt.Fprintf(os.Stdout, "Rotated agent %s from %s to %s\n", updatedAgent.ID, agentInfo.AccountID, newAccountID)
		return nil
	},
}

var accountsCooldownListCmd = &cobra.Command{
	Use:   "list",
	Short: "List account cooldowns",
	Long:  "List accounts with active or expired cooldown timestamps.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewAccountRepository(database)
		accounts, err := repo.List(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to list accounts: %w", err)
		}

		cooldownAccounts := filterAccountsWithCooldown(accounts)
		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, cooldownAccounts)
		}

		if len(cooldownAccounts) == 0 {
			fmt.Fprintln(os.Stdout, "No accounts on cooldown.")
			return nil
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "PROVIDER\tPROFILE\tSTATUS\tCOOLDOWN\tUNTIL")
		for _, account := range cooldownAccounts {
			until := "-"
			if account.CooldownUntil != nil {
				until = account.CooldownUntil.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\t%s\n",
				account.Provider,
				account.ProfileName,
				formatAccountStatus(account),
				formatAccountCooldown(account),
				until,
			)
		}
		return writer.Flush()
	},
}

var accountsCooldownSetCmd = &cobra.Command{
	Use:   "set <account>",
	Short: "Set an account cooldown",
	Long:  "Set a cooldown for an account until a specific time or for a duration.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		until, err := parseCooldownUntil(accountsCooldownUntil)
		if err != nil {
			return err
		}

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewAccountRepository(database)
		account, err := findAccount(ctx, repo, args[0])
		if err != nil {
			return err
		}

		if err := repo.SetCooldown(ctx, account.ID, until); err != nil {
			return fmt.Errorf("failed to set cooldown: %w", err)
		}

		updated, err := repo.Get(ctx, account.ID)
		if err != nil {
			return fmt.Errorf("failed to load updated account: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, updated)
		}

		fmt.Fprintf(os.Stdout, "Cooldown set for %s until %s\n", updated.ProfileName, updated.CooldownUntil.UTC().Format(time.RFC3339))
		return nil
	},
}

var accountsCooldownClearCmd = &cobra.Command{
	Use:   "clear <account>",
	Short: "Clear an account cooldown",
	Long:  "Remove the cooldown timestamp from an account.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewAccountRepository(database)
		account, err := findAccount(ctx, repo, args[0])
		if err != nil {
			return err
		}

		if err := repo.ClearCooldown(ctx, account.ID); err != nil {
			return fmt.Errorf("failed to clear cooldown: %w", err)
		}

		updated, err := repo.Get(ctx, account.ID)
		if err != nil {
			return fmt.Errorf("failed to load updated account: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, updated)
		}

		fmt.Fprintf(os.Stdout, "Cooldown cleared for %s\n", updated.ProfileName)
		return nil
	},
}

func parseProvider(value string) (models.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(models.ProviderAnthropic):
		return models.ProviderAnthropic, nil
	case string(models.ProviderOpenAI):
		return models.ProviderOpenAI, nil
	case string(models.ProviderGoogle):
		return models.ProviderGoogle, nil
	case string(models.ProviderCustom):
		return models.ProviderCustom, nil
	default:
		return "", fmt.Errorf("invalid provider: %s", value)
	}
}

type AccountRotationResult struct {
	AgentID      string          `json:"agent_id"`
	OldAccountID string          `json:"old_account_id"`
	NewAccountID string          `json:"new_account_id"`
	Provider     models.Provider `json:"provider"`
	Reason       string          `json:"reason"`
	Timestamp    time.Time       `json:"timestamp"`
}

type accountIDMode int

const (
	accountIDModeProfile accountIDMode = iota
	accountIDModeDatabase
)

func accountIDModeByAgent(agentAccountID string, currentAccount *models.Account) accountIDMode {
	if currentAccount == nil {
		return accountIDModeProfile
	}
	if agentAccountID == currentAccount.ID {
		return accountIDModeDatabase
	}
	return accountIDModeProfile
}

func accountIDForMode(account *models.Account, mode accountIDMode) string {
	if account == nil {
		return ""
	}
	if mode == accountIDModeDatabase {
		return account.ID
	}
	return account.ProfileName
}

func buildAccountService(ctx context.Context, repo *db.AccountRepository, mode accountIDMode) (*account.Service, error) {
	cfg := GetConfig()
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	cfgCopy := *cfg
	cfgCopy.Accounts = nil

	svc := account.NewService(&cfgCopy, account.WithRepository(repo))

	accounts, err := repo.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to load accounts: %w", err)
	}

	for _, acct := range accounts {
		if acct == nil {
			continue
		}
		clone := *acct
		if mode == accountIDModeProfile {
			clone.ID = clone.ProfileName
		}
		if err := svc.AddAccount(ctx, &clone); err != nil && !errors.Is(err, account.ErrAccountAlreadyExists) {
			return nil, fmt.Errorf("failed to register account %s: %w", clone.ProfileName, err)
		}
	}

	return svc, nil
}

func selectNextAccount(ctx context.Context, repo *db.AccountRepository, current *models.Account, mode accountIDMode) (*models.Account, error) {
	if current == nil {
		return nil, fmt.Errorf("current account is required")
	}

	provider := current.Provider
	accounts, err := repo.List(ctx, &provider)
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}

	var candidates []*models.Account
	for _, acct := range accounts {
		if acct == nil || !acct.IsAvailable() {
			continue
		}
		if mode == accountIDModeDatabase {
			if acct.ID == current.ID {
				continue
			}
		} else if acct.ProfileName == current.ProfileName {
			continue
		}
		candidates = append(candidates, acct)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available accounts to rotate for provider %s", provider)
	}

	return selectLeastRecentlyUsedAccount(candidates), nil
}

func selectLeastRecentlyUsedAccount(accounts []*models.Account) *models.Account {
	if len(accounts) == 0 {
		return nil
	}

	sort.Slice(accounts, func(i, j int) bool {
		left := accountLastUsed(accounts[i])
		right := accountLastUsed(accounts[j])
		if left.Equal(right) {
			return accounts[i].ProfileName < accounts[j].ProfileName
		}
		return left.Before(right)
	})

	return accounts[0]
}

func accountLastUsed(account *models.Account) time.Time {
	if account == nil || account.UsageStats == nil || account.UsageStats.LastUsed == nil {
		return time.Time{}
	}
	return account.UsageStats.LastUsed.UTC()
}

func recordAccountRotation(ctx context.Context, repo *db.EventRepository, agentID, oldAccountID, newAccountID, reason string) error {
	if repo == nil {
		return nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}

	payload, err := json.Marshal(models.AccountRotatedPayload{
		AgentID:      agentID,
		OldAccountID: oldAccountID,
		NewAccountID: newAccountID,
		Reason:       reason,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal rotation payload: %w", err)
	}

	event := &models.Event{
		Type:       models.EventTypeAccountRotated,
		EntityType: models.EntityTypeAccount,
		EntityID:   newAccountID,
		Payload:    payload,
	}

	if err := repo.Create(ctx, event); err != nil {
		return fmt.Errorf("failed to record rotation event: %w", err)
	}

	return nil
}

func formatAccountStatus(account *models.Account) string {
	if !account.IsActive {
		return "inactive"
	}
	if account.IsOnCooldown() {
		return "cooldown"
	}
	return "active"
}

func formatAccountCooldown(account *models.Account) string {
	if account.CooldownUntil == nil {
		return "-"
	}
	if !account.IsOnCooldown() {
		return "expired"
	}
	remaining := account.CooldownRemaining()
	if remaining < time.Second {
		return "<1s"
	}
	return remaining.Round(time.Second).String()
}

func parseCooldownUntil(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("cooldown time is required")
	}

	if dur, err := parseDurationWithDays(value); err == nil {
		return time.Now().UTC().Add(dur), nil
	}

	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05", value); err == nil {
		return t.UTC(), nil
	}

	return time.Time{}, fmt.Errorf("invalid time format: %q (use duration like '30m' or timestamp like '2024-01-15T10:30:00Z')", value)
}

func filterAccountsWithCooldown(accounts []*models.Account) []*models.Account {
	filtered := make([]*models.Account, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if account.CooldownUntil != nil {
			filtered = append(filtered, account)
		}
	}
	return filtered
}

// findAccountByProfile searches for an account by profile name.
func findAccountByProfile(ctx context.Context, repo *db.AccountRepository, profile string) (*models.Account, error) {
	accounts, err := repo.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, acct := range accounts {
		if acct != nil && acct.ProfileName == profile {
			return acct, nil
		}
	}
	return nil, fmt.Errorf("account with profile %q not found", profile)
}
