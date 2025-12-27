// Package cli provides the send command for queueing messages to agents.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

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
	sendPriority  string
	sendAfter     string
	sendFront     bool
	sendWhenIdle  bool
	sendAll       bool
	sendImmediate bool
	sendSkipIdle  bool
	sendFile      string
	sendStdin     bool
	sendEditor    bool
)

func init() {
	rootCmd.AddCommand(sendCmd)

	sendCmd.Flags().StringVar(&sendPriority, "priority", "normal", "queue priority (high, normal, low)")
	sendCmd.Flags().StringVar(&sendAfter, "after", "", "insert after a specific queue item (queue-only)")
	sendCmd.Flags().BoolVar(&sendFront, "front", false, "insert at front of queue")
	sendCmd.Flags().BoolVar(&sendWhenIdle, "when-idle", false, "only dispatch when agent is idle (conditional)")
	sendCmd.Flags().BoolVar(&sendAll, "all", false, "send to all agents in workspace")
	sendCmd.Flags().BoolVar(&sendImmediate, "immediate", false, "send immediately (deprecated; bypasses queue)")
	sendCmd.Flags().BoolVar(&sendSkipIdle, "skip-idle-check", false, "send even if agent is not idle (immediate only)")
	sendCmd.Flags().StringVarP(&sendFile, "file", "f", "", "read message from file")
	sendCmd.Flags().BoolVar(&sendStdin, "stdin", false, "read message from stdin")
	sendCmd.Flags().BoolVar(&sendEditor, "editor", false, "compose message in $EDITOR")

	_ = sendCmd.Flags().MarkDeprecated("immediate", "use 'swarm inject' for immediate tmux injection")
	_ = sendCmd.Flags().MarkDeprecated("skip-idle-check", "use 'swarm inject --force' to bypass idle checks")
}

var sendCmd = &cobra.Command{
	Use:   "send [agent] <message>",
	Short: "Queue a message for an agent",
	Long: `Queue a message to be sent to an agent by the scheduler.

Messages are enqueued and dispatched when the agent is ready (idle).
This is the safe, queue-based way to send messages. For immediate
injection (dangerous), use 'swarm inject'.

If no agent is specified, uses the agent from current context.`,
	Example: `  # Queue a message for a specific agent
  swarm send abc123 "Fix the lint errors"

  # Queue using context (no agent specified)
  swarm send "Continue with the task"

  # Queue for all agents in workspace
  swarm send --all "Pause and commit your work"

  # Queue with high priority (dispatched before normal items)
  swarm send --priority high abc123 "Urgent: revert last change"

  # Queue at front (first to be dispatched)
  swarm send --front abc123 "Do this next"

  # Queue conditional message (only dispatch when idle)
  swarm send --when-idle abc123 "Continue when ready"

  # Queue from file
  swarm send abc123 --file prompt.txt

  # Queue from stdin
  cat prompt.txt | swarm send abc123 --stdin

  # Compose in $EDITOR
  swarm send abc123 --editor`,
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
		_ = wsService // for future use with --all

		queueService := queue.NewService(queueRepo)

		if sendImmediate {
			if sendWhenIdle {
				return errors.New("--when-idle cannot be used with --immediate")
			}
			if sendFront {
				return errors.New("--front cannot be used with --immediate")
			}
			if sendAfter != "" {
				return errors.New("--after cannot be used with --immediate")
			}
			if cmd.Flags().Changed("priority") {
				return errors.New("--priority cannot be used with --immediate")
			}
		} else if sendSkipIdle {
			return errors.New("--skip-idle-check can only be used with --immediate")
		}

		// Resolve agent(s) to send to
		var targetAgents []*models.Agent

		if sendAll {
			// Get workspace context
			wsCtx, err := RequireWorkspaceContext(ctx, wsRepo, "")
			if err != nil {
				return fmt.Errorf("--all requires workspace context: %w", err)
			}

			agents, err := agentRepo.ListByWorkspace(ctx, wsCtx.WorkspaceID)
			if err != nil {
				return fmt.Errorf("failed to list agents: %w", err)
			}
			if len(agents) == 0 {
				return errors.New("no agents in workspace")
			}
			targetAgents = agents
		} else {
			// Single agent - either from args or context
			var agentID string
			var messageArgs []string

			if len(args) == 0 {
				// No args - need context
				agentCtx, err := RequireAgentContext(ctx, agentRepo, "", "")
				if err != nil {
					return err
				}
				agentID = agentCtx.AgentID
				messageArgs = args
			} else {
				// First arg could be agent ID or message
				// Try to resolve as agent first
				agent, err := findAgent(ctx, agentRepo, args[0])
				if err == nil {
					agentID = agent.ID
					messageArgs = args[1:]
				} else {
					// Not a valid agent - try context
					agentCtx, err := RequireAgentContext(ctx, agentRepo, "", "")
					if err != nil {
						return fmt.Errorf("first argument '%s' is not a valid agent and no context set", args[0])
					}
					agentID = agentCtx.AgentID
					messageArgs = args // entire args is the message
				}
			}

			agent, err := agentRepo.Get(ctx, agentID)
			if err != nil {
				return fmt.Errorf("agent not found: %w", err)
			}
			targetAgents = []*models.Agent{agent}

			// Update args for message resolution
			args = messageArgs
		}

		// Resolve message
		message, err := resolveMessage(args, sendFile, sendStdin, sendEditor)
		if err != nil {
			return err
		}

		if sendImmediate {
			return sendImmediateMessages(ctx, database, targetAgents, message, sendSkipIdle)
		}

		queueOpts, err := resolveQueueOptions(cmd)
		if err != nil {
			return err
		}
		if sendAfter != "" && sendAll {
			return errors.New("--after cannot be used with --all")
		}

		results := make([]sendResult, 0, len(targetAgents))
		for _, agent := range targetAgents {
			results = append(results, enqueueMessage(ctx, queueService, queueRepo, agent, message, queueOpts))
		}

		if err := writeQueueResults(message, results, queueOpts); err != nil {
			return err
		}

		// Print next steps for successful sends
		successAgentIDs := make([]string, 0, len(results))
		for _, r := range results {
			if r.Error == "" {
				successAgentIDs = append(successAgentIDs, r.AgentID)
			}
		}
		if len(successAgentIDs) > 0 {
			PrintNextSteps(HintContext{
				Action:   "send",
				AgentIDs: successAgentIDs,
			})
		}

		return nil
	},
}

type sendResult struct {
	AgentID  string `json:"agent_id"`
	ItemID   string `json:"item_id,omitempty"`
	Position int    `json:"position,omitempty"`
	ItemType string `json:"item_type,omitempty"`
	Error    string `json:"error,omitempty"`
}

type queueOptions struct {
	Front    bool
	WhenIdle bool
	AfterID  string
}

func resolveQueueOptions(cmd *cobra.Command) (queueOptions, error) {
	priority, err := normalizePriority(sendPriority)
	if err != nil {
		return queueOptions{}, err
	}

	if sendAfter != "" && sendFront {
		return queueOptions{}, errors.New("--after cannot be used with --front")
	}

	opts := queueOptions{
		Front:    sendFront,
		WhenIdle: sendWhenIdle,
		AfterID:  sendAfter,
	}

	if !opts.Front && opts.AfterID == "" && priority == "high" {
		opts.Front = true
	}

	if cmd.Flags().Changed("priority") && priority == "low" {
		// Low priority maps to default enqueue behavior.
	}

	return opts, nil
}

func normalizePriority(priority string) (string, error) {
	priority = strings.TrimSpace(strings.ToLower(priority))
	if priority == "" {
		return "normal", nil
	}
	switch priority {
	case "high", "normal", "low":
		return priority, nil
	default:
		return "", errors.New("invalid priority (use high, normal, or low)")
	}
}

func enqueueMessage(ctx context.Context, queueService *queue.Service, queueRepo *db.QueueRepository, agent *models.Agent, message string, opts queueOptions) sendResult {
	item, err := buildQueueItem(agent.ID, message, opts.WhenIdle)
	if err != nil {
		return sendResult{AgentID: agent.ID, Error: err.Error()}
	}

	switch {
	case opts.AfterID != "":
		afterItem, err := queueRepo.Get(ctx, opts.AfterID)
		if err != nil {
			return sendResult{AgentID: agent.ID, Error: fmt.Sprintf("failed to find queue item %s: %v", opts.AfterID, err)}
		}
		if afterItem.AgentID != agent.ID {
			return sendResult{AgentID: agent.ID, Error: fmt.Sprintf("queue item %s does not belong to agent %s", opts.AfterID, agent.ID)}
		}
		err = queueService.InsertAt(ctx, agent.ID, afterItem.Position+1, item)
		if err != nil {
			return sendResult{AgentID: agent.ID, Error: err.Error()}
		}
	case opts.Front:
		if err := queueService.InsertAt(ctx, agent.ID, 0, item); err != nil {
			return sendResult{AgentID: agent.ID, Error: err.Error()}
		}
	default:
		if err := queueService.Enqueue(ctx, agent.ID, item); err != nil {
			return sendResult{AgentID: agent.ID, Error: err.Error()}
		}
	}

	position := 0
	queueItems, _ := queueService.List(ctx, agent.ID)
	for i, qi := range queueItems {
		if qi.ID == item.ID {
			position = i + 1
			break
		}
	}

	return sendResult{
		AgentID:  agent.ID,
		ItemID:   item.ID,
		Position: position,
		ItemType: string(item.Type),
	}
}

func buildQueueItem(agentID, message string, whenIdle bool) (*models.QueueItem, error) {
	if whenIdle {
		payload := models.ConditionalPayload{
			ConditionType: models.ConditionTypeWhenIdle,
			Message:       message,
		}
		payloadBytes, _ := json.Marshal(payload)
		return &models.QueueItem{
			AgentID: agentID,
			Type:    models.QueueItemTypeConditional,
			Status:  models.QueueItemStatusPending,
			Payload: payloadBytes,
		}, nil
	}

	payload := models.MessagePayload{Text: message}
	payloadBytes, _ := json.Marshal(payload)
	return &models.QueueItem{
		AgentID: agentID,
		Type:    models.QueueItemTypeMessage,
		Status:  models.QueueItemStatusPending,
		Payload: payloadBytes,
	}, nil
}

func writeQueueResults(message string, results []sendResult, opts queueOptions) error {
	if IsJSONOutput() || IsJSONLOutput() {
		return WriteOutput(os.Stdout, map[string]any{
			"queued":  true,
			"results": results,
			"message": truncateMessage(message, 100),
		})
	}

	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("✗ Failed to queue for agent %s: %s\n", shortID(r.AgentID), r.Error)
			continue
		}

		positionStr := fmt.Sprintf("#%d", r.Position)
		if opts.AfterID != "" && r.Position > 0 {
			positionStr = fmt.Sprintf("#%d (after %s)", r.Position, shortID(opts.AfterID))
		} else if opts.Front {
			positionStr = "#1 (front)"
		}
		typeStr := ""
		if r.ItemType == string(models.QueueItemTypeConditional) {
			typeStr = " (when idle)"
		}
		fmt.Printf("✓ Queued for agent %s at position %s%s\n", shortID(r.AgentID), positionStr, typeStr)
	}

	if len(results) == 1 && results[0].Error == "" {
		fmt.Printf("  \"%s\"\n", truncateMessage(message, 60))
	}

	return nil
}

func sendImmediateMessages(ctx context.Context, database *db.DB, agents []*models.Agent, message string, skipIdle bool) error {
	nodeRepo := db.NewNodeRepository(database)
	nodeService := node.NewService(nodeRepo)
	wsRepo := db.NewWorkspaceRepository(database)
	agentRepo := db.NewAgentRepository(database)
	queueRepo := db.NewQueueRepository(database)
	wsService := workspace.NewService(wsRepo, nodeService, agentRepo)

	tmuxClient := tmux.NewLocalClient()
	agentService := agent.NewService(agentRepo, queueRepo, wsService, nil, tmuxClient, agentServiceOptions(database)...)

	results := make([]sendResult, 0, len(agents))
	for _, agentInfo := range agents {
		opts := &agent.SendMessageOptions{SkipIdleCheck: skipIdle}
		if err := agentService.SendMessage(ctx, agentInfo.ID, message, opts); err != nil {
			if errors.Is(err, agent.ErrServiceAgentNotFound) {
				results = append(results, sendResult{AgentID: agentInfo.ID, Error: "agent not found"})
				continue
			}
			if errors.Is(err, agent.ErrAgentNotIdle) {
				results = append(results, sendResult{AgentID: agentInfo.ID, Error: "agent is not idle (use --skip-idle-check to force)"})
				continue
			}
			results = append(results, sendResult{AgentID: agentInfo.ID, Error: err.Error()})
			continue
		}
		results = append(results, sendResult{AgentID: agentInfo.ID})
	}

	if IsJSONOutput() || IsJSONLOutput() {
		return WriteOutput(os.Stdout, map[string]any{
			"sent":      true,
			"immediate": true,
			"results":   results,
			"message":   truncateMessage(message, 100),
		})
	}

	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("✗ Failed to send to agent %s: %s\n", shortID(r.AgentID), r.Error)
			continue
		}
		fmt.Printf("✓ Message sent to agent %s\n", shortID(r.AgentID))
	}

	if len(results) == 1 && results[0].Error == "" {
		fmt.Printf("  \"%s\"\n", truncateMessage(message, 60))
	}

	return nil
}

func truncateMessage(msg string, maxLen int) string {
	if len(msg) <= maxLen {
		return msg
	}
	return msg[:maxLen-3] + "..."
}
