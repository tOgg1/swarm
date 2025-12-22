package tmux

import (
	"context"
	"errors"
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
