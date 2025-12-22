// Package cli provides CLI helpers for resolving IDs and names.
package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
)

const maxSuggestions = 5

func shortID(id string) string {
	const limit = 8
	if len(id) <= limit {
		return id
	}
	return id[:limit]
}

func findNode(ctx context.Context, service *node.Service, idOrName string) (*models.Node, error) {
	if strings.TrimSpace(idOrName) == "" {
		return nil, errors.New("node name or ID required")
	}

	n, err := service.GetNodeByName(ctx, idOrName)
	if err == nil {
		return n, nil
	}
	if !errors.Is(err, node.ErrNodeNotFound) {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	n, err = service.GetNode(ctx, idOrName)
	if err == nil {
		return n, nil
	}
	if !errors.Is(err, node.ErrNodeNotFound) {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	nodes, err := service.ListNodes(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	matches := matchNodes(nodes, idOrName)
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("node '%s' is ambiguous; matches: %s (use a longer prefix or full ID)", idOrName, formatNodeMatches(matches))
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("node '%s' not found (no nodes registered yet)", idOrName)
	}

	example := fmt.Sprintf("Example input: '%s' or '%s'", nodes[0].Name, shortID(nodes[0].ID))
	return nil, fmt.Errorf("node '%s' not found. %s", idOrName, example)
}

func findWorkspace(ctx context.Context, repo *db.WorkspaceRepository, idOrName string) (*models.Workspace, error) {
	if strings.TrimSpace(idOrName) == "" {
		return nil, errors.New("workspace name or ID required")
	}

	ws, err := repo.GetByName(ctx, idOrName)
	if err == nil {
		return ws, nil
	}
	if !errors.Is(err, db.ErrWorkspaceNotFound) {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}

	ws, err = repo.Get(ctx, idOrName)
	if err == nil {
		return ws, nil
	}
	if !errors.Is(err, db.ErrWorkspaceNotFound) {
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}

	workspaces, err := repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}

	matches := matchWorkspaces(workspaces, idOrName)
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("workspace '%s' is ambiguous; matches: %s (use a longer prefix or full ID)", idOrName, formatWorkspaceMatches(matches))
	}
	if len(workspaces) == 0 {
		return nil, fmt.Errorf("workspace '%s' not found (no workspaces registered yet)", idOrName)
	}

	example := fmt.Sprintf("Example input: '%s' or '%s'", workspaces[0].Name, shortID(workspaces[0].ID))
	return nil, fmt.Errorf("workspace '%s' not found. %s", idOrName, example)
}

func findAgent(ctx context.Context, repo *db.AgentRepository, idOrPrefix string) (*models.Agent, error) {
	if strings.TrimSpace(idOrPrefix) == "" {
		return nil, errors.New("agent ID required")
	}

	agent, err := repo.Get(ctx, idOrPrefix)
	if err == nil {
		return agent, nil
	}
	if !errors.Is(err, db.ErrAgentNotFound) {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	agents, err := repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}

	matches := matchAgents(agents, idOrPrefix)
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("agent '%s' is ambiguous; matches: %s (use a longer prefix or full ID)", idOrPrefix, formatAgentMatches(matches))
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("agent '%s' not found (no agents registered yet)", idOrPrefix)
	}

	example := fmt.Sprintf("Example input: '%s'", shortID(agents[0].ID))
	return nil, fmt.Errorf("agent '%s' not found. %s", idOrPrefix, example)
}

func matchNodes(nodes []*models.Node, query string) []*models.Node {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return nil
	}

	matches := make([]*models.Node, 0)
	seen := make(map[string]struct{})

	for _, n := range nodes {
		if n == nil {
			continue
		}
		if strings.HasPrefix(n.ID, query) {
			if _, ok := seen[n.ID]; !ok {
				matches = append(matches, n)
				seen[n.ID] = struct{}{}
			}
			continue
		}
		name := strings.ToLower(n.Name)
		if strings.HasPrefix(name, normalized) || (len(normalized) >= 3 && strings.Contains(name, normalized)) {
			if _, ok := seen[n.ID]; !ok {
				matches = append(matches, n)
				seen[n.ID] = struct{}{}
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		left := strings.ToLower(matches[i].Name)
		right := strings.ToLower(matches[j].Name)
		if left == right {
			return matches[i].ID < matches[j].ID
		}
		return left < right
	})

	return matches
}

func matchWorkspaces(workspaces []*models.Workspace, query string) []*models.Workspace {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return nil
	}

	matches := make([]*models.Workspace, 0)
	seen := make(map[string]struct{})

	for _, ws := range workspaces {
		if ws == nil {
			continue
		}
		if strings.HasPrefix(ws.ID, query) {
			if _, ok := seen[ws.ID]; !ok {
				matches = append(matches, ws)
				seen[ws.ID] = struct{}{}
			}
			continue
		}
		name := strings.ToLower(ws.Name)
		if strings.HasPrefix(name, normalized) || (len(normalized) >= 3 && strings.Contains(name, normalized)) {
			if _, ok := seen[ws.ID]; !ok {
				matches = append(matches, ws)
				seen[ws.ID] = struct{}{}
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		left := strings.ToLower(matches[i].Name)
		right := strings.ToLower(matches[j].Name)
		if left == right {
			return matches[i].ID < matches[j].ID
		}
		return left < right
	})

	return matches
}

func matchAgents(agents []*models.Agent, query string) []*models.Agent {
	normalized := strings.TrimSpace(query)
	if normalized == "" {
		return nil
	}

	matches := make([]*models.Agent, 0)
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		if strings.HasPrefix(agent.ID, normalized) {
			matches = append(matches, agent)
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ID < matches[j].ID
	})

	return matches
}

func formatNodeMatches(nodes []*models.Node) string {
	return formatMatchList(len(nodes), func(i int) string {
		node := nodes[i]
		return fmt.Sprintf("%s (%s)", node.Name, shortID(node.ID))
	})
}

func formatWorkspaceMatches(workspaces []*models.Workspace) string {
	return formatMatchList(len(workspaces), func(i int) string {
		ws := workspaces[i]
		return fmt.Sprintf("%s (%s)", ws.Name, shortID(ws.ID))
	})
}

func formatAgentMatches(agents []*models.Agent) string {
	return formatMatchList(len(agents), func(i int) string {
		agent := agents[i]
		descriptor := fmt.Sprintf("%s (%s", shortID(agent.ID), agent.Type)
		if agent.WorkspaceID != "" {
			descriptor += fmt.Sprintf(", ws %s", shortID(agent.WorkspaceID))
		}
		descriptor += ")"
		return descriptor
	})
}

func formatMatchList(count int, format func(int) string) string {
	if count == 0 {
		return "none"
	}

	limit := count
	if limit > maxSuggestions {
		limit = maxSuggestions
	}

	parts := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		parts = append(parts, format(i))
	}
	if count > maxSuggestions {
		parts = append(parts, fmt.Sprintf("... and %d more", count-maxSuggestions))
	}

	return strings.Join(parts, ", ")
}
