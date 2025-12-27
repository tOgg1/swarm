package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memorySink struct {
	mu     sync.Mutex
	events []RunnerEvent
}

func (s *memorySink) Emit(ctx context.Context, event RunnerEvent) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *memorySink) Close() error {
	return nil
}

func (s *memorySink) HasType(eventType string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func (s *memorySink) ContainsOutput(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.Type != EventTypeOutputLine {
			continue
		}
		switch data := event.Data.(type) {
		case OutputLineData:
			if strings.Contains(data.Line, text) {
				return true
			}
		case map[string]any:
			if line, ok := data["line"].(string); ok && strings.Contains(line, text) {
				return true
			}
		}
	}
	return false
}

func TestRunnerEmitsEvents(t *testing.T) {
	scriptPath := writeFakeAgent(t)
	sink := &memorySink{}

	runner := &Runner{
		WorkspaceID:       "ws_123",
		AgentID:           "agent_456",
		Command:           []string{scriptPath},
		PromptRegex:       regexp.MustCompile(`ready>\s*$`),
		HeartbeatInterval: 25 * time.Millisecond,
		TailLines:         4,
		EventSink:         sink,
		OutputWriter:      io.Discard,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		return sink.HasType(EventTypePromptReady)
	}, 2*time.Second, 10*time.Millisecond, "expected prompt_ready event")

	require.NoError(t, runner.SendInput(ctx, "hello"))

	require.Eventually(t, func() bool {
		return sink.ContainsOutput("working on: hello")
	}, 2*time.Second, 10*time.Millisecond, "expected output_line for work")

	require.Eventually(t, func() bool {
		return sink.HasType(EventTypeHeartbeat)
	}, 2*time.Second, 10*time.Millisecond, "expected heartbeat")

	require.NoError(t, runner.SendInput(ctx, "exit"))

	err := <-done
	require.NoError(t, err)
	require.True(t, sink.HasType(EventTypeInputSent), "expected input_sent")
	require.True(t, sink.HasType(EventTypeExit), "expected exit event")
}

func writeFakeAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-agent.sh")

	script := `#!/bin/sh
printf "ready> "
while IFS= read -r line; do
  if [ "$line" = "exit" ]; then
    echo "bye"
    exit 0
  fi
  echo "working on: $line"
  sleep 0.05
  printf "ready> "
done
`

	require.NoError(t, os.WriteFile(path, []byte(script), 0755))
	return path
}
