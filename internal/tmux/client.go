// Package tmux provides a small wrapper for tmux command execution.
package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Executor runs tmux commands.
type Executor interface {
	Exec(ctx context.Context, cmd string) (stdout, stderr []byte, err error)
}

// Client wraps tmux command helpers.
type Client struct {
	exec Executor
}

// NewClient creates a new tmux client.
func NewClient(exec Executor) *Client {
	return &Client{exec: exec}
}

// Session describes a tmux session.
type Session struct {
	Name        string
	WindowCount int
}

// ListSessions returns all known tmux sessions.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	stdout, stderr, err := c.exec.Exec(ctx, "tmux list-sessions -F '#{session_name}|#{session_windows}'")
	if err != nil {
		if isNoServerRunning(stderr) {
			return []Session{}, nil
		}
		return nil, fmt.Errorf("tmux list-sessions failed: %w", err)
	}

	output := strings.TrimSpace(string(stdout))
	if output == "" {
		return []Session{}, nil
	}

	lines := strings.Split(output, "\n")
	sessions := make([]Session, 0, len(lines))

	for _, line := range lines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("unexpected tmux output line: %q", line)
		}

		count, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid window count in tmux output: %q", line)
		}

		sessions = append(sessions, Session{
			Name:        strings.TrimSpace(parts[0]),
			WindowCount: count,
		})
	}

	return sessions, nil
}

func isNoServerRunning(stderr []byte) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "no server running")
}
