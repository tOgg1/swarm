package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
)

func TestArchiveAgentLogs_CompressesOldArchives(t *testing.T) {
	dir := t.TempDir()
	service := &Service{
		archiveDir:   dir,
		archiveAfter: 0,
		logger:       logging.Component("test"),
	}
	agent := &models.Agent{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		Type:        models.AgentTypeOpenCode,
	}

	service.archiveAgentLogs(context.Background(), agent, "hello", time.Now().UTC(), nil)
	service.archiveAgentLogs(context.Background(), agent, "second", time.Now().UTC(), nil)

	agentDir := filepath.Join(dir, agent.ID)
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatalf("failed to read archive dir: %v", err)
	}

	jsonCount := 0
	gzipCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(entry.Name()) {
		case ".json":
			jsonCount++
		case ".gz":
			gzipCount++
		}
	}

	if jsonCount != 1 {
		t.Fatalf("expected 1 json archive, got %d", jsonCount)
	}
	if gzipCount != 1 {
		t.Fatalf("expected 1 gz archive, got %d", gzipCount)
	}
}
