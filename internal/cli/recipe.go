// Package cli provides recipe management commands for mass agent spawning.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/recipes"
	"github.com/opencode-ai/swarm/internal/templates"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	recipeRunWorkspace string
	recipeRunDryRun    bool
)

func init() {
	rootCmd.AddCommand(recipeCmd)
	recipeCmd.AddCommand(recipeListCmd)
	recipeCmd.AddCommand(recipeShowCmd)
	recipeCmd.AddCommand(recipeRunCmd)

	recipeRunCmd.Flags().StringVarP(&recipeRunWorkspace, "workspace", "w", "", "workspace to spawn agents in")
	recipeRunCmd.Flags().BoolVar(&recipeRunDryRun, "dry-run", false, "show what would be spawned without doing it")
}

var recipeCmd = &cobra.Command{
	Use:   "recipe",
	Short: "Manage recipes for mass agent spawning",
	Long: `Manage recipes for spawning multiple agents at once.

Recipes define agent configurations for batch spawning with optional
initial tasking via templates or sequences.

Recipes are stored as YAML files in:
- Project: .swarm/recipes/
- User: ~/.config/swarm/recipes/
- Builtin: bundled with swarm`,
}

var recipeListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available recipes",
	Long:    `List all available recipes from project, user, and builtin sources.`,
	Example: `  swarm recipe list
  swarm recipe list --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = ""
		}

		allRecipes, err := recipes.LoadRecipesFromSearchPaths(cwd)
		if err != nil {
			return fmt.Errorf("failed to load recipes: %w", err)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, allRecipes)
		}

		if len(allRecipes) == 0 {
			fmt.Println("No recipes found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tAGENTS\tDESCRIPTION\tSOURCE")
		for _, r := range allRecipes {
			source := r.Source
			if source == "builtin" {
				source = "builtin"
			} else if strings.Contains(source, ".swarm") {
				source = "project"
			} else {
				source = "user"
			}
			desc := truncateString(r.Description, 40)
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", r.Name, r.TotalAgents(), desc, source)
		}
		w.Flush()

		return nil
	},
}

var recipeShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show recipe details",
	Long:  `Show the full details of a recipe including agent configurations.`,
	Example: `  swarm recipe show baseline
  swarm recipe show heavy --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cwd, err := os.Getwd()
		if err != nil {
			cwd = ""
		}

		recipe, err := recipes.FindRecipe(cwd, name)
		if err != nil {
			return fmt.Errorf("recipe '%s' not found", name)
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, recipe)
		}

		fmt.Printf("Recipe: %s\n", recipe.Name)
		fmt.Printf("Source: %s\n", recipe.Source)
		if recipe.Description != "" {
			fmt.Printf("Description: %s\n", recipe.Description)
		}
		fmt.Printf("Total agents: %d\n", recipe.TotalAgents())
		fmt.Println()

		if len(recipe.Profiles) > 0 {
			fmt.Printf("Profile rotation order: %s\n", strings.Join(recipe.Profiles, ", "))
			fmt.Println()
		}

		fmt.Println("Agent specifications:")
		for i, spec := range recipe.Agents {
			fmt.Printf("  [%d] %d x %s\n", i+1, spec.Count, spec.Type)
			if spec.Profile != "" {
				fmt.Printf("      Profile: %s\n", spec.Profile)
			}
			if spec.ProfileRotation != "" {
				fmt.Printf("      Profile rotation: %s\n", spec.ProfileRotation)
			}
			if spec.InitialTemplate != "" {
				fmt.Printf("      Initial template: %s\n", spec.InitialTemplate)
			}
			if spec.InitialSequence != "" {
				fmt.Printf("      Initial sequence: %s\n", spec.InitialSequence)
			}
		}

		return nil
	},
}

var recipeRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Execute a recipe to spawn agents",
	Long: `Execute a recipe to spawn multiple agents in a workspace.

The recipe defines agent types, counts, profile assignments, and
optional initial templates/sequences to run on each agent.`,
	Example: `  swarm recipe run baseline --workspace my-project
  swarm recipe run heavy --dry-run
  swarm recipe run baseline`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		name := args[0]

		cwd, err := os.Getwd()
		if err != nil {
			cwd = ""
		}

		recipe, err := recipes.FindRecipe(cwd, name)
		if err != nil {
			return fmt.Errorf("recipe '%s' not found", name)
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

		// Resolve workspace
		var ws *models.Workspace
		if recipeRunWorkspace != "" {
			ws, err = findWorkspace(ctx, wsRepo, recipeRunWorkspace)
			if err != nil {
				return err
			}
		} else {
			// Try context
			wsCtx, err := ResolveWorkspaceContext(ctx, wsRepo, "")
			if err == nil && wsCtx != nil {
				ws, err = wsRepo.Get(ctx, wsCtx.WorkspaceID)
				if err != nil {
					return fmt.Errorf("failed to get workspace from context: %w", err)
				}
			}
		}

		if ws == nil {
			return fmt.Errorf("--workspace is required (no workspace context set)")
		}

		// Dry run - just show what would be done
		if recipeRunDryRun {
			return showDryRun(recipe, ws)
		}

		// Load templates for initial messages
		allTemplates, err := templates.LoadTemplatesFromSearchPaths(cwd)
		if err != nil {
			return fmt.Errorf("failed to load templates: %w", err)
		}
		templateMap := make(map[string]*templates.Template)
		for _, t := range allTemplates {
			templateMap[t.Name] = t
		}

		// Spawn agents
		var spawnedAgents []*models.Agent
		profileIndex := 0

		for _, spec := range recipe.Agents {
			for i := 0; i < spec.Count; i++ {
				opts := agent.SpawnOptions{
					WorkspaceID: ws.ID,
					Type:        spec.Type,
				}

				// Handle profile assignment
				if spec.Profile != "" {
					// Fixed profile
					// TODO: Look up profile ID
				} else if spec.ProfileRotation != "" && len(recipe.Profiles) > 0 {
					// Rotation
					switch spec.ProfileRotation {
					case "round-robin":
						// Use profile at current index
						_ = recipe.Profiles[profileIndex%len(recipe.Profiles)]
						profileIndex++
						// TODO: Look up profile ID
					}
				}

				step := startProgress(fmt.Sprintf("Spawning %s agent %d/%d", spec.Type, i+1, spec.Count))
				a, err := agentService.SpawnAgent(ctx, opts)
				if err != nil {
					step.Fail(err)
					return fmt.Errorf("failed to spawn agent: %w", err)
				}
				step.Done()
				spawnedAgents = append(spawnedAgents, a)

				// Send initial template if specified
				if spec.InitialTemplate != "" {
					tmpl, ok := templateMap[spec.InitialTemplate]
					if ok {
						message, err := templates.RenderTemplate(tmpl, nil)
						if err == nil {
							// Queue the message
							_ = message // TODO: enqueue initial message
						}
					}
				}
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, map[string]any{
				"recipe":    recipe.Name,
				"workspace": ws.ID,
				"agents":    spawnedAgents,
				"total":     len(spawnedAgents),
			})
		}

		fmt.Printf("\nRecipe '%s' executed successfully!\n", recipe.Name)
		fmt.Printf("Workspace: %s (%s)\n", ws.Name, shortID(ws.ID))
		fmt.Printf("Spawned: %d agents\n", len(spawnedAgents))
		fmt.Println()
		for _, a := range spawnedAgents {
			fmt.Printf("  - %s (%s)\n", shortID(a.ID), a.Type)
		}

		if ws.TmuxSession != "" {
			fmt.Printf("\nAttach with: tmux attach -t %s\n", ws.TmuxSession)
		}

		return nil
	},
}

func showDryRun(recipe *recipes.Recipe, ws *models.Workspace) error {
	if IsJSONOutput() || IsJSONLOutput() {
		return WriteOutput(os.Stdout, map[string]any{
			"dry_run":   true,
			"recipe":    recipe.Name,
			"workspace": ws.ID,
			"total":     recipe.TotalAgents(),
			"agents":    recipe.Agents,
		})
	}

	fmt.Println("=== Dry Run ===")
	fmt.Printf("Recipe: %s\n", recipe.Name)
	fmt.Printf("Workspace: %s (%s)\n", ws.Name, shortID(ws.ID))
	fmt.Printf("Total agents to spawn: %d\n", recipe.TotalAgents())
	fmt.Println()

	fmt.Println("Would spawn:")
	for i, spec := range recipe.Agents {
		fmt.Printf("  [%d] %d x %s", i+1, spec.Count, spec.Type)
		if spec.Profile != "" {
			fmt.Printf(" (profile: %s)", spec.Profile)
		}
		if spec.ProfileRotation != "" {
			fmt.Printf(" (rotation: %s)", spec.ProfileRotation)
		}
		fmt.Println()
		if spec.InitialTemplate != "" {
			fmt.Printf("      → Run template: %s\n", spec.InitialTemplate)
		}
		if spec.InitialSequence != "" {
			fmt.Printf("      → Run sequence: %s\n", spec.InitialSequence)
		}
	}

	return nil
}
