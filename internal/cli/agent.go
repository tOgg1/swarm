// Package cli provides agent management CLI commands.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/queue"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	// agent spawn flags
	agentSpawnWorkspace string
	agentSpawnType      string
	agentSpawnCount     int
	agentSpawnProfile   string
	agentSpawnPrompt    string
	agentSpawnNoWait    bool

	// agent list flags
	agentListWorkspace string
	agentListState     string

	// agent terminate flags
	agentTerminateForce bool

	// agent pause flags
	agentPauseDuration string

	// agent send flags
	agentSendSkipIdle bool
	agentSendFile     string
	agentSendStdin    bool
	agentSendEditor   bool

	// agent queue flags
	agentQueueFile       string
	agentQueuePauseAfter int

	// agent approve flags
	agentApproveAll  bool
	agentApproveDeny bool
)

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentSpawnCmd)
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentStatusCmd)
	agentCmd.AddCommand(agentTerminateCmd)
	agentCmd.AddCommand(agentInterruptCmd)
	agentCmd.AddCommand(agentPauseCmd)
	agentCmd.AddCommand(agentResumeCmd)
	agentCmd.AddCommand(agentSendCmd)
	agentCmd.AddCommand(agentRestartCmd)
	agentCmd.AddCommand(agentQueueCmd)
	agentCmd.AddCommand(agentApproveCmd)

	// Spawn flags
	agentSpawnCmd.Flags().StringVarP(&agentSpawnWorkspace, "workspace", "w", "", "workspace name or ID (uses context if not set)")
	agentSpawnCmd.Flags().StringVarP(&agentSpawnType, "type", "t", "opencode", "agent type (opencode, claude-code, codex, gemini, generic)")
	agentSpawnCmd.Flags().IntVarP(&agentSpawnCount, "count", "n", 1, "number of agents to spawn")
	agentSpawnCmd.Flags().StringVarP(&agentSpawnProfile, "profile", "p", "", "account profile to use")
	agentSpawnCmd.Flags().StringVar(&agentSpawnPrompt, "prompt", "", "initial prompt to send after spawn")
	agentSpawnCmd.Flags().BoolVar(&agentSpawnNoWait, "no-wait", false, "don't wait for agent to be ready")

	// List flags
	agentListCmd.Flags().StringVarP(&agentListWorkspace, "workspace", "w", "", "filter by workspace (uses context if not set)")
	agentListCmd.Flags().StringVar(&agentListState, "state", "", "filter by state (working, idle, paused, error, etc.)")

	// Terminate flags
	agentTerminateCmd.Flags().BoolVarP(&agentTerminateForce, "force", "f", false, "force termination")

	// Pause flags
	agentPauseCmd.Flags().StringVarP(&agentPauseDuration, "duration", "d", "5m", "pause duration (e.g., 30s, 5m, 1h)")

	// Send flags
	agentSendCmd.Flags().BoolVar(&agentSendSkipIdle, "skip-idle-check", false, "send even if agent is not idle")
	agentSendCmd.Flags().StringVarP(&agentSendFile, "file", "f", "", "read message from file")
	agentSendCmd.Flags().BoolVar(&agentSendStdin, "stdin", false, "read message from stdin")
	agentSendCmd.Flags().BoolVar(&agentSendEditor, "editor", false, "compose message in $EDITOR")

	// Queue flags
	agentQueueCmd.Flags().StringVarP(&agentQueueFile, "file", "f", "", "file containing prompts (one per line)")
	agentQueueCmd.Flags().IntVar(&agentQueuePauseAfter, "pause-after", 0, "insert 60s pause after every N messages (0 = no pauses)")

	// Approve flags
	agentApproveCmd.Flags().BoolVar(&agentApproveAll, "all", false, "approve or deny all pending approvals for the agent")
	agentApproveCmd.Flags().BoolVar(&agentApproveDeny, "deny", false, "deny pending approvals instead of approving")
}

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agents",
	Long: `Manage AI coding agents running in workspaces.

Agents are instances of AI coding CLIs (opencode, claude-code, etc.) running
in tmux panes within workspaces.`,
}

var agentSpawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Spawn a new agent",
	Long: `Spawn one or more AI coding agents in a workspace.

The agent will be started in a new tmux pane in the workspace's session.

If --workspace is not specified, the workspace is resolved from:
1. Current directory (if in a workspace's git repo)
2. Stored context (set with 'swarm use <workspace>')`,
	Example: `  # Spawn in current workspace (from directory or context)
  swarm agent spawn

  # Spawn a single opencode agent
  swarm agent spawn --workspace my-project

  # Spawn 3 claude-code agents with a specific profile
  swarm agent spawn -w my-project -t claude-code -n 3 -p work-account

  # Spawn with an initial prompt
  swarm agent spawn -w my-project --prompt "Fix all linting errors"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		// Create services
		nodeRepo := db.NewNodeRepository(database)
		nodeService := node.NewService(nodeRepo)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		queueRepo := db.NewQueueRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		tmuxClient := tmux.NewLocalClient()
		agentService := agent.NewService(agentRepo, queueRepo, wsService, nil, tmuxClient, agentServiceOptions(database)...)

		// Resolve workspace from flag, directory, or stored context
		resolved, err := RequireWorkspaceContext(ctx, wsRepo, agentSpawnWorkspace)
		if err != nil {
			return err
		}
		ws, err := wsRepo.Get(ctx, resolved.WorkspaceID)
		if err != nil {
			return fmt.Errorf("failed to get workspace: %w", err)
		}

		approvalPolicy := ""
		if cfg := GetConfig(); cfg != nil {
			approvalPolicy = cfg.ApprovalPolicyForWorkspace(ws).Mode
		}

		// Parse agent type
		agentType := models.AgentType(agentSpawnType)
		switch agentType {
		case models.AgentTypeOpenCode, models.AgentTypeClaudeCode,
			models.AgentTypeCodex, models.AgentTypeGemini, models.AgentTypeGeneric:
			// Valid
		default:
			return fmt.Errorf("invalid agent type: %s", agentSpawnType)
		}

		// Spawn agents
		var agents []*models.Agent
		for i := 0; i < agentSpawnCount; i++ {
			opts := agent.SpawnOptions{
				WorkspaceID:    ws.ID,
				Type:           agentType,
				AccountID:      agentSpawnProfile,
				InitialPrompt:  agentSpawnPrompt,
				ApprovalPolicy: approvalPolicy,
			}
			// If --no-wait is set, use a very short timeout to skip waiting
			if agentSpawnNoWait {
				opts.ReadyTimeout = 1 * time.Millisecond
			}

			step := startProgress(fmt.Sprintf("Spawning agent %d/%d", i+1, agentSpawnCount))
			a, err := agentService.SpawnAgent(ctx, opts)
			if err != nil {
				step.Fail(err)
				if len(agents) > 0 {
					// Partial success
					if !IsJSONOutput() && !IsJSONLOutput() {
						fmt.Printf("Spawned %d/%d agents before error: %v\n", len(agents), agentSpawnCount, err)
					}
					break
				}
				return fmt.Errorf("failed to spawn agent: %w", err)
			}
			step.Done()
			agents = append(agents, a)
		}

		if len(agents) > 0 && ws.TmuxSession != "" {
			layoutManager := tmux.NewLayoutManager(
				tmuxClient,
				tmux.WithLayoutPreset(tmux.LayoutPresetTiled),
				tmux.WithLayoutWindow(tmux.AgentWindowName),
			)
			if err := layoutManager.Balance(ctx, ws.TmuxSession); err != nil && !IsJSONOutput() && !IsJSONLOutput() {
				fmt.Fprintf(os.Stderr, "Warning: failed to rebalance agent panes: %v\n", err)
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			if len(agents) == 1 {
				return WriteOutput(os.Stdout, agents[0])
			}
			return WriteOutput(os.Stdout, agents)
		}

		if len(agents) == 1 {
			a := agents[0]
			fmt.Printf("Agent spawned:\n")
			fmt.Printf("  ID:        %s\n", a.ID)
			fmt.Printf("  Type:      %s\n", a.Type)
			fmt.Printf("  Workspace: %s\n", ws.Name)
			fmt.Printf("  Pane:      %s\n", a.TmuxPane)
			fmt.Printf("  State:     %s\n", a.State)
			if a.AccountID != "" {
				fmt.Printf("  Profile:   %s\n", a.AccountID)
			}
		} else {
			fmt.Printf("Spawned %d agents:\n", len(agents))
			for _, a := range agents {
				fmt.Printf("  %s (%s) - %s\n", a.ID, a.Type, a.State)
			}
		}

		return nil
	},
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agents",
	Long: `List all agents managed by Swarm.

If --workspace is not specified, filters by workspace from context (if set).
Use --workspace="" to list all agents across workspaces.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

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

		// Build options
		opts := agent.ListAgentsOptions{
			IncludeQueueLength: true,
		}

		// Use context resolution for workspace filter (optional - just for filtering)
		if agentListWorkspace != "" {
			ws, err := findWorkspace(ctx, wsRepo, agentListWorkspace)
			if err != nil {
				return err
			}
			opts.WorkspaceID = ws.ID
		} else if !cmd.Flags().Changed("workspace") {
			// Use context if flag wasn't explicitly set
			resolved, _ := ResolveWorkspaceContext(ctx, wsRepo, "")
			if resolved != nil && resolved.WorkspaceID != "" {
				opts.WorkspaceID = resolved.WorkspaceID
			}
		}

		if agentListState != "" {
			state := models.AgentState(agentListState)
			opts.State = &state
		}

		agents, err := agentService.ListAgents(ctx, opts)
		if err != nil {
			return fmt.Errorf("failed to list agents: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, agents)
		}

		if len(agents) == 0 {
			fmt.Println("No agents found")
			return nil
		}

		rows := make([][]string, 0, len(agents))
		for _, a := range agents {
			workspaceID := "-"
			if a.WorkspaceID != "" {
				workspaceID = shortID(a.WorkspaceID)
			}
			pane := a.TmuxPane
			if pane == "" {
				pane = "-"
			}
			rows = append(rows, []string{
				shortID(a.ID),
				string(a.Type),
				formatAgentState(a.State),
				workspaceID,
				pane,
				fmt.Sprintf("%d", a.QueueLength),
			})
		}
		return writeTable(os.Stdout, []string{"ID", "TYPE", "STATE", "WORKSPACE", "PANE", "QUEUE"}, rows)
	},
}

var agentStatusCmd = &cobra.Command{
	Use:   "status <agent-id>",
	Short: "Show agent status",
	Long:  "Display detailed status for an agent including state, queue, and recent activity.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

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

		stateResult, err := agentService.GetAgentState(ctx, resolved.ID)
		if err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			return fmt.Errorf("failed to get agent status: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, stateResult)
		}

		a := stateResult.Agent
		fmt.Printf("Agent: %s\n", a.ID)
		fmt.Printf("Type:  %s\n", a.Type)
		fmt.Printf("State: %s\n", formatAgentState(a.State))
		fmt.Printf("  Confidence: %s\n", a.StateInfo.Confidence)
		fmt.Printf("  Reason:     %s\n", a.StateInfo.Reason)
		if len(a.StateInfo.Evidence) > 0 {
			fmt.Printf("  Evidence:\n")
			for _, e := range a.StateInfo.Evidence {
				fmt.Printf("    - %s\n", e)
			}
		}
		fmt.Println()

		fmt.Printf("Workspace: %s\n", a.WorkspaceID)
		fmt.Printf("Pane:      %s\n", a.TmuxPane)
		fmt.Printf("Pane Active: %v\n", stateResult.PaneActive)
		fmt.Println()

		if a.AccountID != "" {
			fmt.Printf("Profile: %s\n", a.AccountID)
		}

		fmt.Printf("Queue Length: %d\n", stateResult.QueueLength)

		if a.LastActivity != nil {
			fmt.Printf("Last Activity: %s\n", a.LastActivity.Format(time.RFC3339))
		}

		if a.PausedUntil != nil {
			fmt.Printf("Paused Until: %s\n", a.PausedUntil.Format(time.RFC3339))
		}

		fmt.Printf("\nCreated: %s\n", a.CreatedAt.Format(time.RFC3339))

		if stateResult.LastOutput != "" {
			fmt.Printf("\nLast Output (truncated):\n")
			output := stateResult.LastOutput
			if len(output) > 500 {
				output = output[len(output)-500:]
			}
			fmt.Println(output)
		}

		return nil
	},
}

var agentTerminateCmd = &cobra.Command{
	Use:     "terminate <agent-id>",
	Aliases: []string{"kill", "rm"},
	Short:   "Terminate an agent",
	Long:    "Stop and remove an agent. This kills the tmux pane and removes the agent record.",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

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

		// Confirm destructive action
		impact := "This will kill the tmux pane and remove the agent record."
		if !ConfirmDestructiveAction("agent", resolved.ID, impact) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		}

		step := startProgress("Terminating agent")
		if err := agentService.TerminateAgent(ctx, resolved.ID); err != nil {
			step.Fail(err)
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			return fmt.Errorf("failed to terminate agent: %w", err)
		}
		step.Done()

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"terminated": true,
				"agent_id":   resolved.ID,
			})
		}

		fmt.Printf("Agent '%s' terminated\n", resolved.ID)
		return nil
	},
}

var agentInterruptCmd = &cobra.Command{
	Use:   "interrupt <agent-id>",
	Short: "Interrupt an agent",
	Long:  "Send Ctrl+C to an agent to interrupt its current operation.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

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

		if err := agentService.InterruptAgent(ctx, resolved.ID); err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			return fmt.Errorf("failed to interrupt agent: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"interrupted": true,
				"agent_id":    resolved.ID,
			})
		}

		fmt.Printf("Agent '%s' interrupted\n", resolved.ID)
		return nil
	},
}

var agentPauseCmd = &cobra.Command{
	Use:   "pause <agent-id>",
	Short: "Pause an agent",
	Long:  "Pause an agent for a specified duration. The scheduler will skip paused agents.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

		duration, err := time.ParseDuration(agentPauseDuration)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
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

		if err := agentService.PauseAgent(ctx, resolved.ID, duration); err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			return fmt.Errorf("failed to pause agent: %w", err)
		}

		pausedUntil := time.Now().Add(duration)

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"paused":       true,
				"agent_id":     resolved.ID,
				"duration":     duration.String(),
				"paused_until": pausedUntil.Format(time.RFC3339),
			})
		}

		fmt.Printf("Agent '%s' paused until %s\n", resolved.ID, pausedUntil.Format(time.RFC3339))
		return nil
	},
}

var agentResumeCmd = &cobra.Command{
	Use:   "resume <agent-id>",
	Short: "Resume a paused agent",
	Long:  "Resume an agent that was previously paused.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

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

		if err := agentService.ResumeAgent(ctx, resolved.ID); err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			return fmt.Errorf("failed to resume agent: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"resumed":  true,
				"agent_id": resolved.ID,
			})
		}

		fmt.Printf("Agent '%s' resumed\n", resolved.ID)
		return nil
	},
}

var agentSendCmd = &cobra.Command{
	Use:   "send <agent-id> [message]",
	Short: "Send a message to an agent",
	Long: `Send a text message to an agent. By default, requires the agent to be idle.

Provide the message inline, or use --file, --stdin, or --editor to send multi-line input.`,
	Example: `  # Send a one-line message
  swarm agent send abc123 "Fix the lint errors"

  # Send a multi-line message from a file
  swarm agent send abc123 --file prompt.txt

  # Send from stdin
  cat prompt.txt | swarm agent send abc123 --stdin

  # Compose in $EDITOR
  swarm agent send abc123 --editor`,
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

		opts := &agent.SendMessageOptions{
			SkipIdleCheck: agentSendSkipIdle,
		}

		resolved, err := findAgent(ctx, agentRepo, agentID)
		if err != nil {
			return err
		}

		if err := agentService.SendMessage(ctx, resolved.ID, message, opts); err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			if errors.Is(err, agent.ErrAgentNotIdle) {
				return fmt.Errorf("agent is not idle (use --skip-idle-check to force)")
			}
			return fmt.Errorf("failed to send message: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"sent":     true,
				"agent_id": resolved.ID,
				"message":  message,
			})
		}

		fmt.Printf("Message sent to agent '%s'\n", resolved.ID)
		return nil
	},
}

var agentRestartCmd = &cobra.Command{
	Use:   "restart <agent-id>",
	Short: "Restart an agent",
	Long:  "Terminate and respawn an agent with the same configuration.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

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

		newAgent, err := agentService.RestartAgent(ctx, resolved.ID)
		if err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				return fmt.Errorf("agent '%s' not found", resolved.ID)
			}
			return fmt.Errorf("failed to restart agent: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"restarted":    true,
				"old_agent_id": resolved.ID,
				"new_agent":    newAgent,
			})
		}

		fmt.Printf("Agent restarted:\n")
		fmt.Printf("  Old ID: %s\n", resolved.ID)
		fmt.Printf("  New ID: %s\n", newAgent.ID)
		fmt.Printf("  Pane:   %s\n", newAgent.TmuxPane)
		fmt.Printf("  State:  %s\n", newAgent.State)
		return nil
	},
}

var agentQueueCmd = &cobra.Command{
	Use:   "queue <agent-id> [messages...]",
	Short: "Queue messages for an agent",
	Long: `Queue one or more messages for an agent.

Messages can be provided as arguments or from a file (one per line).
Special markers in the file:
  #PAUSE          - Insert a 60s pause
  #PAUSE:120      - Insert a 120s pause
  #                - Comment line (ignored)
  (blank lines)   - Ignored`,
	Example: `  # Queue a single message
  swarm agent queue abc123 "Fix the bug"

  # Queue multiple messages
  swarm agent queue abc123 "First task" "Second task" "Third task"

  # Queue from a file
  swarm agent queue abc123 --file prompts.txt

  # Auto-insert pauses every 5 messages
  swarm agent queue abc123 --file prompts.txt --pause-after 5`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		agentID := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		agentRepo := db.NewAgentRepository(database)
		queueRepo := db.NewQueueRepository(database)
		queueService := queue.NewService(queueRepo)

		// Verify agent exists
		a, err := findAgent(ctx, agentRepo, agentID)
		if err != nil {
			return err
		}

		// Collect messages
		var messages []string

		if agentQueueFile != "" {
			// Read from file
			file, err := os.Open(agentQueueFile)
			if err != nil {
				return fmt.Errorf("failed to open file: %w", err)
			}
			defer file.Close()

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue // Skip blank lines
				}
				if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#PAUSE") {
					continue // Skip comments (but not pause markers)
				}
				messages = append(messages, line)
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}
		}

		// Add CLI arguments as messages
		if len(args) > 1 {
			messages = append(messages, args[1:]...)
		}

		if len(messages) == 0 {
			return errors.New("no messages to queue (provide arguments or use --file)")
		}

		// Build queue items
		var items []*models.QueueItem
		messageCount := 0

		for _, msg := range messages {
			// Check for pause markers
			if strings.HasPrefix(msg, "#PAUSE") {
				pauseSecs := 60 // default
				if strings.HasPrefix(msg, "#PAUSE:") {
					var duration int
					if _, err := fmt.Sscanf(msg, "#PAUSE:%d", &duration); err == nil && duration > 0 {
						pauseSecs = duration
					}
				}

				payload := models.PausePayload{
					DurationSeconds: pauseSecs,
					Reason:          "scheduled pause from queue file",
				}
				payloadBytes, _ := json.Marshal(payload)

				items = append(items, &models.QueueItem{
					AgentID: a.ID,
					Type:    models.QueueItemTypePause,
					Status:  models.QueueItemStatusPending,
					Payload: payloadBytes,
				})
				continue
			}

			// Regular message
			payload := models.MessagePayload{Text: msg}
			payloadBytes, _ := json.Marshal(payload)

			items = append(items, &models.QueueItem{
				AgentID: a.ID,
				Type:    models.QueueItemTypeMessage,
				Status:  models.QueueItemStatusPending,
				Payload: payloadBytes,
			})

			messageCount++

			// Insert auto-pause if configured
			if agentQueuePauseAfter > 0 && messageCount%agentQueuePauseAfter == 0 {
				pausePayload := models.PausePayload{
					DurationSeconds: 60,
					Reason:          fmt.Sprintf("auto-pause after %d messages", agentQueuePauseAfter),
				}
				pauseBytes, _ := json.Marshal(pausePayload)

				items = append(items, &models.QueueItem{
					AgentID: a.ID,
					Type:    models.QueueItemTypePause,
					Status:  models.QueueItemStatusPending,
					Payload: pauseBytes,
				})
			}
		}

		// Enqueue all items
		if err := queueService.Enqueue(ctx, a.ID, items...); err != nil {
			return fmt.Errorf("failed to enqueue items: %w", err)
		}

		// Count item types for output
		msgCount := 0
		pauseCount := 0
		for _, item := range items {
			switch item.Type {
			case models.QueueItemTypeMessage:
				msgCount++
			case models.QueueItemTypePause:
				pauseCount++
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"queued":      true,
				"agent_id":    a.ID,
				"messages":    msgCount,
				"pauses":      pauseCount,
				"total_items": len(items),
			})
		}

		fmt.Printf("Queued %d items for agent '%s':\n", len(items), truncate(a.ID, 12))
		fmt.Printf("  Messages: %d\n", msgCount)
		if pauseCount > 0 {
			fmt.Printf("  Pauses:   %d\n", pauseCount)
		}
		return nil
	},
}

var agentApproveCmd = &cobra.Command{
	Use:   "approve <agent-id>",
	Short: "Approve or deny pending approvals",
	Long: `Handle pending approvals for an agent.

By default, this approves pending requests. Use --deny to deny instead.
Use --all to apply the action to every pending approval.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		agentRepo := db.NewAgentRepository(database)
		approvalRepo := db.NewApprovalRepository(database)

		agentRecord, err := findAgent(ctx, agentRepo, args[0])
		if err != nil {
			return err
		}

		pending, err := approvalRepo.ListPendingByAgent(ctx, agentRecord.ID)
		if err != nil {
			return fmt.Errorf("failed to list pending approvals: %w", err)
		}

		if len(pending) == 0 {
			if IsJSONOutput() || IsJSONLOutput() {
				return WriteOutput(os.Stdout, pending)
			}
			fmt.Printf("No pending approvals for agent %s.\n", agentRecord.ID)
			return nil
		}

		if len(pending) > 1 && !agentApproveAll {
			return fmt.Errorf("agent has %d pending approvals; use --all to process all", len(pending))
		}

		action := models.ApprovalStatusApproved
		if agentApproveDeny {
			action = models.ApprovalStatusDenied
		}

		resolvedAt := time.Now().UTC()
		updated := make([]*models.Approval, 0, len(pending))

		for _, approval := range pending {
			if err := approvalRepo.UpdateStatus(ctx, approval.ID, action, "user"); err != nil {
				return fmt.Errorf("failed to update approval %s: %w", approval.ID, err)
			}
			approval.Status = action
			approval.ResolvedBy = "user"
			approval.ResolvedAt = &resolvedAt
			updated = append(updated, approval)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, updated)
		}

		for _, approval := range updated {
			fmt.Printf("Approval %s: %s\n", approval.ID, approval.RequestType)
			fmt.Printf("  Status: %s\n", approval.Status)
			fmt.Printf("  Details:\n%s\n", formatApprovalDetails(approval.RequestDetails))
		}

		return nil
	},
}

func formatApprovalDetails(details json.RawMessage) string {
	trimmed := strings.TrimSpace(string(details))
	if trimmed == "" {
		return "    (none)"
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(trimmed), "    ", "  "); err != nil {
		return "    " + trimmed
	}
	return pretty.String()
}

func resolveSendMessage(args []string) (string, error) {
	hasInline := len(args) > 1
	sourceCount := 0
	if hasInline {
		sourceCount++
	}
	if agentSendFile != "" {
		sourceCount++
	}
	if agentSendStdin {
		sourceCount++
	}
	if agentSendEditor {
		sourceCount++
	}

	if sourceCount == 0 {
		return "", errors.New("message required (provide <message>, --file, --stdin, or --editor)")
	}
	if sourceCount > 1 {
		return "", errors.New("choose only one message source: <message>, --file, --stdin, or --editor")
	}

	var message string
	var err error

	switch {
	case agentSendFile != "":
		message, err = readMessageFromFile(agentSendFile)
	case agentSendStdin:
		message, err = readMessageFromStdin()
	case agentSendEditor:
		message, err = readMessageFromEditor()
	default:
		message = strings.Join(args[1:], " ")
	}

	if err != nil {
		return "", err
	}
	if strings.TrimSpace(message) == "" {
		return "", errors.New("message is empty (provide content via <message>, --file, --stdin, or --editor)")
	}

	return message, nil
}

func readMessageFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read message file %q: %w", path, err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("message file %q is empty (add content or use --editor/--stdin)", path)
	}
	return string(data), nil
}

func readMessageFromStdin() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read stdin: %w", err)
	}
	if len(data) == 0 {
		return "", errors.New("stdin was empty (pipe a message or use --file/--editor)")
	}
	return string(data), nil
}

func readMessageFromEditor() (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		return "", errors.New("EDITOR is not set (set $EDITOR or use --file/--stdin)")
	}

	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return "", errors.New("EDITOR is empty")
	}

	tmpFile, err := os.CreateTemp("", "swarm-agent-send-*.txt")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}
	defer os.Remove(tmpPath)

	editorCmd := exec.Command(parts[0], append(parts[1:], tmpPath)...)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr

	if err := editorCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to run editor %q: %w", parts[0], err)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", fmt.Errorf("failed to read editor output: %w", err)
	}
	if len(data) == 0 {
		return "", errors.New("editor output was empty (save content before exiting)")
	}
	return string(data), nil
}
