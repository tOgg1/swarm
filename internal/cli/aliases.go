// Package cli provides fast aliases for common commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	// up command flags
	upAgents  int
	upPath    string
	upType    string
	upProfile string
	upNoTmux  bool
	upNode    string
)

func init() {
	// Register fast aliases at root level
	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(psCmd)

	// up flags
	upCmd.Flags().IntVarP(&upAgents, "agents", "n", 1, "number of agents to spawn")
	upCmd.Flags().StringVarP(&upPath, "path", "p", "", "repository path (default: current directory)")
	upCmd.Flags().StringVarP(&upType, "type", "t", "opencode", "agent type (opencode, claude-code, codex, gemini, generic)")
	upCmd.Flags().StringVar(&upProfile, "profile", "", "account profile to use")
	upCmd.Flags().BoolVar(&upNoTmux, "no-tmux", false, "don't create tmux session")
	upCmd.Flags().StringVar(&upNode, "node", "", "node name or ID (default: local)")

	// ls inherits from ws list
	lsCmd.Flags().StringVar(&wsListNode, "node", "", "filter by node")

	// ps inherits from agent list
	psCmd.Flags().StringVar(&agentListState, "state", "", "filter by state")
	psCmd.Flags().StringVarP(&agentListWorkspace, "workspace", "w", "", "filter by workspace")
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Create workspace and spawn agents",
	Long: `Quick start command to create a workspace and spawn agents.

Creates a workspace for the current directory (or specified path) and
spawns one or more agents. This is the fastest way to get started.`,
	Example: `  # Quick start in current directory
  swarm up

  # Spawn 3 agents
  swarm up --agents 3

  # Use a specific path
  swarm up --path /path/to/repo

  # Use a specific agent type
  swarm up --type claude-code

  # Use a specific profile
  swarm up --profile work`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		// Set up services
		nodeRepo := db.NewNodeRepository(database)
		nodeService := node.NewService(nodeRepo)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		queueRepo := db.NewQueueRepository(database)
		eventRepo := db.NewEventRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo, workspace.WithEventRepository(eventRepo))
		tmuxClient := tmux.NewLocalClient()
		agentService := agent.NewService(agentRepo, queueRepo, wsService, nil, tmuxClient, agentServiceOptions(database)...)

		// Resolve path (default to cwd)
		repoPath := upPath
		if repoPath == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get current directory: %w", err)
			}
			repoPath = cwd
		}
		repoPath, err = filepath.Abs(repoPath)
		if err != nil {
			return fmt.Errorf("failed to resolve path: %w", err)
		}

		// Resolve node
		nodeID := ""
		if upNode != "" {
			n, err := findNode(ctx, nodeService, upNode)
			if err != nil {
				return err
			}
			nodeID = n.ID
		}

		// Check if workspace already exists for this path
		existingWs, err := wsRepo.GetByNodeAndPath(ctx, nodeID, repoPath)
		var ws *models.Workspace

		if err == nil && existingWs != nil {
			// Workspace exists, use it
			ws = existingWs
			if !IsJSONOutput() && !IsJSONLOutput() {
				fmt.Printf("Using existing workspace: %s\n", ws.Name)
			}
		} else {
			// Create workspace
			step := startProgress("Creating workspace")
			input := workspace.CreateWorkspaceInput{
				NodeID:            nodeID,
				RepoPath:          repoPath,
				CreateTmuxSession: !upNoTmux,
			}
			ws, err = wsService.CreateWorkspace(ctx, input)
			if err != nil {
				step.Fail(err)
				if errors.Is(err, workspace.ErrWorkspaceAlreadyExists) {
					return fmt.Errorf("workspace already exists for this path")
				}
				if errors.Is(err, workspace.ErrRepoValidationFailed) {
					return fmt.Errorf("invalid repository path: %w", err)
				}
				return fmt.Errorf("failed to create workspace: %w", err)
			}
			step.Done()
		}

		// Spawn agents
		spawnedAgents := make([]*models.Agent, 0, upAgents)
		for i := 0; i < upAgents; i++ {
			step := startProgress(fmt.Sprintf("Spawning agent %d/%d", i+1, upAgents))

			agentType := models.AgentType(upType)
			opts := agent.SpawnOptions{
				WorkspaceID: ws.ID,
				Type:        agentType,
			}

			a, err := agentService.SpawnAgent(ctx, opts)
			if err != nil {
				step.Fail(err)
				return fmt.Errorf("failed to spawn agent: %w", err)
			}
			step.Done()
			spawnedAgents = append(spawnedAgents, a)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"workspace": ws,
				"agents":    spawnedAgents,
			})
		}

		// Human-readable output
		fmt.Println()
		fmt.Printf("Workspace: %s (%s)\n", ws.Name, shortID(ws.ID))
		fmt.Printf("Path:      %s\n", ws.RepoPath)
		if ws.TmuxSession != "" {
			fmt.Printf("Session:   %s\n", ws.TmuxSession)
		}
		fmt.Println()
		fmt.Printf("Agents: %d spawned\n", len(spawnedAgents))
		for _, a := range spawnedAgents {
			fmt.Printf("  - %s (%s)\n", shortID(a.ID), a.Type)
		}
		fmt.Println()

		if ws.TmuxSession != "" && !upNoTmux {
			fmt.Printf("Attach with: tmux attach -t %s\n", ws.TmuxSession)
		}

		return nil
	},
}

var lsCmd = &cobra.Command{
	Use:     "ls",
	Short:   "List workspaces (alias: ws list)",
	Aliases: []string{"workspaces"},
	Long:    `List all workspaces. This is an alias for 'swarm ws list'.`,
	Example: `  swarm ls
  swarm ls --node prod`,
	RunE: wsListCmd.RunE,
}

var psCmd = &cobra.Command{
	Use:     "ps",
	Short:   "List agents (alias: agent list)",
	Aliases: []string{"agents"},
	Long:    `List all agents. This is an alias for 'swarm agent list'.`,
	Example: `  swarm ps
  swarm ps --state idle
  swarm ps -w my-project`,
	RunE: agentListCmd.RunE,
}
