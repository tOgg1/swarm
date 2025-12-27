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
	"github.com/opencode-ai/swarm/internal/queue"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

var injectForce bool

func init() {
	rootCmd.AddCommand(injectCmd)

	injectCmd.Flags().BoolVar(&injectForce, "force", false, "skip confirmation for non-idle agents")
	injectCmd.Flags().StringVarP(&agentSendFile, "file", "f", "", "read message from file")
	injectCmd.Flags().BoolVar(&agentSendStdin, "stdin", false, "read message from stdin")
	injectCmd.Flags().BoolVar(&agentSendEditor, "editor", false, "compose message in $EDITOR")
}

var injectCmd = &cobra.Command{
	Use:   "inject <agent-id> [message]",
	Short: "Inject a message directly into an agent (dangerous)",
	Long: `Inject a message directly into an agent's tmux pane, bypassing the queue.

This is a dangerous operation intended for emergencies or debugging.
Use 'swarm send' for safe, queued dispatch.`,
	Example: `  # Direct injection of a one-line message
  swarm inject abc123 "Fix the lint errors"

  # Send a multi-line message from a file
  swarm inject abc123 --file prompt.txt

  # Send from stdin
  cat prompt.txt | swarm inject abc123 --stdin

  # Compose in $EDITOR
  swarm inject abc123 --editor`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

		message, err := resolveSendMessage(args)
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

		if !injectForce && resolved.State != models.AgentStateIdle {
			if SkipConfirmation() {
				return fmt.Errorf("agent is %s; use --force to inject without confirmation", resolved.State)
			}
			prompt := fmt.Sprintf("Agent '%s' is %s. Inject anyway?", resolved.ID, resolved.State)
			if !confirm(prompt) {
				fmt.Fprintln(os.Stderr, "Cancelled.")
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
				"sent":           true,
				"agent_id":       resolved.ID,
				"message":        message,
				"bypassed_queue": true,
			})
		}

		fmt.Fprintln(os.Stderr, "Warning: Direct injection bypasses queue. Use `swarm send` for safe dispatch.")
		fmt.Printf("Warning: Direct injection to agent %s\n", resolved.ID)
		fmt.Println("Message sent (bypassed queue)")
		return nil
	},
}
