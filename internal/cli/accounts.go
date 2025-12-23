// Package cli provides account management CLI commands.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opencode-ai/swarm/internal/account"
	"github.com/opencode-ai/swarm/internal/account/caam"
	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/config"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	accountsListProvider  string
	accountsCooldownUntil string
	accountsRotateReason  string

	// accounts add flags
	accountsAddProvider      string
	accountsAddProfile       string
	accountsAddCredential    string
	accountsAddCredentialRef string
	accountsAddEnvVar        string
	accountsAddSkipTest      bool
	accountsAddForce         bool

	// accounts import-caam flags
	accountsImportCaamPath     string
	accountsImportCaamProvider string
	accountsImportCaamDryRun   bool
)

func init() {
	rootCmd.AddCommand(accountsCmd)
	accountsCmd.AddCommand(accountsListCmd)
	accountsCmd.AddCommand(accountsAddCmd)
	accountsCmd.AddCommand(accountsCooldownCmd)
	accountsCmd.AddCommand(accountsRotateCmd)
	accountsCmd.AddCommand(accountsImportCaamCmd)

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
	accountsAddCmd.Flags().StringVar(&accountsAddCredentialRef, "credential-ref", "", "credential reference (env:VAR, $VAR, file:/path)")
	accountsAddCmd.Flags().StringVar(&accountsAddCredential, "credential", "", "API key value (stored in a local file)")
	accountsAddCmd.Flags().StringVar(&accountsAddEnvVar, "env-var", "", "environment variable containing the credential")
	accountsAddCmd.Flags().BoolVar(&accountsAddSkipTest, "skip-test", false, "skip credential validation")
	accountsAddCmd.Flags().BoolVar(&accountsAddForce, "force", false, "overwrite stored credential file if it exists")

	// accounts import-caam flags
	accountsImportCaamCmd.Flags().StringVar(&accountsImportCaamPath, "path", "", "path to caam vault directory")
	accountsImportCaamCmd.Flags().StringVar(&accountsImportCaamProvider, "provider", "", "filter by provider (claude, codex, gemini)")
	accountsImportCaamCmd.Flags().BoolVar(&accountsImportCaamDryRun, "dry-run", false, "show what would be imported without making changes")
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
		reader := bufio.NewReader(os.Stdin)

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewAccountRepository(database)

		// Get provider
		provider, err := getAccountProvider(reader)
		if err != nil {
			return err
		}

		// Get profile name
		profile, err := getAccountProfile(reader, provider)
		if err != nil {
			return err
		}

		// Check if profile already exists
		existing, _ := findAccountByProfile(ctx, repo, provider, profile)
		if existing != nil {
			return fmt.Errorf("account profile %q already exists", profile)
		}

		// Get credential reference
		credentialRef, err := getAccountCredential(reader, provider, profile, accountsAddForce)
		if err != nil {
			return err
		}

		// Validate credential
		if !accountsAddSkipTest {
			if err := validateCredential(provider, credentialRef); err != nil {
				if !IsInteractive() {
					return fmt.Errorf("credential validation failed: %w", err)
				}
				confirm, promptErr := promptConfirm(reader, fmt.Sprintf("Credential validation failed (%v). Continue anyway? [y/N]: ", err))
				if promptErr != nil {
					return promptErr
				}
				if !confirm {
					return fmt.Errorf("credential validation failed: %w", err)
				}
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
		created, err := findAccountByProfile(ctx, repo, provider, profile)
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

var accountsImportCaamCmd = &cobra.Command{
	Use:        "import-caam",
	Short:      "Import accounts from caam vault (DEPRECATED)",
	Deprecated: "Use 'swarm vault' commands instead. The native vault provides better integration.",
	Long: `DEPRECATED: Import accounts from a coding_agent_account_manager (caam) vault.

This command is deprecated. Use the native Swarm vault instead:
  swarm vault backup <adapter> <profile>  # Save current auth
  swarm vault list                        # List saved profiles
  swarm vault activate <adapter> <profile> # Switch profiles

The caam vault stores account profiles for various AI coding assistants
(Claude, Codex, Gemini) in a standard directory structure.

By default, looks for the vault at ~/.local/share/caam/vault.
Use --path to specify a different location.

Credential references are created using the caam: prefix, which allows
Swarm to resolve credentials from the caam vault at runtime.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		reader := bufio.NewReader(os.Stdin)

		// Determine vault path
		vaultPath := accountsImportCaamPath
		if vaultPath == "" {
			vaultPath = caam.DefaultVaultPath()
		}

		// Parse the vault
		vault, err := caam.ParseVault(vaultPath)
		if err != nil {
			return fmt.Errorf("failed to parse caam vault: %w", err)
		}

		// Filter by provider if specified
		var profiles []*caam.Profile
		if accountsImportCaamProvider != "" {
			profiles = vault.GetProfilesByProvider(accountsImportCaamProvider)
		} else {
			profiles = vault.Profiles
		}

		// Filter to valid profiles only
		var validProfiles []*caam.Profile
		for _, p := range profiles {
			if p.IsValid() {
				validProfiles = append(validProfiles, p)
			}
		}

		if len(validProfiles) == 0 {
			fmt.Fprintln(os.Stdout, "No valid profiles found in caam vault.")
			return nil
		}

		// Dry run - just show what would be imported
		if accountsImportCaamDryRun {
			return showCaamImportPreview(validProfiles)
		}

		// Open database
		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewAccountRepository(database)

		// Import each profile
		var imported, skipped, failed int
		var results []CaamImportResult

		for _, profile := range validProfiles {
			account := profile.ToSwarmAccount()

			// Check if already exists
			existing, _ := findAccountByProfile(ctx, repo, account.Provider, account.ProfileName)
			if existing != nil {
				skipped++
				results = append(results, CaamImportResult{
					Provider:    string(account.Provider),
					ProfileName: account.ProfileName,
					Status:      "skipped",
					Reason:      "already exists",
				})
				continue
			}

			// Confirm import if interactive
			if IsInteractive() && !IsJSONOutput() && !IsJSONLOutput() {
				confirm, err := promptConfirm(reader, fmt.Sprintf("Import %s/%s? [y/N]: ", profile.Provider, profile.Email))
				if err != nil {
					return err
				}
				if !confirm {
					skipped++
					results = append(results, CaamImportResult{
						Provider:    string(account.Provider),
						ProfileName: account.ProfileName,
						Status:      "skipped",
						Reason:      "user declined",
					})
					continue
				}
			}

			// Create the account
			if err := repo.Create(ctx, account); err != nil {
				failed++
				results = append(results, CaamImportResult{
					Provider:    string(account.Provider),
					ProfileName: account.ProfileName,
					Status:      "failed",
					Reason:      err.Error(),
				})
				continue
			}

			imported++
			results = append(results, CaamImportResult{
				Provider:    string(account.Provider),
				ProfileName: account.ProfileName,
				Status:      "imported",
			})
		}

		// Output results
		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, CaamImportSummary{
				VaultPath: vaultPath,
				Imported:  imported,
				Skipped:   skipped,
				Failed:    failed,
				Results:   results,
			})
		}

		fmt.Fprintf(os.Stdout, "Import complete: %d imported, %d skipped, %d failed\n", imported, skipped, failed)
		return nil
	},
}

// CaamImportResult represents the result of importing a single profile.
type CaamImportResult struct {
	Provider    string `json:"provider"`
	ProfileName string `json:"profile_name"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
}

// CaamImportSummary represents the summary of a caam import operation.
type CaamImportSummary struct {
	VaultPath string             `json:"vault_path"`
	Imported  int                `json:"imported"`
	Skipped   int                `json:"skipped"`
	Failed    int                `json:"failed"`
	Results   []CaamImportResult `json:"results"`
}

// showCaamImportPreview displays what would be imported without making changes.
func showCaamImportPreview(profiles []*caam.Profile) error {
	if IsJSONOutput() || IsJSONLOutput() {
		var previews []map[string]string
		for _, p := range profiles {
			previews = append(previews, map[string]string{
				"provider":       p.Provider,
				"email":          p.Email,
				"swarm_provider": string(p.ToSwarmAccount().Provider),
				"credential_ref": p.ToSwarmAccount().CredentialRef,
			})
		}
		return WriteOutput(os.Stdout, previews)
	}

	fmt.Fprintln(os.Stdout, "Profiles that would be imported:")
	fmt.Fprintln(os.Stdout)

	writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(writer, "CAAM PROVIDER\tEMAIL\tSWARM PROVIDER\tCREDENTIAL REF")
	for _, p := range profiles {
		account := p.ToSwarmAccount()
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
			p.Provider,
			p.Email,
			account.Provider,
			account.CredentialRef,
		)
	}
	writer.Flush()

	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Total: %d profiles\n", len(profiles))
	fmt.Fprintln(os.Stdout, "Run without --dry-run to import.")
	return nil
}

// getAccountProvider prompts for or returns the provider.
func getAccountProvider(reader *bufio.Reader) (models.Provider, error) {
	if accountsAddProvider != "" {
		return parseProvider(accountsAddProvider)
	}

	if IsNonInteractive() {
		return "", fmt.Errorf("--provider is required in non-interactive mode")
	}

	fmt.Fprintln(os.Stderr, "Select provider:")
	fmt.Fprintln(os.Stderr, "  1) anthropic")
	fmt.Fprintln(os.Stderr, "  2) openai")
	fmt.Fprintln(os.Stderr, "  3) google")
	fmt.Fprintln(os.Stderr, "  4) custom")
	choice, err := promptLine(reader, "Provider [1-4]: ")
	if err != nil {
		return "", err
	}

	switch strings.ToLower(choice) {
	case "1", "anthropic":
		return models.ProviderAnthropic, nil
	case "2", "openai":
		return models.ProviderOpenAI, nil
	case "3", "google":
		return models.ProviderGoogle, nil
	case "4", "custom":
		return models.ProviderCustom, nil
	default:
		return "", fmt.Errorf("invalid provider selection %q", choice)
	}
}

// getAccountProfile prompts for or returns the profile name.
func getAccountProfile(reader *bufio.Reader, provider models.Provider) (string, error) {
	if accountsAddProfile != "" {
		return strings.TrimSpace(accountsAddProfile), nil
	}

	if IsNonInteractive() {
		return "", fmt.Errorf("--profile is required in non-interactive mode")
	}

	defaultProfile := "default"
	choice, err := promptLine(reader, fmt.Sprintf("Profile name for %s [%s]: ", provider, defaultProfile))
	if err != nil {
		return "", err
	}
	if choice == "" {
		return defaultProfile, nil
	}
	return choice, nil
}

// getAccountCredential prompts for or returns the credential reference.
func getAccountCredential(reader *bufio.Reader, provider models.Provider, profile string, force bool) (string, error) {
	if accountsAddCredentialRef != "" {
		return strings.TrimSpace(accountsAddCredentialRef), nil
	}

	if accountsAddEnvVar != "" {
		envVar := strings.TrimSpace(accountsAddEnvVar)
		if !strings.HasPrefix(envVar, "env:") {
			envVar = "env:" + envVar
		}
		return envVar, nil
	}

	if accountsAddCredential != "" {
		path, err := storeCredentialSecret(provider, profile, accountsAddCredential, force)
		if err != nil {
			return "", err
		}
		return "file:" + path, nil
	}

	if IsNonInteractive() {
		return "", fmt.Errorf("--credential-ref, --credential, or --env-var is required in non-interactive mode")
	}

	fmt.Fprintln(os.Stderr, "Credential source:")
	fmt.Fprintln(os.Stderr, "  1) Environment variable (recommended)")
	fmt.Fprintln(os.Stderr, "  2) Existing file")
	fmt.Fprintln(os.Stderr, "  3) Enter secret now (stored in local file)")
	choice, err := promptLine(reader, "Choice [1-3]: ")
	if err != nil {
		return "", err
	}

	switch strings.ToLower(choice) {
	case "", "1", "env", "environment":
		envDefault := defaultEnvVarForProvider(provider)
		envChoice, err := promptLine(reader, fmt.Sprintf("Environment variable name [%s]: ", envDefault))
		if err != nil {
			return "", err
		}
		if envChoice == "" {
			envChoice = envDefault
		}
		if envChoice == "" {
			return "", fmt.Errorf("environment variable name is required")
		}
		return "env:" + envChoice, nil
	case "2", "file":
		path, err := promptLine(reader, "Credential file path: ")
		if err != nil {
			return "", err
		}
		if path == "" {
			return "", fmt.Errorf("credential file path is required")
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("failed to read credential file: %w", err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("credential file path is a directory")
		}
		return "file:" + path, nil
	case "3", "secret", "enter":
		secret, err := promptSecret("API key: ")
		if err != nil {
			return "", err
		}
		confirm, err := promptSecret("Confirm API key: ")
		if err != nil {
			return "", err
		}
		if secret == "" {
			return "", fmt.Errorf("API key is required")
		}
		if secret != confirm {
			return "", fmt.Errorf("API key confirmation does not match")
		}
		path, err := storeCredentialSecret(provider, profile, secret, force)
		if err != nil {
			return "", err
		}
		return "file:" + path, nil
	default:
		return "", fmt.Errorf("invalid credential selection %q", choice)
	}
}

// defaultEnvVarForProvider returns the default environment variable name for a provider.
func defaultEnvVarForProvider(provider models.Provider) string {
	env := account.ProviderEnvVar(provider)
	if env != "" {
		return env
	}
	return "API_KEY"
}

// validateCredential tests that the credential resolves for the provider.
func validateCredential(provider models.Provider, credentialRef string) error {
	apiKey, err := account.ResolveCredential(credentialRef)
	if err != nil {
		return err
	}
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("credential is empty")
	}

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
		if len(apiKey) < 10 {
			return fmt.Errorf("API key appears too short")
		}
	case models.ProviderCustom:
	}

	return nil
}

func promptLine(reader *bufio.Reader, prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptSecret(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes)), nil
}

func storeCredentialSecret(provider models.Provider, profile, secret string, force bool) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", fmt.Errorf("credential is empty")
	}
	cfg := GetConfig()
	if cfg == nil {
		return "", fmt.Errorf("configuration not loaded")
	}
	dir := filepath.Join(cfg.Global.DataDir, "credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create credentials directory: %w", err)
	}

	filename := fmt.Sprintf("%s_%s.key", sanitizeCredentialPart(string(provider)), sanitizeCredentialPart(profile))
	path := filepath.Join(dir, filename)
	if _, err := os.Stat(path); err == nil && !force {
		return "", fmt.Errorf("credential file already exists (use --force to overwrite)")
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to check credential file: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to write credential file: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(secret); err != nil {
		return "", fmt.Errorf("failed to write credential file: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		return "", fmt.Errorf("failed to set credential file permissions: %w", err)
	}
	return path, nil
}

func sanitizeCredentialPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func promptConfirm(reader *bufio.Reader, prompt string) (bool, error) {
	choice, err := promptLine(reader, prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(choice) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
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

// findAccountByProfile searches for an account by provider + profile name.
func findAccountByProfile(ctx context.Context, repo *db.AccountRepository, provider models.Provider, profile string) (*models.Account, error) {
	accounts, err := repo.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, acct := range accounts {
		if acct != nil && acct.Provider == provider && acct.ProfileName == profile {
			return acct, nil
		}
	}
	return nil, fmt.Errorf("account %q for provider %s not found", profile, provider)
}
