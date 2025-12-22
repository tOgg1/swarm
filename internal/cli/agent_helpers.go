// Package cli provides helper constructors for services.
package cli

import (
	"path/filepath"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/db"
)

func agentServiceOptions(database *db.DB) []agent.ServiceOption {
	opts := []agent.ServiceOption{}

	if database != nil {
		opts = append(opts, agent.WithEventRepository(db.NewEventRepository(database)))
	}

	if cfg := GetConfig(); cfg != nil {
		archiveDir := filepath.Join(cfg.Global.DataDir, "archives", "agents")
		opts = append(opts, agent.WithArchiveDir(archiveDir))
	}

	return opts
}
