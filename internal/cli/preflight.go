// Package cli provides preflight checks for CLI commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

// PreflightError carries a message and suggested next steps.
type PreflightError struct {
	Message  string
	Hint     string
	NextStep string
	Err      error
}

// Error implements error.
func (e *PreflightError) Error() string {
	if e == nil {
		return ""
	}

	parts := []string{e.Message}
	if e.Err != nil {
		parts[0] = fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	if e.Hint != "" {
		parts = append(parts, "Hint: "+e.Hint)
	}
	if e.NextStep != "" {
		parts = append(parts, "Next: "+e.NextStep)
	}
	return strings.Join(parts, "\n")
}

func runPreflight(cmd *cobra.Command) error {
	if !shouldRunPreflight(cmd) {
		return nil
	}

	ctx := context.Background()

	maybeWarnMissingConfig()

	if err := checkTmux(cmd); err != nil {
		return err
	}
	if err := checkSSH(cmd); err != nil {
		return err
	}
	if err := checkDatabase(ctx); err != nil {
		return err
	}
	maybeAutoImportWorkspaces(ctx)
	if err := checkWorkspacePath(cmd); err != nil {
		return err
	}

	return nil
}

func maybeAutoImportWorkspaces(ctx context.Context) {
	if appConfig == nil || !appConfig.WorkspaceDefaults.AutoImportExisting {
		return
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return
	}

	database, err := openDatabase()
	if err != nil {
		logger.Warn().Err(err).Msg("failed to open database for auto-import")
		return
	}
	defer database.Close()

	nodeRepo := db.NewNodeRepository(database)
	nodeService := node.NewService(nodeRepo)
	wsRepo := db.NewWorkspaceRepository(database)
	agentRepo := db.NewAgentRepository(database)
	wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

	report, err := wsService.RecoverOrphanedSessions(ctx, "", appConfig.WorkspaceDefaults.TmuxPrefix)
	if err != nil {
		if errors.Is(err, workspace.ErrNodeNotFound) {
			return
		}
		logger.Warn().Err(err).Msg("failed to auto-import existing tmux sessions")
		return
	}
	if report == nil {
		return
	}

	if imported := len(report.Imported); imported > 0 {
		logger.Info().Int("count", imported).Msg("imported existing tmux sessions")
	}
	if failed := len(report.Failures); failed > 0 {
		logger.Warn().Int("count", failed).Msg("some tmux sessions failed to import")
	}
}

func shouldRunPreflight(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}

	if cmd.Name() == "swarm" {
		if flag := cmd.Flag("version"); flag != nil && flag.Changed {
			return false
		}
	}

	path := cmd.CommandPath()
	switch {
	case strings.HasPrefix(path, "swarm init"):
		return false
	case strings.HasPrefix(path, "swarm migrate"):
		return false
	case strings.HasPrefix(path, "swarm completion"):
		return false
	case strings.HasPrefix(path, "swarm help"):
		return false
	}

	return true
}

func maybeWarnMissingConfig() {
	if IsJSONOutput() || IsJSONLOutput() {
		return
	}
	if configLoader == nil {
		return
	}
	if cfgFile != "" {
		return
	}
	if configLoader.ConfigFileUsed() != "" {
		return
	}

	fmt.Fprintln(os.Stderr, "Warning: no config file found.")
	fmt.Fprintln(os.Stderr, "Hint: run `swarm init` to create a config file.")
}

func checkTmux(cmd *cobra.Command) error {
	if !requiresTmux(cmd) {
		return nil
	}
	if _, err := exec.LookPath("tmux"); err == nil {
		return nil
	}

	return &PreflightError{
		Message:  "tmux is required for this command",
		Hint:     "Install tmux and ensure it is in PATH",
		NextStep: "swarm init",
	}
}

func requiresTmux(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Parent() == nil && cmd.Name() == "swarm" {
		return true
	}

	parent := cmd.Parent()
	if parent == nil {
		return false
	}

	switch parent.Name() {
	case "ws":
		switch cmd.Name() {
		case "list", "refresh":
			return false
		case "create":
			return !wsCreateNoTmux
		case "remove":
			return wsRemoveDestroy
		default:
			return true
		}
	case "agent":
		switch cmd.Name() {
		case "list", "queue", "pause", "resume":
			return false
		default:
			return true
		}
	}

	return false
}

func checkSSH(cmd *cobra.Command) error {
	if appConfig == nil {
		return nil
	}
	if appConfig.NodeDefaults.SSHBackend != models.SSHBackendSystem {
		return nil
	}
	if cmd == nil || cmd.Parent() == nil || cmd.Parent().Name() != "node" {
		return nil
	}
	if _, err := exec.LookPath("ssh"); err == nil {
		return nil
	}

	return &PreflightError{
		Message:  "ssh binary not found for system backend",
		Hint:     "Install OpenSSH client or switch to native SSH backend",
		NextStep: "swarm init",
	}
}

func checkDatabase(ctx context.Context) error {
	db, err := openDatabase()
	if err != nil {
		return &PreflightError{
			Message:  "database unavailable",
			Hint:     "Check database path and permissions",
			NextStep: "swarm init",
			Err:      err,
		}
	}
	defer db.Close()

	version, err := db.SchemaVersion(ctx)
	if err != nil {
		if isMissingSchemaTable(err) {
			return &PreflightError{
				Message:  "database not migrated",
				Hint:     "Run `swarm migrate up` to initialize the database",
				NextStep: "swarm init",
			}
		}
		return &PreflightError{
			Message:  "failed to read database schema version",
			Hint:     "Ensure the database is reachable and not locked",
			NextStep: "swarm init",
			Err:      err,
		}
	}
	if version == 0 {
		return &PreflightError{
			Message:  "database has no migrations applied",
			Hint:     "Run `swarm migrate up` to initialize the database",
			NextStep: "swarm init",
		}
	}

	return nil
}

func isMissingSchemaTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") && strings.Contains(msg, "schema_version")
}

func checkWorkspacePath(cmd *cobra.Command) error {
	if cmd == nil || cmd.Parent() == nil || cmd.Parent().Name() != "ws" {
		return nil
	}
	if cmd.Name() != "create" {
		return nil
	}
	if wsCreatePath == "" {
		return nil
	}

	path := wsCreatePath
	if path == "." {
		if cwd, err := os.Getwd(); err == nil {
			path = cwd
		}
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	return &PreflightError{
		Message:  "workspace path not found",
		Hint:     fmt.Sprintf("Check that %s exists and is readable", filepath.Clean(path)),
		NextStep: "swarm ws create --path <repo>",
	}
}
