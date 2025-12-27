// Package cli provides the inject command for direct tmux injection.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	injectForce  bool
	injectFile   string
	injectStdin  bool
	injectEditor bool
)

func init() {
	rootCmd.AddCommand(injectCmd)

	injectCmd.Flags().BoolVarP(&injectForce, "force", "F", false, "skip confirmation for non-idle agents")
	injectCmd.Flags().StringVarP(&injectFile, "file", "f", "", "read message from file")
	injectCmd.Flags().BoolVar(&injectStdin, "stdin", false, "read message from stdin")
	injectCmd.Flags().BoolVar(&injectEditor, "editor", false, "compose message in $EDITOR")
}

var injectCmd = &cobra.Command{
	Use:   "inject <agent-id> [message]",
	Short: "Inject a message directly into an agent (bypasses queue)",
	Long: `Inject text directly into an agent's tmux pane via send-keys.

WARNING: This bypasses the scheduler queue and sends immediately.
Use 'swarm send' for safe, queue-based message dispatch.

Direct injection is useful for:
- Emergency interventions
- Debugging agent behavior  
- Immediate control commands

But it can cause issues if the agent is not ready to receive input.
Non-idle agents require confirmation (use --force to skip).`,
	Example: `  # Inject a message (will prompt for confirmation if agent is busy)
  swarm inject abc123 "Stop and commit"

  # Force inject without confirmation
  swarm inject --force abc123 "Emergency stop"

  # Inject from file
  swarm inject abc123 --file prompt.txt

  # Inject from stdin
  echo "Continue" | swarm inject abc123 --stdin

  # Compose in editor
  swarm inject abc123 --editor`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

		// Resolve message - use inject-specific flags
		message, err := resolveMessage(args[1:], injectFile, injectStdin, injectEditor)
		if err != nil {
			return err
		}

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		nodeRepo := db.NewNodeRepository(database)
		nodeService := node.NewService(nodeRepo)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		queueRepo := db.NewQueueRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		tmuxClient := tmux.NewLocalClient()
		agentService := agent.NewService(agentRepo, queueRepo, wsService, nil, tmuxClient, agentServiceOptions(database)...)

		resolved, err := findAgent(ctx, agentRepo, agentID)
		if err != nil {
			return err
		}

		// Check agent state and potentially require confirmation
		if !injectForce && !isAgentReadyForInject(resolved) {
			if SkipConfirmation() {
				return fmt.Errorf("agent is %s; use --force to inject without confirmation", resolved.State)
			}
			if !confirmInject(resolved) {
				fmt.Fprintln(os.Stderr, "Injection cancelled.")
				return nil
			}
		}

		if err := agentService.SendMessage(ctx, resolved.ID, message, &agent.SendMessageOptions{
			SkipIdleCheck: true,
		}); err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			return fmt.Errorf("failed to inject message: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"injected":       true,
				"agent_id":       resolved.ID,
				"message":        message,
				"bypassed_queue": true,
				"agent_state":    string(resolved.State),
			})
		}

		fmt.Printf("Warning: Direct injection to agent %s (bypassed queue)\n", shortID(resolved.ID))
		fmt.Println("Message injected")
		return nil
	},
}

// isAgentReadyForInject returns true if the agent is in a state that can safely receive input.
func isAgentReadyForInject(a *models.Agent) bool {
	switch a.State {
	case models.AgentStateIdle, models.AgentStateStopped, models.AgentStateStarting:
		return true
	default:
		return false
	}
}

// confirmInject prompts the user to confirm injection to a non-ready agent.
func confirmInject(a *models.Agent) bool {
	stateStr := formatAgentState(a.State)
	fmt.Printf("\nAgent %s is %s\n", shortID(a.ID), stateStr)

	// Give state-specific advice
	switch a.State {
	case models.AgentStateWorking:
		fmt.Println("  The agent is currently working on a task.")
		fmt.Println("  Injecting now may corrupt or confuse the agent's context.")
	case models.AgentStateAwaitingApproval:
		fmt.Println("  The agent is waiting for approval.")
		fmt.Println("  Consider using 'swarm agent approve' instead.")
	case models.AgentStatePaused:
		fmt.Println("  The agent is paused.")
		fmt.Println("  Consider using 'swarm agent resume' first.")
	case models.AgentStateRateLimited:
		fmt.Println("  The agent is rate-limited.")
		fmt.Println("  The message may not be processed immediately.")
	case models.AgentStateError:
		fmt.Println("  The agent is in an error state.")
		fmt.Println("  Consider using 'swarm agent restart' first.")
	}

	fmt.Println()
	return confirm("Proceed with injection?")
}
