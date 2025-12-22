// Package cli provides node management CLI commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/spf13/cobra"
)

var (
	// Node list flags
	nodeStatus string

	// Node add flags
	nodeAddSSH     string
	nodeAddName    string
	nodeAddLocal   bool
	nodeAddKeyPath string
	nodeAddNoTest  bool

	// Node remove flags
	nodeRemoveForce bool

	// Node exec flags
	nodeExecTimeout int
)

func init() {
	rootCmd.AddCommand(nodeCmd)
	nodeCmd.AddCommand(nodeListCmd)
	nodeCmd.AddCommand(nodeAddCmd)
	nodeCmd.AddCommand(nodeRemoveCmd)
	nodeCmd.AddCommand(nodeBootstrapCmd)
	nodeCmd.AddCommand(nodeDoctorCmd)
	nodeCmd.AddCommand(nodeRefreshCmd)
	nodeCmd.AddCommand(nodeExecCmd)

	// List flags
	nodeListCmd.Flags().StringVar(&nodeStatus, "status", "", "filter by status (online, offline, unknown)")

	// Add flags
	nodeAddCmd.Flags().StringVar(&nodeAddSSH, "ssh", "", "SSH target (user@host:port)")
	nodeAddCmd.Flags().StringVar(&nodeAddName, "name", "", "node name (required)")
	nodeAddCmd.Flags().BoolVar(&nodeAddLocal, "local", false, "mark as local node (no SSH)")
	nodeAddCmd.Flags().StringVar(&nodeAddKeyPath, "key", "", "path to SSH private key")
	nodeAddCmd.Flags().BoolVar(&nodeAddNoTest, "no-test", false, "skip connection test")
	nodeAddCmd.MarkFlagRequired("name")

	// Remove flags
	nodeRemoveCmd.Flags().BoolVarP(&nodeRemoveForce, "force", "f", false, "force removal even with workspaces")

	// Exec flags
	nodeExecCmd.Flags().IntVar(&nodeExecTimeout, "timeout", 60, "command timeout in seconds")
}

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Manage swarm nodes",
	Long: `Manage nodes in the swarm cluster.

Nodes are machines (local or remote) where agent workspaces can run.
Remote nodes are accessed via SSH.`,
}

var nodeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List nodes",
	Long:  "List all registered nodes in the swarm.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		var status *models.NodeStatus
		if nodeStatus != "" {
			parsed := models.NodeStatus(nodeStatus)
			status = &parsed
		}

		nodes, err := service.ListNodes(ctx, status)
		if err != nil {
			return fmt.Errorf("failed to list nodes: %w", err)
		}

		if !IsJSONOutput() && !IsJSONLOutput() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tSTATUS\tLOCAL\tSSH TARGET\tAGENTS")
			for _, n := range nodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\t%d\n",
					n.ID, n.Name, n.Status, n.IsLocal, n.SSHTarget, n.AgentCount)
			}
			return w.Flush()
		}

		return WriteOutput(os.Stdout, nodes)
	},
}

var nodeAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new node",
	Long: `Register a new node in the swarm.

For remote nodes, provide the SSH target:
  swarm node add --name myserver --ssh user@host

For local nodes:
  swarm node add --name localhost --local

By default, the connection is tested before adding. Use --no-test to skip.`,
	Example: `  # Add a remote node
  swarm node add --name prod-server --ssh ubuntu@192.168.1.100

  # Add with specific port and key
  swarm node add --name staging --ssh deploy@staging.example.com:2222 --key ~/.ssh/staging_key

  # Add the local machine
  swarm node add --name localhost --local`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Validate flags
		if !nodeAddLocal && nodeAddSSH == "" {
			return errors.New("either --ssh or --local is required")
		}
		if nodeAddLocal && nodeAddSSH != "" {
			return errors.New("--ssh and --local are mutually exclusive")
		}

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		n := &models.Node{
			Name:       nodeAddName,
			IsLocal:    nodeAddLocal,
			SSHTarget:  nodeAddSSH,
			SSHKeyPath: nodeAddKeyPath,
			Status:     models.NodeStatusUnknown,
		}

		testConnection := !nodeAddNoTest && !nodeAddLocal

		if err := service.AddNode(ctx, n, testConnection); err != nil {
			if errors.Is(err, node.ErrConnectionFailed) {
				return fmt.Errorf("connection test failed: %w (use --no-test to skip)", err)
			}
			if errors.Is(err, node.ErrNodeAlreadyExists) {
				return fmt.Errorf("node with name '%s' already exists", nodeAddName)
			}
			return fmt.Errorf("failed to add node: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, n)
		}

		fmt.Printf("Node '%s' added successfully (ID: %s)\n", n.Name, n.ID)
		if testConnection {
			fmt.Println("Connection test: PASSED")
			if n.Metadata.TmuxVersion != "" {
				fmt.Printf("  tmux: %s\n", n.Metadata.TmuxVersion)
			}
			if len(n.Metadata.AvailableAdapters) > 0 {
				fmt.Printf("  adapters: %v\n", n.Metadata.AvailableAdapters)
			}
		}

		return nil
	},
}

var nodeRemoveCmd = &cobra.Command{
	Use:   "remove <name-or-id>",
	Short: "Remove a node",
	Long: `Unregister a node from the swarm.

If the node has active workspaces, you must use --force to remove it.
This does not stop agents running on the node.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		nameOrID := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		// Find node by name or ID
		n, err := service.GetNodeByName(ctx, nameOrID)
		if err != nil {
			if errors.Is(err, node.ErrNodeNotFound) {
				// Try by ID
				n, err = service.GetNode(ctx, nameOrID)
				if err != nil {
					return fmt.Errorf("node '%s' not found", nameOrID)
				}
			} else {
				return fmt.Errorf("failed to get node: %w", err)
			}
		}

		// Check for workspaces
		if n.AgentCount > 0 && !nodeRemoveForce {
			return fmt.Errorf("node has %d agents; use --force to remove anyway", n.AgentCount)
		}

		if err := service.RemoveNode(ctx, n.ID); err != nil {
			return fmt.Errorf("failed to remove node: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"removed": true,
				"node_id": n.ID,
				"name":    n.Name,
			})
		}

		fmt.Printf("Node '%s' removed\n", n.Name)
		return nil
	},
}

var nodeBootstrapCmd = &cobra.Command{
	Use:   "bootstrap <name-or-id>",
	Short: "Bootstrap a node with swarm dependencies",
	Long: `Bootstrap a node by installing required dependencies.

This command SSHs to the node and installs:
  - tmux (if not present)
  - Any missing agent CLIs (opencode, claude, etc.)

For local nodes, uses the local package manager.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		nameOrID := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		// Find node
		n, err := service.GetNodeByName(ctx, nameOrID)
		if err != nil {
			if errors.Is(err, node.ErrNodeNotFound) {
				n, err = service.GetNode(ctx, nameOrID)
				if err != nil {
					return fmt.Errorf("node '%s' not found", nameOrID)
				}
			} else {
				return fmt.Errorf("failed to get node: %w", err)
			}
		}

		// Run doctor first to see what's missing
		report, err := service.Doctor(ctx, n)
		if err != nil {
			return fmt.Errorf("failed to diagnose node: %w", err)
		}

		// Display what needs bootstrapping
		if IsJSONOutput() || IsJSONLOutput() {
			// For JSON, just return the doctor report for now
			return WriteOutput(os.Stdout, map[string]any{
				"node":        n.Name,
				"diagnosis":   report,
				"message":     "Bootstrap not yet implemented - run doctor to see requirements",
				"implemented": false,
			})
		}

		fmt.Printf("Node '%s' diagnosis:\n\n", n.Name)
		for _, check := range report.Checks {
			statusIcon := "?"
			switch check.Status {
			case node.CheckPass:
				statusIcon = "✓"
			case node.CheckWarn:
				statusIcon = "!"
			case node.CheckFail:
				statusIcon = "✗"
			case node.CheckSkip:
				statusIcon = "-"
			}

			details := check.Details
			if check.Error != "" {
				details = check.Error
			}
			if details != "" {
				fmt.Printf("  %s %s: %s\n", statusIcon, check.Name, details)
			} else {
				fmt.Printf("  %s %s\n", statusIcon, check.Name)
			}
		}

		fmt.Println("\nBootstrap actions not yet implemented.")
		fmt.Println("Please install missing dependencies manually.")

		return nil
	},
}

var nodeDoctorCmd = &cobra.Command{
	Use:   "doctor <name-or-id>",
	Short: "Run diagnostics on a node",
	Long: `Run comprehensive diagnostics on a node.

Checks include:
  - SSH connectivity (for remote nodes)
  - tmux availability and version
  - Disk space
  - CPU cores
  - Memory
  - Agent CLI availability (opencode, claude, codex, gemini)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		nameOrID := args[0]

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		// Find node
		n, err := service.GetNodeByName(ctx, nameOrID)
		if err != nil {
			if errors.Is(err, node.ErrNodeNotFound) {
				n, err = service.GetNode(ctx, nameOrID)
				if err != nil {
					return fmt.Errorf("node '%s' not found", nameOrID)
				}
			} else {
				return fmt.Errorf("failed to get node: %w", err)
			}
		}

		report, err := service.Doctor(ctx, n)
		if err != nil {
			return fmt.Errorf("failed to run diagnostics: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, report)
		}

		// Pretty print
		fmt.Printf("Node: %s (%s)\n", n.Name, n.ID)
		fmt.Printf("Checked at: %s\n\n", report.CheckedAt.Format("2006-01-02 15:04:05"))

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "CHECK\tSTATUS\tDETAILS")
		for _, check := range report.Checks {
			statusStr := string(check.Status)
			switch check.Status {
			case node.CheckPass:
				statusStr = "✓ pass"
			case node.CheckWarn:
				statusStr = "! warn"
			case node.CheckFail:
				statusStr = "✗ fail"
			case node.CheckSkip:
				statusStr = "- skip"
			}

			details := check.Details
			if check.Error != "" {
				details = check.Error
			}

			fmt.Fprintf(w, "%s\t%s\t%s\n", check.Name, statusStr, details)
		}
		w.Flush()

		fmt.Println()
		if report.Success {
			fmt.Println("Overall: HEALTHY")
		} else {
			fmt.Println("Overall: UNHEALTHY (some checks failed)")
		}

		return nil
	},
}

var nodeRefreshCmd = &cobra.Command{
	Use:   "refresh [name-or-id]",
	Short: "Refresh node status",
	Long: `Test connectivity and update node status.

If no node is specified, refreshes all nodes.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		var nodesToRefresh []*models.Node

		if len(args) > 0 {
			// Refresh specific node
			nameOrID := args[0]
			n, err := service.GetNodeByName(ctx, nameOrID)
			if err != nil {
				if errors.Is(err, node.ErrNodeNotFound) {
					n, err = service.GetNode(ctx, nameOrID)
					if err != nil {
						return fmt.Errorf("node '%s' not found", nameOrID)
					}
				} else {
					return fmt.Errorf("failed to get node: %w", err)
				}
			}
			nodesToRefresh = []*models.Node{n}
		} else {
			// Refresh all nodes
			nodes, err := service.ListNodes(ctx, nil)
			if err != nil {
				return fmt.Errorf("failed to list nodes: %w", err)
			}
			nodesToRefresh = nodes
		}

		results := make([]map[string]any, 0, len(nodesToRefresh))

		for _, n := range nodesToRefresh {
			result, err := service.RefreshNodeStatus(ctx, n.ID)
			r := map[string]any{
				"node_id": n.ID,
				"name":    n.Name,
			}
			if err != nil {
				r["error"] = err.Error()
				r["status"] = "error"
			} else {
				r["success"] = result.Success
				if result.Success {
					r["status"] = "online"
					r["latency_ms"] = result.Latency.Milliseconds()
				} else {
					r["status"] = "offline"
					r["error"] = result.Error
				}
			}
			results = append(results, r)

			if !IsJSONOutput() && !IsJSONLOutput() {
				status := "online"
				if !result.Success {
					status = "offline"
				}
				fmt.Printf("%s: %s", n.Name, status)
				if result.Success {
					fmt.Printf(" (latency: %dms)\n", result.Latency.Milliseconds())
				} else {
					fmt.Printf(" (%s)\n", result.Error)
				}
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, results)
		}

		return nil
	},
}

var nodeExecCmd = &cobra.Command{
	Use:   "exec <name-or-id> -- <command>",
	Short: "Execute command on node",
	Long: `Execute an arbitrary command on a node and return the result.

For remote nodes, the command is executed via SSH.
For local nodes, the command is executed directly.

The command must be specified after the -- separator.`,
	Example: `  # Run a simple command
  swarm node exec myserver -- uname -a

  # Check disk space
  swarm node exec prod-server -- df -h

  # Run with timeout
  swarm node exec staging --timeout 30 -- long-running-script.sh

  # Get JSON output
  swarm node exec myserver --json -- cat /etc/os-release`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Find the -- separator
		dashIdx := -1
		for i, arg := range args {
			if arg == "--" {
				dashIdx = i
				break
			}
		}

		// Handle cases where -- might be in os.Args but not in args
		// (Cobra removes it if it's a standard flag separator)
		var nameOrID string
		var cmdArgs []string

		if dashIdx == -1 {
			// No -- found in args, check if command was provided
			if len(args) < 2 {
				return errors.New("command required: use -- to separate node from command")
			}
			nameOrID = args[0]
			cmdArgs = args[1:]
		} else {
			if dashIdx == 0 {
				return errors.New("node name required before --")
			}
			if dashIdx >= len(args)-1 {
				return errors.New("command required after --")
			}
			nameOrID = args[0]
			cmdArgs = args[dashIdx+1:]
		}

		if len(cmdArgs) == 0 {
			return errors.New("command required")
		}

		// Build the command string
		remoteCmd := strings.Join(cmdArgs, " ")

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		// Find node by name or ID
		n, err := service.GetNodeByName(ctx, nameOrID)
		if err != nil {
			if errors.Is(err, node.ErrNodeNotFound) {
				n, err = service.GetNode(ctx, nameOrID)
				if err != nil {
					return fmt.Errorf("node '%s' not found", nameOrID)
				}
			} else {
				return fmt.Errorf("failed to get node: %w", err)
			}
		}

		// Apply timeout
		if nodeExecTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(nodeExecTimeout)*time.Second)
			defer cancel()
		}

		// Execute command
		result, err := service.ExecCommand(ctx, n, remoteCmd)
		if err != nil {
			return fmt.Errorf("failed to execute command: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"node_id":   n.ID,
				"node_name": n.Name,
				"command":   remoteCmd,
				"stdout":    result.Stdout,
				"stderr":    result.Stderr,
				"exit_code": result.ExitCode,
				"error":     result.Error,
			})
		}

		// Print stdout
		if result.Stdout != "" {
			fmt.Print(result.Stdout)
			if !strings.HasSuffix(result.Stdout, "\n") {
				fmt.Println()
			}
		}

		// Print stderr to stderr
		if result.Stderr != "" {
			fmt.Fprint(os.Stderr, result.Stderr)
			if !strings.HasSuffix(result.Stderr, "\n") {
				fmt.Fprintln(os.Stderr)
			}
		}

		// Return error if non-zero exit
		if result.ExitCode != 0 {
			return fmt.Errorf("command exited with code %d", result.ExitCode)
		}

		return nil
	},
}
