// Package tmux provides a small wrapper for tmux command execution.
package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Executor runs tmux commands.
type Executor interface {
	Exec(ctx context.Context, cmd string) (stdout, stderr []byte, err error)
}

// LocalExecutor executes commands locally via os/exec.
type LocalExecutor struct{}

// Exec runs a command locally and returns stdout and stderr.
func (e *LocalExecutor) Exec(ctx context.Context, cmd string) (stdout, stderr []byte, err error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	var stdoutBuf, stderrBuf bytes.Buffer
	c.Stdout = &stdoutBuf
	c.Stderr = &stderrBuf
	err = c.Run()
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), err
}

// Client wraps tmux command helpers.
type Client struct {
	exec Executor
}

// NewClient creates a new tmux client.
func NewClient(exec Executor) *Client {
	return &Client{exec: exec}
}

// NewTmuxClient is an alias for NewClient for backward compatibility.
func NewTmuxClient(exec Executor) *Client {
	return NewClient(exec)
}

// NewLocalClient creates a new tmux client that executes commands locally.
func NewLocalClient() *Client {
	return &Client{exec: &LocalExecutor{}}
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

// ListPanePaths returns the unique working directories for panes in a session.
func (c *Client) ListPanePaths(ctx context.Context, session string) ([]string, error) {
	if strings.TrimSpace(session) == "" {
		return nil, fmt.Errorf("session name is required")
	}

	cmd := fmt.Sprintf("tmux list-panes -t %s -F '#{pane_id}|#{pane_current_path}'", session)
	stdout, stderr, err := c.exec.Exec(ctx, cmd)
	if err != nil {
		if isNoServerRunning(stderr) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("tmux list-panes failed: %w", err)
	}

	output := strings.TrimSpace(string(stdout))
	if output == "" {
		return []string{}, nil
	}

	seen := make(map[string]struct{})
	paths := []string{}

	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("unexpected tmux output line: %q", line)
		}

		path := strings.TrimSpace(parts[1])
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	return paths, nil
}

// HasSession checks if a session with the given name exists.
func (c *Client) HasSession(ctx context.Context, session string) (bool, error) {
	if strings.TrimSpace(session) == "" {
		return false, fmt.Errorf("session name is required")
	}

	cmd := fmt.Sprintf("tmux has-session -t %s", escapeSessionName(session))
	_, stderr, err := c.exec.Exec(ctx, cmd)
	if err != nil {
		// "no server running" or "session not found" both mean session doesn't exist
		if isNoServerRunning(stderr) || isSessionNotFound(stderr) {
			return false, nil
		}
		return false, fmt.Errorf("tmux has-session failed: %w", err)
	}

	return true, nil
}

// NewSession creates a new tmux session with the given name and working directory.
func (c *Client) NewSession(ctx context.Context, session, workDir string) error {
	if strings.TrimSpace(session) == "" {
		return fmt.Errorf("session name is required")
	}

	cmd := fmt.Sprintf("tmux new-session -d -s %s", escapeSessionName(session))
	if workDir != "" {
		cmd = fmt.Sprintf("%s -c %s", cmd, escapeArg(workDir))
	}

	_, stderr, err := c.exec.Exec(ctx, cmd)
	if err != nil {
		if isDuplicateSession(stderr) {
			return ErrSessionExists
		}
		return fmt.Errorf("tmux new-session failed: %w", err)
	}

	return nil
}

// KillSession terminates a tmux session.
func (c *Client) KillSession(ctx context.Context, session string) error {
	if strings.TrimSpace(session) == "" {
		return fmt.Errorf("session name is required")
	}

	cmd := fmt.Sprintf("tmux kill-session -t %s", escapeSessionName(session))
	_, stderr, err := c.exec.Exec(ctx, cmd)
	if err != nil {
		if isNoServerRunning(stderr) || isSessionNotFound(stderr) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("tmux kill-session failed: %w", err)
	}

	return nil
}

// Pane describes a tmux pane.
type Pane struct {
	ID         string // e.g., "%1"
	Index      int    // pane index within window
	CurrentDir string
	Active     bool
}

// ListPanes returns all panes in a session.
func (c *Client) ListPanes(ctx context.Context, session string) ([]Pane, error) {
	if strings.TrimSpace(session) == "" {
		return nil, fmt.Errorf("session name is required")
	}

	cmd := fmt.Sprintf("tmux list-panes -t %s -F '#{pane_id}|#{pane_index}|#{pane_current_path}|#{pane_active}'", escapeSessionName(session))
	stdout, stderr, err := c.exec.Exec(ctx, cmd)
	if err != nil {
		if isNoServerRunning(stderr) {
			return []Pane{}, nil
		}
		return nil, fmt.Errorf("tmux list-panes failed: %w", err)
	}

	output := strings.TrimSpace(string(stdout))
	if output == "" {
		return []Pane{}, nil
	}

	var panes []Pane
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			return nil, fmt.Errorf("unexpected tmux output line: %q", line)
		}

		index, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		panes = append(panes, Pane{
			ID:         strings.TrimSpace(parts[0]),
			Index:      index,
			CurrentDir: strings.TrimSpace(parts[2]),
			Active:     strings.TrimSpace(parts[3]) == "1",
		})
	}

	return panes, nil
}

// SplitWindow creates a new pane by splitting the current window.
// If horizontal is true, splits left-right; otherwise splits top-bottom.
// Returns the new pane ID.
func (c *Client) SplitWindow(ctx context.Context, target string, horizontal bool, workDir string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("target is required")
	}

	splitFlag := "-v" // vertical split (top-bottom)
	if horizontal {
		splitFlag = "-h" // horizontal split (left-right)
	}

	// Use -P -F to print the new pane ID
	cmd := fmt.Sprintf("tmux split-window %s -t %s -P -F '#{pane_id}'", splitFlag, escapeArg(target))
	if workDir != "" {
		cmd = fmt.Sprintf("%s -c %s", cmd, escapeArg(workDir))
	}

	stdout, _, err := c.exec.Exec(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("tmux split-window failed: %w", err)
	}

	return strings.TrimSpace(string(stdout)), nil
}

// SendKeys sends keys to a tmux pane.
// If literal is true, sends keys literally (no translation).
// If enter is true, appends an Enter keypress.
func (c *Client) SendKeys(ctx context.Context, target, keys string, literal, enter bool) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("target is required")
	}

	literalFlag := ""
	if literal {
		literalFlag = "-l"
	}

	// Escape the keys for shell
	escapedKeys := escapeArg(keys)
	cmd := fmt.Sprintf("tmux send-keys -t %s %s %s", escapeArg(target), literalFlag, escapedKeys)

	if _, _, err := c.exec.Exec(ctx, cmd); err != nil {
		return fmt.Errorf("tmux send-keys failed: %w", err)
	}

	if enter {
		enterCmd := fmt.Sprintf("tmux send-keys -t %s Enter", escapeArg(target))
		if _, _, err := c.exec.Exec(ctx, enterCmd); err != nil {
			return fmt.Errorf("tmux send-keys Enter failed: %w", err)
		}
	}

	return nil
}

// SendInterrupt sends Ctrl+C to a tmux pane.
func (c *Client) SendInterrupt(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("target is required")
	}

	cmd := fmt.Sprintf("tmux send-keys -t %s C-c", escapeArg(target))
	if _, _, err := c.exec.Exec(ctx, cmd); err != nil {
		return fmt.Errorf("tmux send-keys C-c failed: %w", err)
	}

	return nil
}

// SendAndWait sends keys to a tmux pane and waits for output to stabilize.
// It polls capture-pane output until the hash is unchanged for stableRounds.
func (c *Client) SendAndWait(ctx context.Context, target, keys string, literal, enter bool, stableRounds int) (string, error) {
	if stableRounds <= 0 {
		stableRounds = 2
	}

	if err := c.SendKeys(ctx, target, keys, literal, enter); err != nil {
		return "", err
	}

	var lastHash string
	stable := 0

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		content, err := c.CapturePane(ctx, target, false)
		if err != nil {
			return "", err
		}

		hash := HashSnapshot(content)
		if hash == lastHash {
			stable++
			if stable >= stableRounds {
				return content, nil
			}
		} else {
			stable = 0
			lastHash = hash
		}
	}
}

// CapturePane captures the visible content of a pane.
// If history is true, includes scrollback history.
func (c *Client) CapturePane(ctx context.Context, target string, history bool) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("target is required")
	}

	cmd := fmt.Sprintf("tmux capture-pane -t %s -p", escapeArg(target))
	if history {
		cmd = fmt.Sprintf("%s -S -", cmd) // Start from beginning of history
	}

	stdout, _, err := c.exec.Exec(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane failed: %w", err)
	}

	return string(stdout), nil
}

// KillPane kills a specific pane.
func (c *Client) KillPane(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("target is required")
	}

	cmd := fmt.Sprintf("tmux kill-pane -t %s", escapeArg(target))
	if _, _, err := c.exec.Exec(ctx, cmd); err != nil {
		return fmt.Errorf("tmux kill-pane failed: %w", err)
	}

	return nil
}

// SelectPane selects (focuses) a pane.
func (c *Client) SelectPane(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("target is required")
	}

	cmd := fmt.Sprintf("tmux select-pane -t %s", escapeArg(target))
	if _, _, err := c.exec.Exec(ctx, cmd); err != nil {
		return fmt.Errorf("tmux select-pane failed: %w", err)
	}

	return nil
}

// Common errors
var (
	ErrSessionExists   = fmt.Errorf("session already exists")
	ErrSessionNotFound = fmt.Errorf("session not found")
)

func isNoServerRunning(stderr []byte) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "no server running")
}

func isSessionNotFound(stderr []byte) bool {
	s := strings.ToLower(string(stderr))
	return strings.Contains(s, "session not found") ||
		strings.Contains(s, "can't find session")
}

func isDuplicateSession(stderr []byte) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "duplicate session")
}

// escapeSessionName escapes a session name for use in tmux commands.
func escapeSessionName(name string) string {
	// For session names, we just need to quote if there are special chars
	if strings.ContainsAny(name, " \t\n'\"\\$`!") {
		return fmt.Sprintf("'%s'", strings.ReplaceAll(name, "'", "'\\''"))
	}
	return name
}

// escapeArg escapes an argument for shell use.
func escapeArg(arg string) string {
	// Use single quotes and escape any internal single quotes
	return fmt.Sprintf("'%s'", strings.ReplaceAll(arg, "'", "'\\''"))
}
