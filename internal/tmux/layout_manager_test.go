package tmux

import (
	"context"
	"testing"
)

func TestLayoutManager_EnsureLayout(t *testing.T) {
	exec := &fakeExecutor{
		stdoutQueue: [][]byte{
			[]byte("%1|0|0|/repo|1|bash\n"),
			[]byte("%2\n"),
			[]byte("%3\n"),
			[]byte(""),
		},
	}
	client := NewClient(exec)
	manager := NewLayoutManager(client, WithLayoutPreset(LayoutPresetTiled), WithLayoutWindow("agents"))

	if err := manager.EnsureLayout(context.Background(), "session", 3, "/repo"); err != nil {
		t.Fatalf("EnsureLayout failed: %v", err)
	}
	if len(exec.commands) != 4 {
		t.Fatalf("expected 4 commands, got %d", len(exec.commands))
	}
	if !containsAll(exec.commands[0], "list-panes", "session:agents") {
		t.Fatalf("unexpected list command: %s", exec.commands[0])
	}
	if !containsAll(exec.commands[3], "select-layout", "tiled") {
		t.Fatalf("unexpected layout command: %s", exec.commands[3])
	}
}

func TestLayoutManager_Balance(t *testing.T) {
	exec := &fakeExecutor{}
	client := NewClient(exec)
	manager := NewLayoutManager(client, WithLayoutPreset(LayoutPresetEvenVertical), WithLayoutWindow("agents"))

	if err := manager.Balance(context.Background(), "session"); err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if !containsAll(exec.lastCmd, "select-layout", "session:agents", "even-vertical") {
		t.Fatalf("unexpected layout command: %s", exec.lastCmd)
	}
}

func TestLayoutManager_SplitDirectionUsesPreset(t *testing.T) {
	exec := &fakeExecutor{
		stdoutQueue: [][]byte{
			[]byte("%1|0|0|/repo|1|bash\n"),
			[]byte("%2\n"),
			[]byte(""),
		},
	}
	client := NewClient(exec)
	manager := NewLayoutManager(client, WithLayoutPreset(LayoutPresetEvenHorizontal), WithLayoutWindow("agents"))

	if err := manager.EnsureLayout(context.Background(), "session", 2, "/repo"); err != nil {
		t.Fatalf("EnsureLayout failed: %v", err)
	}
	if len(exec.commands) < 2 {
		t.Fatalf("expected split-window command")
	}
	if !containsAll(exec.commands[1], "split-window", "-h") {
		t.Fatalf("expected horizontal split for even-horizontal preset, got %s", exec.commands[1])
	}
}
