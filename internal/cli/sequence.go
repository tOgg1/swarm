// Package cli provides sequence management commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/swarm/internal/db"

	"github.com/opencode-ai/swarm/internal/queue"
	"github.com/opencode-ai/swarm/internal/sequences"
	"github.com/spf13/cobra"
)

var (
	sequenceTags  []string
	sequenceAgent string
	sequenceVars  []string
)

func init() {
	rootCmd.AddCommand(sequenceCmd)

	sequenceCmd.AddCommand(sequenceListCmd)
	sequenceCmd.AddCommand(sequenceShowCmd)
	sequenceCmd.AddCommand(sequenceAddCmd)
	sequenceCmd.AddCommand(sequenceEditCmd)
	sequenceCmd.AddCommand(sequenceRunCmd)
	sequenceCmd.AddCommand(sequenceDeleteCmd)

	sequenceListCmd.Flags().StringSliceVar(&sequenceTags, "tags", nil, "filter by tags (comma-separated or repeatable)")

	sequenceRunCmd.Flags().StringVarP(&sequenceAgent, "agent", "a", "", "agent ID or prefix")
	sequenceRunCmd.Flags().StringSliceVar(&sequenceVars, "var", nil, "sequence variable (key=value)")
}

var sequenceCmd = &cobra.Command{
	Use:     "seq",
	Aliases: []string{"sequence"},
	Short:   "Manage sequences",
	Long:    "Create, edit, and run reusable multi-step sequences.",
}

var sequenceListCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List sequences",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		wsRepo := db.NewWorkspaceRepository(database)
		projectDir := resolveTemplateProjectDir(ctx, wsRepo)
		items, err := sequences.LoadSequencesFromSearchPaths(projectDir)
		if err != nil {
			return err
		}

		filtered := filterSequences(items, sequenceTags)

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, filtered)
		}

		if len(filtered) == 0 {
			fmt.Println("No sequences found")
			return nil
		}

		userDir := userSequenceDir()
		projectSequencesDir := ""
		if projectDir != "" {
			projectSequencesDir = filepath.Join(projectDir, ".swarm", "sequences")
		}

		rows := make([][]string, 0, len(filtered))
		for _, seq := range filtered {
			rows = append(rows, []string{
				seq.Name,
				fmt.Sprintf("%d", len(seq.Steps)),
				seq.Description,
				sequenceSourceLabel(seq.Source, userDir, projectSequencesDir),
			})
		}

		return writeTable(os.Stdout, []string{"NAME", "STEPS", "DESCRIPTION", "SOURCE"}, rows)
	},
}

var sequenceShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show sequence details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		wsRepo := db.NewWorkspaceRepository(database)
		projectDir := resolveTemplateProjectDir(ctx, wsRepo)
		items, err := sequences.LoadSequencesFromSearchPaths(projectDir)
		if err != nil {
			return err
		}

		seq := findSequenceByName(items, args[0])
		if seq == nil {
			return fmt.Errorf("sequence %q not found", args[0])
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, seq)
		}

		fmt.Printf("Sequence: %s\n", seq.Name)
		fmt.Printf("Source: %s\n", seq.Source)
		if seq.Description != "" {
			fmt.Printf("Description: %s\n", seq.Description)
		}
		if len(seq.Tags) > 0 {
			fmt.Printf("Tags: %s\n", strings.Join(seq.Tags, ","))
		}

		fmt.Println("\nSteps:")
		for i, step := range seq.Steps {
			fmt.Printf("  %d. %s\n", i+1, formatSequenceStep(step))
		}

		if len(seq.Variables) == 0 {
			fmt.Println("\nVariables: (none)")
			return nil
		}

		fmt.Println("\nVariables:")
		for _, variable := range seq.Variables {
			line := fmt.Sprintf("- %s", variable.Name)
			if variable.Description != "" {
				line += ": " + variable.Description
			}
			if variable.Required {
				line += " (required)"
			}
			if variable.Default != "" {
				line += fmt.Sprintf(" [default: %s]", variable.Default)
			}
			fmt.Println(line)
		}

		return nil
	},
}

var sequenceAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new sequence",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := normalizeSequenceName(args[0])
		if err != nil {
			return err
		}

		path := sequenceFilePath(name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("sequence %q already exists at %s", name, path)
		}

		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("failed to create sequences directory: %w", err)
		}

		if err := os.WriteFile(path, []byte(sequenceSkeleton(name)), 0644); err != nil {
			return fmt.Errorf("failed to write sequence file: %w", err)
		}

		if err := openEditor(path); err != nil {
			return err
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]string{"path": path})
		}

		fmt.Printf("Sequence created: %s\n", path)
		return nil
	},
}

var sequenceEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit an existing sequence",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := normalizeSequenceName(args[0])
		if err != nil {
			return err
		}

		path := sequenceFilePath(name)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("sequence %q not found in user sequences", name)
			}
			return fmt.Errorf("failed to stat sequence file: %w", err)
		}

		if err := openEditor(path); err != nil {
			return err
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]string{"path": path})
		}

		fmt.Printf("Sequence updated: %s\n", path)
		return nil
	},
}

var sequenceRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Queue a sequence",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		wsRepo := db.NewWorkspaceRepository(database)
		agentRepo := db.NewAgentRepository(database)
		queueRepo := db.NewQueueRepository(database)
		queueService := queue.NewService(queueRepo)

		projectDir := resolveTemplateProjectDir(ctx, wsRepo)
		sequencesList, err := sequences.LoadSequencesFromSearchPaths(projectDir)
		if err != nil {
			return err
		}
		seq := findSequenceByName(sequencesList, args[0])
		if seq == nil {
			return fmt.Errorf("sequence %q not found", args[0])
		}

		vars, err := parseSequenceVars(sequenceVars)
		if err != nil {
			return err
		}

		items, err := sequences.RenderSequence(seq, vars)
		if err != nil {
			return err
		}

		agent, err := resolveTemplateAgent(ctx, agentRepo, sequenceAgent)
		if err != nil {
			return err
		}

		itemPtrs := make([]*models.QueueItem, 0, len(items))
		for i := range items {
			itemPtrs = append(itemPtrs, &items[i])
		}

		if err := queueService.Enqueue(ctx, agent.ID, itemPtrs...); err != nil {
			return fmt.Errorf("failed to enqueue sequence: %w", err)
		}

		itemIDs := make([]string, 0, len(itemPtrs))
		for _, item := range itemPtrs {
			itemIDs = append(itemIDs, item.ID)
		}

		result := sequenceRunResult{
			Sequence: seq.Name,
			AgentID:  agent.ID,
			ItemIDs:  itemIDs,
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, result)
		}

		fmt.Printf("Queued sequence %q (%d steps) for agent %s\n", seq.Name, len(itemPtrs), shortID(agent.ID))
		for i, step := range seq.Steps {
			fmt.Printf("  Step %d: %s -> queued\n", i+1, formatSequenceStepShort(step))
		}

		return nil
	},
}

var sequenceDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a user sequence",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := normalizeSequenceName(args[0])
		if err != nil {
			return err
		}

		path := sequenceFilePath(name)
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("sequence %q not found in user sequences", name)
			}
			return fmt.Errorf("failed to delete sequence: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]string{"deleted": name})
		}

		fmt.Printf("Deleted sequence %q\n", name)
		return nil
	},
}

type sequenceRunResult struct {
	Sequence string   `json:"sequence"`
	AgentID  string   `json:"agent_id"`
	ItemIDs  []string `json:"item_ids"`
}

func parseSequenceVars(values []string) (map[string]string, error) {
	vars := make(map[string]string)
	for _, entry := range values {
		for _, part := range splitCommaList(entry) {
			if part == "" {
				continue
			}
			key, value, ok := strings.Cut(part, "=")
			if !ok {
				return nil, fmt.Errorf("invalid variable %q (expected key=value)", part)
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return nil, fmt.Errorf("invalid variable %q (empty key)", part)
			}
			vars[key] = value
		}
	}
	return vars, nil
}

func normalizeSequenceName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("sequence name is required")
	}
	if strings.Contains(trimmed, string(filepath.Separator)) || strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("invalid sequence name %q", trimmed)
	}
	return trimmed, nil
}

func sequenceFilePath(name string) string {
	return filepath.Join(userSequenceDir(), name+".yaml")
}

func userSequenceDir() string {
	return filepath.Join(getConfigDir(), "sequences")
}

func sequenceSkeleton(name string) string {
	return fmt.Sprintf("name: %s\ndescription: Describe this sequence\nsteps:\n  - type: message\n    content: Describe the workflow here.\n", name)
}

func formatSequenceStep(step sequences.SequenceStep) string {
	switch step.Type {
	case sequences.StepTypeMessage:
		return fmt.Sprintf("[message] %s", step.Content)
	case sequences.StepTypePause:
		if step.Reason != "" {
			return fmt.Sprintf("[pause %s] %s", step.Duration, step.Reason)
		}
		return fmt.Sprintf("[pause %s]", step.Duration)
	case sequences.StepTypeConditional:
		when := strings.TrimSpace(step.When)
		if when == "" && strings.TrimSpace(step.Expression) != "" {
			when = "expr"
		}
		if when == "" {
			when = "custom"
		}
		if step.Expression != "" {
			return fmt.Sprintf("[conditional:%s] %s (expr: %s)", when, step.Content, step.Expression)
		}
		return fmt.Sprintf("[conditional:%s] %s", when, step.Content)
	default:
		return fmt.Sprintf("[%s]", step.Type)
	}
}

func formatSequenceStepShort(step sequences.SequenceStep) string {
	switch step.Type {
	case sequences.StepTypeMessage:
		return "message"
	case sequences.StepTypePause:
		if step.Duration != "" {
			return fmt.Sprintf("pause %s", step.Duration)
		}
		return "pause"
	case sequences.StepTypeConditional:
		when := strings.TrimSpace(step.When)
		if when == "" && strings.TrimSpace(step.Expression) != "" {
			when = "expr"
		}
		if when == "" {
			when = "custom"
		}
		return fmt.Sprintf("conditional:%s", when)
	default:
		return string(step.Type)
	}
}

func filterSequences(items []*sequences.Sequence, tags []string) []*sequences.Sequence {
	if len(tags) == 0 {
		return items
	}

	wanted := make(map[string]struct{})
	for _, entry := range tags {
		for _, tag := range splitCommaList(entry) {
			wanted[strings.ToLower(tag)] = struct{}{}
		}
	}

	filtered := make([]*sequences.Sequence, 0, len(items))
	for _, seq := range items {
		if seq == nil {
			continue
		}
		if len(seq.Tags) == 0 {
			continue
		}
		for _, tag := range seq.Tags {
			if _, ok := wanted[strings.ToLower(tag)]; ok {
				filtered = append(filtered, seq)
				break
			}
		}
	}

	return filtered
}

func findSequenceByName(items []*sequences.Sequence, name string) *sequences.Sequence {
	for _, seq := range items {
		if seq == nil {
			continue
		}
		if strings.EqualFold(seq.Name, name) {
			return seq
		}
	}
	return nil
}

func sequenceSourceLabel(source, userDir, projectDir string) string {
	if source == "builtin" {
		return "builtin"
	}
	if userDir != "" && isWithinDir(source, userDir) {
		return "user"
	}
	if projectDir != "" && isWithinDir(source, projectDir) {
		return "project"
	}
	return "file"
}
