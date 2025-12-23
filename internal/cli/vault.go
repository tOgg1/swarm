// Package cli provides vault management CLI commands.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opencode-ai/swarm/internal/vault"
	"github.com/spf13/cobra"
)

var (
	// vault backup flags
	vaultBackupForce bool

	// vault activate flags
	vaultActivateBackupCurrent bool

	// vault delete flags
	vaultDeleteForce bool

	// vault clear flags
	vaultClearForce bool

	// vault global flags
	vaultPath string
)

func init() {
	rootCmd.AddCommand(vaultCmd)
	vaultCmd.AddCommand(vaultInitCmd)
	vaultCmd.AddCommand(vaultBackupCmd)
	vaultCmd.AddCommand(vaultActivateCmd)
	vaultCmd.AddCommand(vaultListCmd)
	vaultCmd.AddCommand(vaultDeleteCmd)
	vaultCmd.AddCommand(vaultStatusCmd)
	vaultCmd.AddCommand(vaultPathsCmd)
	vaultCmd.AddCommand(vaultClearCmd)

	// Global vault flags
	vaultCmd.PersistentFlags().StringVar(&vaultPath, "vault-path", "", "path to vault directory (default: ~/.config/swarm/vault)")

	// Backup flags
	vaultBackupCmd.Flags().BoolVar(&vaultBackupForce, "force", false, "overwrite existing profile")

	// Activate flags
	vaultActivateCmd.Flags().BoolVar(&vaultActivateBackupCurrent, "backup-current", false, "backup current auth files before activating")

	// Delete flags
	vaultDeleteCmd.Flags().BoolVar(&vaultDeleteForce, "force", false, "skip confirmation prompt")

	// Clear flags
	vaultClearCmd.Flags().BoolVar(&vaultClearForce, "force", false, "skip confirmation prompt")
}

var vaultCmd = &cobra.Command{
	Use:   "vault",
	Short: "Manage credential vault",
	Long: `Manage the credential vault for AI coding agent authentication.

The vault stores auth files for different providers (Claude, Codex, Gemini, OpenCode)
and allows instant switching between profiles (<100ms).

Vault location: ~/.config/swarm/vault/profiles/{provider}/{profile}/

Examples:
  swarm vault backup claude work        # Save current Claude auth as "work"
  swarm vault activate claude personal  # Switch to "personal" Claude profile
  swarm vault list                      # List all profiles
  swarm vault status                    # Show active profile for each adapter`,
}

var vaultInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize vault directory",
	Long:  "Create the vault directory structure if it doesn't exist.",
	RunE: func(cmd *cobra.Command, args []string) error {
		vp := getVaultPath()

		// Create vault directory structure
		profilesPath := vault.ProfilesPath(vp)
		if err := os.MkdirAll(profilesPath, 0700); err != nil {
			return fmt.Errorf("failed to create vault directory: %w", err)
		}

		// Create provider directories
		for _, adapter := range vault.AllAdapters() {
			providerPath := vault.ProfilePath(vp, adapter, "")
			if err := os.MkdirAll(providerPath, 0700); err != nil {
				return fmt.Errorf("failed to create provider directory for %s: %w", adapter, err)
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]string{
				"status": "initialized",
				"path":   vp,
			})
		}

		fmt.Fprintf(os.Stdout, "Vault initialized at %s\n", vp)
		return nil
	},
}

var vaultBackupCmd = &cobra.Command{
	Use:   "backup <adapter> <profile>",
	Short: "Backup current auth files to vault",
	Long: `Save current authentication files to a named profile in the vault.

Adapters: claude, codex, gemini, opencode

Examples:
  swarm vault backup claude work
  swarm vault backup codex personal
  swarm vault backup gemini default`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		vp := getVaultPath()
		adapterName := strings.ToLower(args[0])
		profileName := args[1]

		adapter := vault.ParseAdapter(adapterName)
		if adapter == "" {
			return fmt.Errorf("unknown adapter: %s (valid: claude, codex, gemini, opencode)", adapterName)
		}

		// Check if profile exists
		existing, _ := vault.Get(vp, adapter, profileName)
		if existing != nil && !vaultBackupForce {
			return fmt.Errorf("profile %q already exists (use --force to overwrite)", profileName)
		}

		profile, err := vault.Backup(vp, adapter, profileName)
		if err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, profile)
		}

		fmt.Fprintf(os.Stdout, "Backed up %s auth files to profile %q\n", adapter, profileName)
		fmt.Fprintf(os.Stdout, "  Files: %s\n", strings.Join(profile.AuthFiles, ", "))
		fmt.Fprintf(os.Stdout, "  Path: %s\n", profile.Path)
		return nil
	},
}

var vaultActivateCmd = &cobra.Command{
	Use:     "activate <adapter> <profile>",
	Aliases: []string{"switch", "use"},
	Short:   "Activate a vault profile",
	Long: `Restore authentication files from a vault profile.

This instantly switches to the specified profile by copying auth files
to their standard locations.

Adapters: claude, codex, gemini, opencode

Examples:
  swarm vault activate claude work
  swarm vault switch codex personal
  swarm vault use gemini default`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		vp := getVaultPath()
		adapterName := strings.ToLower(args[0])
		profileName := args[1]

		adapter := vault.ParseAdapter(adapterName)
		if adapter == "" {
			return fmt.Errorf("unknown adapter: %s (valid: claude, codex, gemini, opencode)", adapterName)
		}

		// Optionally backup current auth files first
		if vaultActivateBackupCurrent {
			authPaths := vault.GetAuthPaths(adapter)
			if authPaths.HasAuth() {
				backupName := fmt.Sprintf("backup-%s", time.Now().Format("20060102-150405"))
				if _, err := vault.Backup(vp, adapter, backupName); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to backup current auth: %v\n", err)
				} else {
					fmt.Fprintf(os.Stderr, "Backed up current auth to %q\n", backupName)
				}
			}
		}

		if err := vault.Activate(vp, adapter, profileName); err != nil {
			return fmt.Errorf("activation failed: %w", err)
		}

		profile, _ := vault.Get(vp, adapter, profileName)

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]interface{}{
				"status":  "activated",
				"adapter": adapter,
				"profile": profileName,
				"files":   profile.AuthFiles,
			})
		}

		fmt.Fprintf(os.Stdout, "Activated %s profile %q\n", adapter, profileName)
		return nil
	},
}

var vaultListCmd = &cobra.Command{
	Use:   "list [adapter]",
	Short: "List vault profiles",
	Long: `List all saved profiles in the vault.

Optionally filter by adapter (claude, codex, gemini, opencode).

Examples:
  swarm vault list           # List all profiles
  swarm vault list claude    # List only Claude profiles`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		vp := getVaultPath()

		var adapter vault.Adapter
		if len(args) > 0 {
			adapter = vault.ParseAdapter(strings.ToLower(args[0]))
			if adapter == "" {
				return fmt.Errorf("unknown adapter: %s (valid: claude, codex, gemini, opencode)", args[0])
			}
		}

		profiles, err := vault.List(vp, adapter)
		if err != nil {
			return fmt.Errorf("failed to list profiles: %w", err)
		}

		// Get active profiles for display
		activeProfiles := make(map[string]string) // adapter -> profile name
		for _, a := range vault.AllAdapters() {
			if active, _ := vault.GetActive(vp, a); active != nil {
				activeProfiles[string(a)] = active.Name
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			type profileOutput struct {
				Adapter     string   `json:"adapter"`
				Name        string   `json:"name"`
				AuthFiles   []string `json:"auth_files"`
				ContentHash string   `json:"content_hash"`
				CreatedAt   string   `json:"created_at"`
				UpdatedAt   string   `json:"updated_at"`
				IsActive    bool     `json:"is_active"`
			}
			output := make([]profileOutput, 0, len(profiles))
			for _, p := range profiles {
				isActive := activeProfiles[string(p.Adapter)] == p.Name
				output = append(output, profileOutput{
					Adapter:     string(p.Adapter),
					Name:        p.Name,
					AuthFiles:   p.AuthFiles,
					ContentHash: p.ContentHash,
					CreatedAt:   p.CreatedAt.Format(time.RFC3339),
					UpdatedAt:   p.UpdatedAt.Format(time.RFC3339),
					IsActive:    isActive,
				})
			}
			return WriteOutput(os.Stdout, output)
		}

		if len(profiles) == 0 {
			fmt.Fprintln(os.Stdout, "No profiles found.")
			fmt.Fprintln(os.Stdout, "Use 'swarm vault backup <adapter> <profile>' to save current auth.")
			return nil
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "ADAPTER\tPROFILE\tFILES\tUPDATED\tSTATUS")
		for _, p := range profiles {
			status := ""
			if activeProfiles[string(p.Adapter)] == p.Name {
				status = "active"
			}
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\n",
				p.Adapter,
				p.Name,
				len(p.AuthFiles),
				formatRelativeTime(p.UpdatedAt),
				status,
			)
		}
		return writer.Flush()
	},
}

var vaultDeleteCmd = &cobra.Command{
	Use:   "delete <adapter> <profile>",
	Short: "Delete a vault profile",
	Long: `Remove a profile from the vault.

This permanently deletes the saved auth files for the profile.

Examples:
  swarm vault delete claude old-work
  swarm vault delete codex backup --force`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		vp := getVaultPath()
		adapterName := strings.ToLower(args[0])
		profileName := args[1]

		adapter := vault.ParseAdapter(adapterName)
		if adapter == "" {
			return fmt.Errorf("unknown adapter: %s (valid: claude, codex, gemini, opencode)", adapterName)
		}

		// Check if profile exists
		profile, err := vault.Get(vp, adapter, profileName)
		if err != nil {
			return fmt.Errorf("profile not found: %w", err)
		}

		// Confirm deletion
		if !vaultDeleteForce && !yesFlag && IsInteractive() {
			fmt.Fprintf(os.Stderr, "Delete profile %q for %s? [y/N]: ", profileName, adapter)
			var response string
			fmt.Scanln(&response)
			if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
				fmt.Fprintln(os.Stdout, "Deletion cancelled.")
				return nil
			}
		}

		if err := vault.Delete(vp, adapter, profileName); err != nil {
			return fmt.Errorf("deletion failed: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]interface{}{
				"status":  "deleted",
				"adapter": adapter,
				"profile": profileName,
				"files":   profile.AuthFiles,
			})
		}

		fmt.Fprintf(os.Stdout, "Deleted profile %q for %s\n", profileName, adapter)
		return nil
	},
}

var vaultStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show active profile for each adapter",
	Long: `Display the currently active profile for each adapter.

Active profiles are detected by comparing content hashes of current
auth files against stored profiles.

Examples:
  swarm vault status`,
	RunE: func(cmd *cobra.Command, args []string) error {
		vp := getVaultPath()

		type adapterStatus struct {
			Adapter       string `json:"adapter"`
			ActiveProfile string `json:"active_profile,omitempty"`
			HasAuth       bool   `json:"has_auth"`
			ProfileCount  int    `json:"profile_count"`
		}

		statuses := make([]adapterStatus, 0, len(vault.AllAdapters()))

		for _, adapter := range vault.AllAdapters() {
			authPaths := vault.GetAuthPaths(adapter)
			profiles, _ := vault.List(vp, adapter)
			active, _ := vault.GetActive(vp, adapter)

			status := adapterStatus{
				Adapter:      string(adapter),
				HasAuth:      authPaths.HasAuth(),
				ProfileCount: len(profiles),
			}
			if active != nil {
				status.ActiveProfile = active.Name
			}
			statuses = append(statuses, status)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, statuses)
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "ADAPTER\tACTIVE PROFILE\tAUTH FILES\tSAVED PROFILES")
		for _, s := range statuses {
			activeProfile := "(none)"
			if s.ActiveProfile != "" {
				activeProfile = s.ActiveProfile
			}
			hasAuth := "no"
			if s.HasAuth {
				hasAuth = "yes"
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%d\n",
				s.Adapter,
				activeProfile,
				hasAuth,
				s.ProfileCount,
			)
		}
		return writer.Flush()
	},
}

var vaultPathsCmd = &cobra.Command{
	Use:   "paths [adapter]",
	Short: "Show auth file locations",
	Long: `Display the auth file locations for each adapter.

Shows where each adapter stores its authentication files.

Examples:
  swarm vault paths           # Show all adapter paths
  swarm vault paths claude    # Show Claude paths only`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var adapters []vault.Adapter
		if len(args) > 0 {
			adapter := vault.ParseAdapter(strings.ToLower(args[0]))
			if adapter == "" {
				return fmt.Errorf("unknown adapter: %s (valid: claude, codex, gemini, opencode)", args[0])
			}
			adapters = []vault.Adapter{adapter}
		} else {
			adapters = vault.AllAdapters()
		}

		type pathInfo struct {
			Adapter   string   `json:"adapter"`
			Primary   string   `json:"primary"`
			Secondary []string `json:"secondary,omitempty"`
			Exists    []string `json:"existing,omitempty"`
		}

		pathInfos := make([]pathInfo, 0, len(adapters))
		for _, adapter := range adapters {
			paths := vault.GetAuthPaths(adapter)
			info := pathInfo{
				Adapter:   string(adapter),
				Primary:   paths.Primary,
				Secondary: paths.Secondary,
				Exists:    paths.ExistingPaths(),
			}
			pathInfos = append(pathInfos, info)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, pathInfos)
		}

		for i, info := range pathInfos {
			if i > 0 {
				fmt.Fprintln(os.Stdout)
			}
			fmt.Fprintf(os.Stdout, "%s:\n", info.Adapter)
			exists := fileExists(info.Primary)
			fmt.Fprintf(os.Stdout, "  Primary: %s %s\n", info.Primary, formatExists(exists))
			for _, sec := range info.Secondary {
				exists := fileExists(sec)
				fmt.Fprintf(os.Stdout, "  Secondary: %s %s\n", sec, formatExists(exists))
			}
		}
		return nil
	},
}

var vaultClearCmd = &cobra.Command{
	Use:   "clear <adapter>",
	Short: "Remove current auth files",
	Long: `Remove current authentication files for an adapter.

This logs you out of the adapter by removing its auth files.
Use this before activating a new profile to ensure a clean switch.

Examples:
  swarm vault clear claude
  swarm vault clear codex --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		adapterName := strings.ToLower(args[0])

		adapter := vault.ParseAdapter(adapterName)
		if adapter == "" {
			return fmt.Errorf("unknown adapter: %s (valid: claude, codex, gemini, opencode)", adapterName)
		}

		authPaths := vault.GetAuthPaths(adapter)
		existing := authPaths.ExistingPaths()

		if len(existing) == 0 {
			if IsJSONOutput() || IsJSONLOutput() {
				return WriteOutput(os.Stdout, map[string]interface{}{
					"status":  "no_files",
					"adapter": adapter,
				})
			}
			fmt.Fprintf(os.Stdout, "No auth files found for %s\n", adapter)
			return nil
		}

		// Confirm clear
		if !vaultClearForce && !yesFlag && IsInteractive() {
			fmt.Fprintf(os.Stderr, "Remove %d auth file(s) for %s? [y/N]: ", len(existing), adapter)
			var response string
			fmt.Scanln(&response)
			if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
				fmt.Fprintln(os.Stdout, "Clear cancelled.")
				return nil
			}
		}

		if err := vault.Clear(adapter); err != nil {
			return fmt.Errorf("clear failed: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]interface{}{
				"status":  "cleared",
				"adapter": adapter,
				"files":   existing,
			})
		}

		fmt.Fprintf(os.Stdout, "Cleared auth files for %s\n", adapter)
		return nil
	},
}

// getVaultPath returns the vault path from flag or default.
func getVaultPath() string {
	if vaultPath != "" {
		return vaultPath
	}
	return vault.DefaultVaultPath()
}

// formatRelativeTime formats a time as a relative duration.
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// formatExists returns a status indicator for file existence.
func formatExists(exists bool) string {
	if exists {
		return "(exists)"
	}
	return "(not found)"
}

// WriteVaultOutput writes vault output in the appropriate format.
func WriteVaultOutput(v interface{}) error {
	if IsJSONOutput() || IsJSONLOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	return nil
}
