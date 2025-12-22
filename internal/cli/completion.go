// Package cli implements the Swarm command-line interface using Cobra.
package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:       "completion [bash|zsh|fish]",
	Short:     "Generate shell completion scripts",
	Long:      "Generate shell completion scripts for bash, zsh, or fish.",
	Args:      cobra.ExactValidArgs(1),
	ValidArgs: []string{"bash", "zsh", "fish"},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := cmd.Root()
		switch args[0] {
		case "bash":
			return root.GenBashCompletionV2(os.Stdout, true)
		case "zsh":
			return root.GenZshCompletion(os.Stdout)
		case "fish":
			return root.GenFishCompletion(os.Stdout, true)
		default:
			return fmt.Errorf("unsupported shell: %s", args[0])
		}
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
	registerDynamicCompletions()
}

func registerDynamicCompletions() {
	nodeRemoveCmd.ValidArgsFunction = completeNodeNames
	nodeBootstrapCmd.ValidArgsFunction = completeNodeNames
	nodeDoctorCmd.ValidArgsFunction = completeNodeNames
	nodeRefreshCmd.ValidArgsFunction = completeNodeNames
	nodeExecCmd.ValidArgsFunction = completeNodeNames

	wsStatusCmd.ValidArgsFunction = completeWorkspaceNames
	wsBeadsStatusCmd.ValidArgsFunction = completeWorkspaceNames
	wsAttachCmd.ValidArgsFunction = completeWorkspaceNames
	wsRemoveCmd.ValidArgsFunction = completeWorkspaceNames
	wsKillCmd.ValidArgsFunction = completeWorkspaceNames
	wsRefreshCmd.ValidArgsFunction = completeWorkspaceNames

	_ = wsCreateCmd.RegisterFlagCompletionFunc("node", completeNodeNames)
	_ = wsImportCmd.RegisterFlagCompletionFunc("node", completeNodeNames)
	_ = wsListCmd.RegisterFlagCompletionFunc("node", completeNodeNames)

	_ = agentSpawnCmd.RegisterFlagCompletionFunc("workspace", completeWorkspaceNames)
	_ = agentListCmd.RegisterFlagCompletionFunc("workspace", completeWorkspaceNames)
}

func completeNodeNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	nodes, err := listNodeCompletions()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterCompletions(nodes, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeWorkspaceNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	workspaces, err := listWorkspaceCompletions()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterCompletions(workspaces, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func listNodeCompletions() ([]string, error) {
	if appConfig == nil {
		return nil, fmt.Errorf("configuration not loaded")
	}
	dbPath := appConfig.DatabasePath()
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	database, err := openDatabase()
	if err != nil {
		return nil, err
	}
	defer database.Close()

	repo := db.NewNodeRepository(database)
	service := node.NewService(repo)

	nodes, err := service.ListNodes(context.Background(), nil)
	if err != nil {
		return nil, err
	}

	candidates := make([]string, 0, len(nodes)*2)
	seen := make(map[string]struct{})
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if strings.TrimSpace(n.Name) != "" {
			if _, ok := seen[n.Name]; !ok {
				candidates = append(candidates, n.Name)
				seen[n.Name] = struct{}{}
			}
		}
		if strings.TrimSpace(n.ID) != "" {
			if _, ok := seen[n.ID]; !ok {
				candidates = append(candidates, n.ID)
				seen[n.ID] = struct{}{}
			}
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

func listWorkspaceCompletions() ([]string, error) {
	if appConfig == nil {
		return nil, fmt.Errorf("configuration not loaded")
	}
	dbPath := appConfig.DatabasePath()
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	database, err := openDatabase()
	if err != nil {
		return nil, err
	}
	defer database.Close()

	repo := db.NewWorkspaceRepository(database)
	workspaces, err := repo.List(context.Background())
	if err != nil {
		return nil, err
	}

	candidates := make([]string, 0, len(workspaces)*2)
	seen := make(map[string]struct{})
	for _, ws := range workspaces {
		if ws == nil {
			continue
		}
		if strings.TrimSpace(ws.Name) != "" {
			if _, ok := seen[ws.Name]; !ok {
				candidates = append(candidates, ws.Name)
				seen[ws.Name] = struct{}{}
			}
		}
		if strings.TrimSpace(ws.ID) != "" {
			if _, ok := seen[ws.ID]; !ok {
				candidates = append(candidates, ws.ID)
				seen[ws.ID] = struct{}{}
			}
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

func filterCompletions(candidates []string, toComplete string) []string {
	if len(candidates) == 0 {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(toComplete))
	if normalized == "" {
		return candidates
	}
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.HasPrefix(strings.ToLower(candidate), normalized) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}
