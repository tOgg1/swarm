package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeExecutor struct {
	stdout  []byte
	stderr  []byte
	err     error
	lastCmd string
}

func (f *fakeExecutor) Exec(ctx context.Context, cmd string) ([]byte, []byte, error) {
	f.lastCmd = cmd
	return f.stdout, f.stderr, f.err
}

func TestListSessions(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("alpha|2\nbeta|1\n")}
	client := NewClient(exec)

	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if exec.lastCmd == "" {
		t.Fatalf("expected command to be executed")
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].Name != "alpha" || sessions[0].WindowCount != 2 {
		t.Fatalf("unexpected first session: %+v", sessions[0])
	}
}

func TestListSessions_NoServer(t *testing.T) {
	exec := &fakeExecutor{
		err:    errors.New("exit status 1"),
		stderr: []byte("no server running on /tmp/tmux-1000/default"),
	}
	client := NewClient(exec)

	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no sessions, got %d", len(sessions))
	}
}

func TestListSessions_InvalidOutput(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("bad-output")}
	client := NewClient(exec)

	_, err := client.ListSessions(context.Background())
	if err == nil {
		t.Fatalf("expected error for invalid output")
	}
}

func TestListPanePaths(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("%1|/repo/one\n%2|/repo/two\n%3|/repo/one\n")}
	client := NewClient(exec)

	paths, err := client.ListPanePaths(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("ListPanePaths failed: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 unique paths, got %d", len(paths))
	}
	if paths[0] != "/repo/one" || paths[1] != "/repo/two" {
		t.Fatalf("unexpected paths order: %#v", paths)
	}
}

func TestListPanePaths_NoServer(t *testing.T) {
	exec := &fakeExecutor{
		err:    errors.New("exit status 1"),
		stderr: []byte("no server running on /tmp/tmux-1000/default"),
	}
	client := NewClient(exec)

	paths, err := client.ListPanePaths(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("expected no paths, got %d", len(paths))
	}
}

func TestNewTmuxClient(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("alpha|1\n")}
	client := NewTmuxClient(exec)

	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Name != "alpha" {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestHasSession(t *testing.T) {
	tests := []struct {
		name    string
		stdout  []byte
		stderr  []byte
		err     error
		want    bool
		wantErr bool
	}{
		{
			name:   "session exists",
			stdout: []byte{},
			want:   true,
		},
		{
			name:   "session not found",
			stderr: []byte("session not found"),
			err:    errors.New("exit status 1"),
			want:   false,
		},
		{
			name:   "no server running",
			stderr: []byte("no server running"),
			err:    errors.New("exit status 1"),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &fakeExecutor{stdout: tt.stdout, stderr: tt.stderr, err: tt.err}
			client := NewClient(exec)

			got, err := client.HasSession(context.Background(), "test-session")
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasSession_EmptyName(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	_, err := client.HasSession(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty session name")
	}
}

func TestNewSession(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.NewSession(context.Background(), "my-session", "/home/user/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec.lastCmd == "" {
		t.Fatal("expected command to be executed")
	}
	if !containsAll(exec.lastCmd, "new-session", "-d", "-s", "my-session") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

func TestNewSession_NoWorkDir(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.NewSession(context.Background(), "my-session", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsAll(exec.lastCmd, "new-session", "-d", "-s") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

func TestNewSession_Duplicate(t *testing.T) {
	exec := &fakeExecutor{
		stderr: []byte("duplicate session: my-session"),
		err:    errors.New("exit status 1"),
	}
	client := NewClient(exec)

	err := client.NewSession(context.Background(), "my-session", "")
	if err != ErrSessionExists {
		t.Errorf("expected ErrSessionExists, got %v", err)
	}
}

func TestNewSession_EmptyName(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.NewSession(context.Background(), "", "/some/path")
	if err == nil {
		t.Fatal("expected error for empty session name")
	}
}

func TestKillSession(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.KillSession(context.Background(), "my-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsAll(exec.lastCmd, "kill-session", "-t", "my-session") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

func TestKillSession_NotFound(t *testing.T) {
	exec := &fakeExecutor{
		stderr: []byte("can't find session: my-session"),
		err:    errors.New("exit status 1"),
	}
	client := NewClient(exec)

	err := client.KillSession(context.Background(), "my-session")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestListPanes(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("%1|0|0|/home/user/project|1\n%2|0|1|/home/user/project|0\n")}
	client := NewClient(exec)

	panes, err := client.ListPanes(context.Background(), "my-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}
	if panes[0].ID != "%1" || panes[0].WindowIndex != 0 || panes[0].Index != 0 || !panes[0].Active {
		t.Errorf("unexpected first pane: %+v", panes[0])
	}
	if panes[1].ID != "%2" || panes[1].WindowIndex != 0 || panes[1].Index != 1 || panes[1].Active {
		t.Errorf("unexpected second pane: %+v", panes[1])
	}
}

func TestSplitWindow(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("%3\n")}
	client := NewClient(exec)

	paneID, err := client.SplitWindow(context.Background(), "my-session:0", true, "/home/user/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if paneID != "%3" {
		t.Errorf("expected pane ID '%%3', got %q", paneID)
	}
	if !containsAll(exec.lastCmd, "split-window", "-h", "-t", "-P", "-F") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

func TestSplitWindow_Vertical(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("%4\n")}
	client := NewClient(exec)

	_, err := client.SplitWindow(context.Background(), "my-session:0", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsAll(exec.lastCmd, "-v") {
		t.Errorf("expected -v flag for vertical split: %s", exec.lastCmd)
	}
}

func TestSendKeys(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.SendKeys(context.Background(), "%1", "echo hello", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsAll(exec.lastCmd, "send-keys", "-t", "-l") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

func TestSendKeys_WithEnter(t *testing.T) {
	callCount := 0
	client := NewClient(&multiCallExecutor{calls: &callCount})

	err := client.SendKeys(context.Background(), "%1", "echo hello", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 command calls (send-keys + Enter), got %d", callCount)
	}
}

func TestSendInterrupt(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.SendInterrupt(context.Background(), "%1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsAll(exec.lastCmd, "send-keys", "-t", "C-c") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

type multiCallExecutor struct {
	calls *int
}

func (e *multiCallExecutor) Exec(ctx context.Context, cmd string) ([]byte, []byte, error) {
	*e.calls++
	return nil, nil, nil
}

type sequenceExecutor struct {
	outputs [][]byte
	index   int
	lastCmd string
}

func (e *sequenceExecutor) Exec(ctx context.Context, cmd string) ([]byte, []byte, error) {
	e.lastCmd = cmd
	if e.index >= len(e.outputs) {
		return nil, nil, nil
	}
	out := e.outputs[e.index]
	e.index++
	return out, nil, nil
}

func TestSendAndWait(t *testing.T) {
	exec := &sequenceExecutor{
		outputs: [][]byte{
			[]byte("busy\n"),
			[]byte("still busy\n"),
			[]byte("done\n"),
			[]byte("done\n"),
			[]byte("done\n"),
		},
	}
	client := NewClient(exec)

	content, err := client.SendAndWait(context.Background(), "%1", "echo hello", true, true, 2)
	if err != nil {
		t.Fatalf("SendAndWait failed: %v", err)
	}
	if content != "done\n" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestCapturePane(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("line 1\nline 2\nline 3\n")}
	client := NewClient(exec)

	content, err := client.CapturePane(context.Background(), "%1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "line 1\nline 2\nline 3\n" {
		t.Errorf("unexpected content: %q", content)
	}
	if containsAll(exec.lastCmd, "-S -") {
		t.Errorf("should not have history flag: %s", exec.lastCmd)
	}
}

func TestCapturePane_WithHistory(t *testing.T) {
	exec := &fakeExecutor{stdout: []byte("history content")}
	client := NewClient(exec)

	_, err := client.CapturePane(context.Background(), "%1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsAll(exec.lastCmd, "-S -") {
		t.Errorf("expected history flag -S -: %s", exec.lastCmd)
	}
}

func TestKillPane(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.KillPane(context.Background(), "%1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsAll(exec.lastCmd, "kill-pane", "-t") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

func TestSelectPane(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)

	err := client.SelectPane(context.Background(), "%1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsAll(exec.lastCmd, "select-pane", "-t") {
		t.Errorf("unexpected command: %s", exec.lastCmd)
	}
}

func TestEscapeArg(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"with'quote", "'with'\\''quote'"},
		{"$variable", "'$variable'"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeArg(tt.input)
			if got != tt.want {
				t.Errorf("escapeArg(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Helper function for tests
func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
