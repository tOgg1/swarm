// Package agent provides agent lifecycle management for Swarm.
package agent

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
)

const (
	archiveSchemaVersion    = 1
	archiveEventPageSize    = 250
	archiveEventLimit       = 5000
	defaultArchiveAfter     = 24 * time.Hour
)

type agentArchive struct {
	Version        int                 `json:"version"`
	ArchivedAt     time.Time           `json:"archived_at"`
	Agent          *models.Agent       `json:"agent"`
	Workspace      *models.Workspace   `json:"workspace,omitempty"`
	Transcript     *archiveTranscript  `json:"transcript,omitempty"`
	Events         []*models.Event     `json:"events,omitempty"`
	EventsTruncated bool               `json:"events_truncated,omitempty"`
	Errors         *archiveErrorReport `json:"errors,omitempty"`
}

type archiveTranscript struct {
	CapturedAt time.Time `json:"captured_at,omitempty"`
	History    bool      `json:"history"`
	Content    string    `json:"content,omitempty"`
}

type archiveErrorReport struct {
	Transcript string `json:"transcript,omitempty"`
	Events     string `json:"events,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
}

func (s *Service) archiveEnabled() bool {
	if s == nil {
		return false
	}
	return strings.TrimSpace(s.archiveDir) != ""
}

func (s *Service) captureTranscript(ctx context.Context, pane string) (string, time.Time, error) {
	if s == nil || s.tmuxClient == nil {
		return "", time.Time{}, fmt.Errorf("tmux client not configured")
	}
	if strings.TrimSpace(pane) == "" {
		return "", time.Time{}, fmt.Errorf("tmux pane is required")
	}

	content, err := s.tmuxClient.CapturePane(ctx, pane, true)
	return content, time.Now().UTC(), err
}

func (s *Service) archiveAgentLogs(ctx context.Context, agent *models.Agent, transcript string, transcriptAt time.Time, transcriptErr error) {
	if !s.archiveEnabled() || agent == nil {
		return
	}
	if strings.TrimSpace(agent.ID) == "" {
		s.logger.Warn().Msg("agent archive skipped: missing agent id")
		return
	}

	archiveDir := filepath.Join(s.archiveDir, agent.ID)
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to create archive directory")
		return
	}

	var workspaceErr error
	var ws *models.Workspace
	if s.workspaceService != nil {
		ws, workspaceErr = s.workspaceService.GetWorkspace(ctx, agent.WorkspaceID)
	}

	events, eventsTruncated, eventsErr := s.collectAgentEvents(ctx, agent.ID)

	payload := &agentArchive{
		Version:        archiveSchemaVersion,
		ArchivedAt:     time.Now().UTC(),
		Agent:          agent,
		Workspace:      ws,
		Events:         events,
		EventsTruncated: eventsTruncated,
	}

	if transcript != "" || transcriptErr != nil {
		if transcriptAt.IsZero() {
			transcriptAt = time.Now().UTC()
		}
		payload.Transcript = &archiveTranscript{
			CapturedAt: transcriptAt,
			History:    true,
			Content:    transcript,
		}
	}

	if transcriptErr != nil || eventsErr != nil || workspaceErr != nil {
		payload.Errors = &archiveErrorReport{}
		if transcriptErr != nil {
			payload.Errors.Transcript = transcriptErr.Error()
		}
		if eventsErr != nil {
			payload.Errors.Events = eventsErr.Error()
		}
		if workspaceErr != nil {
			payload.Errors.Workspace = workspaceErr.Error()
		}
	}

	filename := archiveFilename(payload.ArchivedAt)
	path := filepath.Join(archiveDir, filename)
	if err := writeArchive(path, payload); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to write agent archive")
		return
	}

	if err := compressOldArchives(archiveDir, path, s.archiveAfter); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to compress old archives")
	}
}

func (s *Service) collectAgentEvents(ctx context.Context, agentID string) ([]*models.Event, bool, error) {
	if s == nil || s.eventRepo == nil || strings.TrimSpace(agentID) == "" {
		return nil, false, nil
	}

	entityType := models.EntityTypeAgent
	cursor := ""
	events := make([]*models.Event, 0)
	for {
		page, err := s.eventRepo.Query(ctx, db.EventQuery{
			EntityType: &entityType,
			EntityID:   &agentID,
			Cursor:     cursor,
			Limit:      archiveEventPageSize,
		})
		if err != nil {
			return events, false, err
		}

		events = append(events, page.Events...)
		if page.NextCursor == "" || len(events) >= archiveEventLimit {
			break
		}
		cursor = page.NextCursor
	}

	if len(events) > archiveEventLimit {
		events = events[:archiveEventLimit]
		return events, true, nil
	}

	return events, false, nil
}

func writeArchive(path string, payload *agentArchive) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("archive path is required")
	}
	if payload == nil {
		return fmt.Errorf("archive payload is required")
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func compressOldArchives(dir, keep string, minAge time.Duration) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		if path == keep {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if minAge > 0 && now.Sub(info.ModTime()) < minAge {
			continue
		}

		if err := gzipFile(path); err != nil {
			return err
		}
	}

	return nil
}

func gzipFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is required")
	}
	if strings.HasSuffix(path, ".gz") {
		return nil
	}

	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()

	gzipPath := path + ".gz"
	if _, err := os.Stat(gzipPath); err == nil {
		return nil
	}

	dest, err := os.Create(gzipPath)
	if err != nil {
		return err
	}
	defer dest.Close()

	writer := gzip.NewWriter(dest)
	if _, err := io.Copy(writer, source); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	return os.Remove(path)
}

func archiveFilename(ts time.Time) string {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return fmt.Sprintf("archive-%s-%d.json", ts.Format("20060102-150405"), ts.UnixNano())
}
