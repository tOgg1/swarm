// Package cli provides export commands for Swarm data.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(exportCmd)
	exportCmd.AddCommand(exportStatusCmd)
	exportCmd.AddCommand(exportEventsCmd)

	exportEventsCmd.Flags().StringVar(&exportEventsTypes, "type", "", "filter by event type (comma-separated)")
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

		status, err := buildExportStatus(ctx, database)
		if err != nil {
			return err
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, status)
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintf(writer, "Nodes:\t%d\n", len(status.Nodes))
		fmt.Fprintf(writer, "Workspaces:\t%d\n", len(status.Workspaces))
		fmt.Fprintf(writer, "Agents:\t%d\n", len(status.Agents))
		fmt.Fprintf(writer, "Queue items:\t%d\n", len(status.Queues))
		fmt.Fprintf(writer, "Alerts:\t%d\n", len(status.Alerts))
		if err := writer.Flush(); err != nil {
			return err
		}

		fmt.Println("Use --json or --jsonl for full export output.")
		return nil
	},
}

var (
	exportEventsTypes string
)

const exportEventsPageSize = 500

var exportEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Export events",
	Long:  "Export the event log as JSON or JSONL, optionally filtered by type.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := MustBeJSONLForWatch(); err != nil {
			return err
		}

		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		eventRepo := db.NewEventRepository(database)

		eventTypes, err := parseEventTypes(exportEventsTypes)
		if err != nil {
			return err
		}

		since, err := GetSinceTime()
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}

		if IsWatchMode() {
			return StreamEventsWithReplay(ctx, eventRepo, os.Stdout, since, eventTypes, nil, "")
		}

		if IsJSONLOutput() {
			return streamExportEvents(ctx, eventRepo, since, eventTypes)
		}

		events, err := collectExportEvents(ctx, eventRepo, since, eventTypes)
		if err != nil {
			return err
		}

		if IsJSONOutput() {
			return WriteOutput(os.Stdout, events)
		}

		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintf(writer, "Events:\t%d\n", len(events))
		if err := writer.Flush(); err != nil {
			return err
		}

		fmt.Println("Use --json or --jsonl for full export output.")
		return nil
	},
}

func streamExportEvents(ctx context.Context, repo *db.EventRepository, since *time.Time, eventTypes []models.EventType) error {
	return exportEventsPaginated(ctx, repo, since, eventTypes, func(events []*models.Event) error {
		if len(events) == 0 {
			return nil
		}
		return WriteOutput(os.Stdout, events)
	})
}

func collectExportEvents(ctx context.Context, repo *db.EventRepository, since *time.Time, eventTypes []models.EventType) ([]*models.Event, error) {
	var collected []*models.Event
	err := exportEventsPaginated(ctx, repo, since, eventTypes, func(events []*models.Event) error {
		if len(events) == 0 {
			return nil
		}
		collected = append(collected, events...)
		return nil
	})
	return collected, err
}

func exportEventsPaginated(ctx context.Context, repo *db.EventRepository, since *time.Time, eventTypes []models.EventType, handle func([]*models.Event) error) error {
	var cursor string
	for {
		query := db.EventQuery{
			Cursor: cursor,
			Since:  since,
			Limit:  exportEventsPageSize,
		}
		if len(eventTypes) == 1 {
			eventType := eventTypes[0]
			query.Type = &eventType
		}

		page, err := repo.Query(ctx, query)
		if err != nil {
			return fmt.Errorf("failed to query events: %w", err)
		}

		events := filterEventsByType(page.Events, eventTypes)
		if err := handle(events); err != nil {
			return err
		}

		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		since = nil
	}

	return nil
}

func parseEventTypes(raw string) ([]models.EventType, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	types := make([]models.EventType, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		types = append(types, models.EventType(part))
	}

	if len(types) == 0 {
		return nil, fmt.Errorf("event type filter cannot be empty")
	}

	return types, nil
}

func filterEventsByType(events []*models.Event, eventTypes []models.EventType) []*models.Event {
	if len(eventTypes) <= 1 {
		return events
	}

	allowed := make(map[models.EventType]struct{}, len(eventTypes))
	for _, t := range eventTypes {
		allowed[t] = struct{}{}
	}

	filtered := make([]*models.Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if _, ok := allowed[event.Type]; ok {
			filtered = append(filtered, event)
		}
	}

	return filtered
}

// ExportStatus is the payload returned by `swarm export status`.
type ExportStatus struct {
	Nodes      []*models.Node      `json:"nodes"`
	Workspaces []*models.Workspace `json:"workspaces"`
	Agents     []*models.Agent     `json:"agents"`
	Queues     []*models.QueueItem `json:"queues"`
	Alerts     []models.Alert      `json:"alerts"`
}

func buildExportStatus(ctx context.Context, database *db.DB) (*ExportStatus, error) {
	nodeRepo := db.NewNodeRepository(database)
	wsRepo := db.NewWorkspaceRepository(database)
	agentRepo := db.NewAgentRepository(database)
	queueRepo := db.NewQueueRepository(database)

	nodes, err := nodeRepo.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	workspaces, err := wsRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}

	agents, err := agentRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
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
			return nil, fmt.Errorf("failed to list queue for agent %s: %w", agent.ID, err)
		}
		queues = append(queues, items...)
	}

	return &ExportStatus{
		Nodes:      nodes,
		Workspaces: workspaces,
		Agents:     agents,
		Queues:     queues,
		Alerts:     alerts,
	}, nil
}
