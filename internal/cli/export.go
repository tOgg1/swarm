// Package cli provides export commands for Swarm data.
package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(exportCmd)
	exportCmd.AddCommand(exportStatusCmd)
}

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export Swarm data",
	Long:  "Export Swarm state for automation or reporting.",
}

var exportStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Export full status",
	Long:  "Export full status as JSON: nodes, workspaces, agents, queues, alerts.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		nodeRepo := db.NewNodeRepository(database)
		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		queueRepo := db.NewQueueRepository(database)

		nodes, err := nodeRepo.List(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to list nodes: %w", err)
		}

		workspaces, err := wsRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to list workspaces: %w", err)
		}

		agents, err := agentRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to list agents: %w", err)
		}

		agentsByWorkspace := make(map[string][]*models.Agent, len(workspaces))
		for _, agent := range agents {
			agentsByWorkspace[agent.WorkspaceID] = append(agentsByWorkspace[agent.WorkspaceID], agent)
		}

		var alerts []models.Alert
		for _, ws := range workspaces {
			wsAgents := agentsByWorkspace[ws.ID]
			ws.AgentCount = len(wsAgents)
			wsAlerts := workspace.BuildAlerts(wsAgents)
			if len(wsAlerts) > 0 {
				ws.Alerts = wsAlerts
				alerts = append(alerts, wsAlerts...)
			}
		}

		var queues []*models.QueueItem
		for _, agent := range agents {
			items, err := queueRepo.List(ctx, agent.ID)
			if err != nil {
				return fmt.Errorf("failed to list queue for agent %s: %w", agent.ID, err)
			}
			queues = append(queues, items...)
		}

		status := ExportStatus{
			Nodes:      nodes,
			Workspaces: workspaces,
			Agents:     agents,
			Queues:     queues,
			Alerts:     alerts,
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, status)
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintf(writer, "Nodes:\t%d\n", len(nodes))
		fmt.Fprintf(writer, "Workspaces:\t%d\n", len(workspaces))
		fmt.Fprintf(writer, "Agents:\t%d\n", len(agents))
		fmt.Fprintf(writer, "Queue items:\t%d\n", len(queues))
		fmt.Fprintf(writer, "Alerts:\t%d\n", len(alerts))
		if err := writer.Flush(); err != nil {
			return err
		}

		fmt.Println("Use --json or --jsonl for full export output.")
		return nil
	},
}

// ExportStatus is the payload returned by `swarm export status`.
type ExportStatus struct {
	Nodes      []*models.Node      `json:"nodes"`
	Workspaces []*models.Workspace `json:"workspaces"`
	Agents     []*models.Agent     `json:"agents"`
	Queues     []*models.QueueItem `json:"queues"`
	Alerts     []models.Alert      `json:"alerts"`
}
