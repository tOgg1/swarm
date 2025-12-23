// Package cli provides workspace management CLI commands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/beads"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	// ws create flags
	wsCreatePath    string
	wsCreateNode    string
	wsCreateName    string
	wsCreateSession string
	wsCreateNoTmux  bool

	// ws import flags
	wsImportSession  string
	wsImportNode     string
	wsImportName     string
	wsImportRepoPath string

	// ws list flags
	wsListNode   string
	wsListStatus string

	// ws remove flags
	wsRemoveForce   bool
	wsRemoveDestroy bool

	// ws kill flags
	wsKillForce bool
)

func init() {
	rootCmd.AddCommand(wsCmd)
	wsCmd.AddCommand(wsCreateCmd)
	wsCmd.AddCommand(wsImportCmd)
	wsCmd.AddCommand(wsListCmd)
	wsCmd.AddCommand(wsStatusCmd)
	wsCmd.AddCommand(wsBeadsStatusCmd)
	wsCmd.AddCommand(wsAttachCmd)
	wsCmd.AddCommand(wsRemoveCmd)
	wsCmd.AddCommand(wsKillCmd)
	wsCmd.AddCommand(wsRefreshCmd)

	// Create flags
	wsCreateCmd.Flags().StringVar(&wsCreatePath, "path", "", "repository path (required)")
	wsCreateCmd.Flags().StringVar(&wsCreateNode, "node", "", "node name or ID (default: local)")
	wsCreateCmd.Flags().StringVar(&wsCreateName, "name", "", "workspace name (default: derived from path)")
	wsCreateCmd.Flags().StringVar(&wsCreateSession, "session", "", "tmux session name (default: auto-generated)")
	wsCreateCmd.Flags().BoolVar(&wsCreateNoTmux, "no-tmux", false, "don't create tmux session")
	wsCreateCmd.MarkFlagRequired("path")

	// Import flags
	wsImportCmd.Flags().StringVar(&wsImportSession, "session", "", "tmux session name (required)")
	wsImportCmd.Flags().StringVar(&wsImportNode, "node", "", "node name or ID (required)")
	wsImportCmd.Flags().StringVar(&wsImportName, "name", "", "workspace name (default: session name)")
	wsImportCmd.Flags().StringVar(&wsImportRepoPath, "repo-path", "", "repository path override (use when multiple repos are detected)")
	wsImportCmd.MarkFlagRequired("session")
	wsImportCmd.MarkFlagRequired("node")

	// List flags
	wsListCmd.Flags().StringVar(&wsListNode, "node", "", "filter by node")
	wsListCmd.Flags().StringVar(&wsListStatus, "status", "", "filter by status (active, archived)")

	// Remove flags
	wsRemoveCmd.Flags().BoolVarP(&wsRemoveForce, "force", "f", false, "force removal even with active agents")
	wsRemoveCmd.Flags().BoolVar(&wsRemoveDestroy, "destroy", false, "also kill the tmux session")

	// Kill flags
	wsKillCmd.Flags().BoolVarP(&wsKillForce, "force", "f", false, "force kill even with active agents")
}

var wsCmd = &cobra.Command{
	Use:     "ws",
	Aliases: []string{"workspace"},
	Short:   "Manage workspaces",
	Long: `Manage Swarm workspaces.

A workspace represents a repository with an associated tmux session where agents run.`,
}

var wsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new workspace",
	Long: `Create a new workspace for a repository.

By default, a tmux session is created in the repository directory.`,
	Example: `  # Create workspace for current directory
  swarm ws create --path .

  # Create with custom name
  swarm ws create --path /home/user/myproject --name my-project

  # Create on a specific node
  swarm ws create --path /data/repos/api --node prod-server`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		// Resolve services
		nodeRepo := db.NewNodeRepository(database)
		nodeService := node.NewService(nodeRepo)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		eventRepo := db.NewEventRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo, workspace.WithEventRepository(eventRepo))

		// Resolve node ID if name provided
		nodeID := ""
		if wsCreateNode != "" {
			n, err := findNode(ctx, nodeService, wsCreateNode)
			if err != nil {
				return err
			}
			nodeID = n.ID
		}

		input := workspace.CreateWorkspaceInput{
			NodeID:            nodeID,
			RepoPath:          wsCreatePath,
			Name:              wsCreateName,
			TmuxSession:       wsCreateSession,
			CreateTmuxSession: !wsCreateNoTmux,
		}

		step := startProgress("Creating workspace")
		ws, err := wsService.CreateWorkspace(ctx, input)
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

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, ws)
		}

		fmt.Printf("Workspace created:\n")
		fmt.Printf("  ID:      %s\n", ws.ID)
		fmt.Printf("  Name:    %s\n", ws.Name)
		fmt.Printf("  Path:    %s\n", ws.RepoPath)
		fmt.Printf("  Session: %s\n", ws.TmuxSession)
		if ws.GitInfo != nil && ws.GitInfo.Branch != "" {
			fmt.Printf("  Branch:  %s\n", ws.GitInfo.Branch)
		}

		if !wsCreateNoTmux {
			fmt.Printf("\nAttach with: tmux attach -t %s\n", ws.TmuxSession)
		}

		return nil
	},
}

var wsImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import an existing tmux session",
	Long: `Import an existing tmux session as a workspace.

This allows Swarm to manage agents in sessions created outside of Swarm.`,
	Example: `  swarm ws import --session my-project --node localhost`,
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
		eventRepo := db.NewEventRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo, workspace.WithEventRepository(eventRepo))

		// Resolve node
		n, err := findNode(ctx, nodeService, wsImportNode)
		if err != nil {
			return err
		}

		input := workspace.ImportWorkspaceInput{
			NodeID:      n.ID,
			TmuxSession: wsImportSession,
			Name:        wsImportName,
			RepoPath:    wsImportRepoPath,
		}

		step := startProgress("Importing workspace")
		ws, err := wsService.ImportWorkspace(ctx, input)
		if err != nil {
			var ambiguous *workspace.AmbiguousRepoRootError
			if errors.As(err, &ambiguous) {
				step.Fail(err)
				if wsImportRepoPath != "" {
					return fmt.Errorf("failed to import workspace: %w", err)
				}
				if !IsInteractive() || IsJSONOutput() || IsJSONLOutput() {
					return fmt.Errorf("ambiguous repository roots detected: %s (use --repo-path to select)", strings.Join(ambiguous.Roots, ", "))
				}

				selection, selectErr := promptRepoRootSelection(ambiguous.Roots)
				if selectErr != nil {
					return selectErr
				}
				input.RepoPath = selection
				step = startProgress("Importing workspace")
				ws, err = wsService.ImportWorkspace(ctx, input)
			}
		}
		if err != nil {
			step.Fail(err)
			if errors.Is(err, workspace.ErrWorkspaceAlreadyExists) {
				return fmt.Errorf("workspace already exists for this session")
			}
			if errors.Is(err, workspace.ErrRepoValidationFailed) {
				return fmt.Errorf("invalid repository path: %w", err)
			}
			return fmt.Errorf("failed to import workspace: %w", err)
		}
		step.Done()

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, ws)
		}

		fmt.Printf("Workspace imported:\n")
		fmt.Printf("  ID:      %s\n", ws.ID)
		fmt.Printf("  Name:    %s\n", ws.Name)
		fmt.Printf("  Path:    %s\n", ws.RepoPath)
		fmt.Printf("  Session: %s\n", ws.TmuxSession)

		return nil
	},
}

var wsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspaces",
	Long:  "List all workspaces managed by Swarm.",
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
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		// Build options
		opts := workspace.ListWorkspacesOptions{
			IncludeAgentCounts: true,
		}

		if wsListNode != "" {
			// Resolve node
			n, err := findNode(ctx, nodeService, wsListNode)
			if err != nil {
				return err
			}
			opts.NodeID = n.ID
		}

		if wsListStatus != "" {
			status := models.WorkspaceStatus(wsListStatus)
			opts.Status = &status
		}

		workspaces, err := wsService.ListWorkspaces(ctx, opts)
		if err != nil {
			return fmt.Errorf("failed to list workspaces: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, workspaces)
		}

		if len(workspaces) == 0 {
			fmt.Println("No workspaces found")
			return nil
		}

		nodeLabels := make(map[string]string)
		if nodes, err := nodeService.ListNodes(ctx, nil); err == nil {
			for _, n := range nodes {
				if n != nil && n.Name != "" {
					nodeLabels[n.ID] = n.Name
				}
			}
		}

		rows := make([][]string, 0, len(workspaces))
		for _, ws := range workspaces {
			nodeLabel := nodeLabels[ws.NodeID]
			if nodeLabel == "" {
				nodeLabel = shortID(ws.NodeID)
			}

			rows = append(rows, []string{
				ws.Name,
				shortID(ws.ID),
				nodeLabel,
				truncatePath(ws.RepoPath, 40),
				formatWorkspaceStatus(ws.Status),
				fmt.Sprintf("%d", ws.AgentCount),
				ws.TmuxSession,
			})
		}

		return writeTable(os.Stdout, []string{"NAME", "ID", "NODE", "PATH", "STATUS", "AGENTS", "SESSION"}, rows)
	},
}

var wsStatusCmd = &cobra.Command{
	Use:   "status <id-or-name>",
	Short: "Show workspace status",
	Long:  "Display detailed status for a workspace including git info and agent states.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		idOrName := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		nodeRepo := db.NewNodeRepository(database)
		nodeService := node.NewService(nodeRepo)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		// Find workspace
		ws, err := findWorkspace(ctx, wsRepo, idOrName)
		if err != nil {
			return err
		}

		status, err := wsService.GetWorkspaceStatus(ctx, ws.ID)
		if err != nil {
			return fmt.Errorf("failed to get workspace status: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, status)
		}

		// Pretty print
		fmt.Printf("Workspace: %s (%s)\n", status.Workspace.Name, status.Workspace.ID)
		fmt.Printf("Path:      %s\n", status.Workspace.RepoPath)
		fmt.Printf("Session:   %s\n", status.Workspace.TmuxSession)
		fmt.Printf("Status:    %s\n", formatWorkspaceStatus(status.Workspace.Status))
		fmt.Printf("Beads:     %v\n", status.BeadsDetected)
		fmt.Println()

		fmt.Printf("Node Online:   %v\n", status.NodeOnline)
		fmt.Printf("Tmux Active:   %v\n", status.TmuxActive)
		fmt.Println()

		if status.GitInfo != nil {
			fmt.Printf("Git:\n")
			fmt.Printf("  Branch:   %s\n", status.GitInfo.Branch)
			if status.GitInfo.LastCommit != "" {
				fmt.Printf("  Commit:   %s\n", truncate(status.GitInfo.LastCommit, 12))
			}
			if status.GitInfo.RemoteURL != "" {
				fmt.Printf("  Remote:   %s\n", status.GitInfo.RemoteURL)
			}
			fmt.Println()
		}

		fmt.Printf("Agents:\n")
		fmt.Printf("  Active:  %d\n", status.ActiveAgents)
		fmt.Printf("  Idle:    %d\n", status.IdleAgents)
		fmt.Printf("  Blocked: %d\n", status.BlockedAgents)
		if status.Pulse != nil && status.Pulse.Sparkline != "" {
			fmt.Printf("  Pulse:   %s (last %dm)\n", status.Pulse.Sparkline, status.Pulse.WindowMinutes)
		}

		if len(status.Alerts) > 0 {
			fmt.Println()
			fmt.Printf("Alerts:\n")
			for _, alert := range status.Alerts {
				fmt.Printf("  [%s] %s (agent: %s)\n", alert.Severity, alert.Message, alert.AgentID)
			}
		}

		return nil
	},
}

type beadsStatusReport struct {
	Workspace      *models.Workspace   `json:"workspace"`
	IssuesPath     string              `json:"issues_path"`
	Tasks          []beads.TaskSummary `json:"tasks"`
	StatusCounts   map[string]int      `json:"status_counts"`
	PriorityCounts map[int]int         `json:"priority_counts"`
	Total          int                 `json:"total"`
}

var wsBeadsStatusCmd = &cobra.Command{
	Use:   "beads-status <id-or-name>",
	Short: "Show beads issue status for a workspace",
	Long:  "Export beads issue status from the workspace .beads/issues.jsonl file.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		idOrName := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		wsRepo := db.NewWorkspaceRepository(database)
		ws, err := findWorkspace(ctx, wsRepo, idOrName)
		if err != nil {
			return err
		}

		issuesPath := beads.IssuesPath(ws.RepoPath)
		issues, err := beads.LoadIssues(issuesPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("no beads issues file found at %s", issuesPath)
			}
			return fmt.Errorf("failed to read beads issues: %w", err)
		}

		summaries := beads.Summaries(issues)
		sort.Slice(summaries, func(i, j int) bool {
			left := summaries[i]
			right := summaries[j]

			if beadsStatusRank(left.Status) != beadsStatusRank(right.Status) {
				return beadsStatusRank(left.Status) < beadsStatusRank(right.Status)
			}
			if left.Priority != right.Priority {
				return left.Priority < right.Priority
			}
			return left.ID < right.ID
		})

		statusCounts, priorityCounts := summarizeBeadsCounts(summaries)
		report := beadsStatusReport{
			Workspace:      ws,
			IssuesPath:     issuesPath,
			Tasks:          summaries,
			StatusCounts:   statusCounts,
			PriorityCounts: priorityCounts,
			Total:          len(summaries),
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, report)
		}

		fmt.Printf("Workspace: %s (%s)\n", ws.Name, ws.ID)
		fmt.Printf("Path:      %s\n", ws.RepoPath)
		fmt.Printf("Beads:     %s\n", issuesPath)
		fmt.Printf("Total:     %d\n", report.Total)
		fmt.Printf("Status:    open=%d in_progress=%d closed=%d\n",
			statusCounts["open"], statusCounts["in_progress"], statusCounts["closed"])
		fmt.Printf("Priority:  P0=%d P1=%d P2=%d P3=%d P4=%d\n",
			priorityCounts[0], priorityCounts[1], priorityCounts[2], priorityCounts[3], priorityCounts[4])
		fmt.Println()

		if len(summaries) == 0 {
			fmt.Println("No beads issues found.")
			return nil
		}

		rows := make([][]string, 0, len(summaries))
		for _, task := range summaries {
			rows = append(rows, []string{
				task.ID,
				task.Status,
				fmt.Sprintf("P%d", task.Priority),
				task.IssueType,
				task.Title,
			})
		}

		return writeTable(os.Stdout, []string{"ID", "STATUS", "PRIORITY", "TYPE", "TITLE"}, rows)
	},
}

var wsAttachCmd = &cobra.Command{
	Use:   "attach <id-or-name>",
	Short: "Attach to workspace tmux session",
	Long:  "Attach to the tmux session for a workspace.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		idOrName := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		nodeRepo := db.NewNodeRepository(database)
		nodeService := node.NewService(nodeRepo)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		// Find workspace
		ws, err := findWorkspace(ctx, wsRepo, idOrName)
		if err != nil {
			return err
		}

		step := startProgress("Preparing tmux attach")
		attachCmd, err := wsService.AttachWorkspace(ctx, ws.ID)
		if err != nil {
			step.Fail(err)
			return fmt.Errorf("failed to get attach command: %w", err)
		}
		step.Done()

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]string{
				"workspace_id": ws.ID,
				"command":      attachCmd,
			})
		}

		// Execute tmux attach
		tmuxCmd := exec.CommandContext(ctx, "sh", "-c", attachCmd)
		tmuxCmd.Stdin = os.Stdin
		tmuxCmd.Stdout = os.Stdout
		tmuxCmd.Stderr = os.Stderr

		return tmuxCmd.Run()
	},
}

var wsRemoveCmd = &cobra.Command{
	Use:     "remove <id-or-name>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a workspace",
	Long: `Remove a workspace from Swarm.

By default, this only removes the Swarm record. The tmux session is left running.
Use --destroy to also kill the tmux session.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		idOrName := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		nodeRepo := db.NewNodeRepository(database)
		nodeService := node.NewService(nodeRepo)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		// Find workspace
		ws, err := findWorkspace(ctx, wsRepo, idOrName)
		if err != nil {
			return err
		}

		// Check for active agents
		if ws.AgentCount > 0 && !wsRemoveForce {
			return fmt.Errorf("workspace has %d agents; use --force to remove anyway", ws.AgentCount)
		}

		// Confirm destructive action
		var impact string
		if wsRemoveDestroy {
			impact = "This will remove the workspace record and kill the tmux session."
		} else {
			impact = "This will remove the workspace record. The tmux session will be left running."
		}
		if ws.AgentCount > 0 {
			impact += fmt.Sprintf(" %d agent(s) will be orphaned.", ws.AgentCount)
		}
		if !ConfirmDestructiveAction("workspace", ws.Name, impact) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		}

		if wsRemoveDestroy {
			if err := wsService.DestroyWorkspace(ctx, ws.ID); err != nil {
				return fmt.Errorf("failed to destroy workspace: %w", err)
			}
		} else {
			if err := wsService.UnmanageWorkspace(ctx, ws.ID); err != nil {
				return fmt.Errorf("failed to remove workspace: %w", err)
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"removed":      true,
				"workspace_id": ws.ID,
				"name":         ws.Name,
				"destroyed":    wsRemoveDestroy,
			})
		}

		if wsRemoveDestroy {
			fmt.Printf("Workspace '%s' destroyed (tmux session killed)\n", ws.Name)
		} else {
			fmt.Printf("Workspace '%s' removed (tmux session left running)\n", ws.Name)
		}

		return nil
	},
}

var wsKillCmd = &cobra.Command{
	Use:     "kill <id-or-name>",
	Aliases: []string{"destroy"},
	Short:   "Destroy a workspace",
	Long: `Destroy a workspace by terminating agents, killing the tmux session,
and removing the Swarm record.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		idOrName := args[0]

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

		ws, err := findWorkspace(ctx, wsRepo, idOrName)
		if err != nil {
			return err
		}

		agents, err := agentRepo.ListByWorkspace(ctx, ws.ID)
		if err != nil {
			return fmt.Errorf("failed to list agents: %w", err)
		}

		if len(agents) > 0 && !wsKillForce {
			return fmt.Errorf("workspace has %d agents; use --force to kill anyway", len(agents))
		}

		// Confirm destructive action
		impact := "This will terminate all agents, kill the tmux session, and remove the workspace."
		if len(agents) > 0 {
			impact = fmt.Sprintf("This will terminate %d agent(s), kill the tmux session, and remove the workspace.", len(agents))
		}
		if !ConfirmDestructiveAction("workspace", ws.Name, impact) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		}

		for _, agentRecord := range agents {
			if err := agentService.TerminateAgent(ctx, agentRecord.ID); err != nil {
				return fmt.Errorf("failed to terminate agent %s: %w", agentRecord.ID, err)
			}
		}

		if err := wsService.DestroyWorkspace(ctx, ws.ID); err != nil {
			return fmt.Errorf("failed to destroy workspace: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"destroyed":     true,
				"workspace_id":  ws.ID,
				"name":          ws.Name,
				"agents_killed": len(agents),
			})
		}

		fmt.Printf("Workspace '%s' destroyed (killed %d agents)\n", ws.Name, len(agents))
		return nil
	},
}

var wsRefreshCmd = &cobra.Command{
	Use:   "refresh [id-or-name]",
	Short: "Refresh workspace git info",
	Long:  "Refresh git information for a workspace or all workspaces.",
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
		wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

		var workspaces []*models.Workspace

		if len(args) > 0 {
			ws, err := findWorkspace(ctx, wsRepo, args[0])
			if err != nil {
				return err
			}
			workspaces = []*models.Workspace{ws}
		} else {
			list, err := wsService.ListWorkspaces(ctx, workspace.ListWorkspacesOptions{})
			if err != nil {
				return fmt.Errorf("failed to list workspaces: %w", err)
			}
			workspaces = list
		}

		results := make([]map[string]any, 0, len(workspaces))

		for _, ws := range workspaces {
			gitInfo, err := wsService.RefreshGitInfo(ctx, ws.ID)
			r := map[string]any{
				"workspace_id": ws.ID,
				"name":         ws.Name,
			}
			if err != nil {
				r["error"] = err.Error()
			} else {
				r["git_info"] = gitInfo
			}
			results = append(results, r)

			if !IsJSONOutput() && !IsJSONLOutput() {
				if err != nil {
					fmt.Printf("%s: error - %v\n", ws.Name, err)
				} else {
					fmt.Printf("%s: %s\n", ws.Name, gitInfo.Branch)
				}
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, results)
		}

		return nil
	},
}

func beadsStatusRank(status string) int {
	switch status {
	case "open":
		return 0
	case "in_progress":
		return 1
	case "blocked":
		return 2
	case "closed":
		return 3
	default:
		return 99
	}
}

func summarizeBeadsCounts(tasks []beads.TaskSummary) (map[string]int, map[int]int) {
	statusCounts := map[string]int{}
	priorityCounts := map[int]int{}

	for _, task := range tasks {
		statusCounts[task.Status]++
		priorityCounts[task.Priority]++
	}

	return statusCounts, priorityCounts
}

func promptRepoRootSelection(roots []string) (string, error) {
	if len(roots) == 0 {
		return "", errors.New("no repository roots to select")
	}

	fmt.Fprintln(os.Stderr, "Multiple repository roots detected. Select one:")
	for i, root := range roots {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, root)
	}
	fmt.Fprintf(os.Stderr, "Select repo root [1-%d]: ", len(roots))

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	choice := strings.TrimSpace(line)
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > len(roots) {
		return "", fmt.Errorf("invalid selection %q", choice)
	}

	return roots[index-1], nil
}

// truncatePath truncates a path for display.
func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen+3:]
}

// truncate truncates a string.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
