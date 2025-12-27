package sequences

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/swarm/internal/models"
)

func TestLoadSequence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.yaml")

	yaml := `name: example
description: Example sequence
steps:
  - type: message
    content: "Hello {{.name}}"
  - type: pause
    duration: 5s
    reason: "wait"
  - type: conditional
    when: idle
    message: "Continue"
`

	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("write sequence: %v", err)
	}

	seq, err := LoadSequence(path)
	if err != nil {
		t.Fatalf("LoadSequence: %v", err)
	}

	if seq.Name != "example" {
		t.Fatalf("expected name example, got %q", seq.Name)
	}
	if seq.Source != path {
		t.Fatalf("expected source %q, got %q", path, seq.Source)
	}
	if got := seq.Steps[2].Content; got != "Continue" {
		t.Fatalf("expected conditional content to be Continue, got %q", got)
	}
}

func TestRenderSequence(t *testing.T) {
	seq := &Sequence{
		Name: "example",
		Steps: []SequenceStep{
			{
				Type:    StepTypeMessage,
				Content: "Hello {{.name | default \"world\"}}",
			},
			{
				Type:    StepTypeConditional,
				When:    "queue-empty",
				Content: "Queue clear for {{.name | default \"world\"}}",
			},
			{
				Type:     StepTypePause,
				Duration: "2s",
				Reason:   "wait",
			},
		},
	}

	items, err := RenderSequence(seq, map[string]string{})
	if err != nil {
		t.Fatalf("RenderSequence: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	var msgPayload models.MessagePayload
	if err := json.Unmarshal(items[0].Payload, &msgPayload); err != nil {
		t.Fatalf("unmarshal message payload: %v", err)
	}
	if msgPayload.Text != "Hello world" {
		t.Fatalf("unexpected message text: %q", msgPayload.Text)
	}

	var condPayload models.ConditionalPayload
	if err := json.Unmarshal(items[1].Payload, &condPayload); err != nil {
		t.Fatalf("unmarshal conditional payload: %v", err)
	}
	if condPayload.ConditionType != models.ConditionTypeCustomExpression {
		t.Fatalf("unexpected condition type: %s", condPayload.ConditionType)
	}
	if condPayload.Expression != "queue_length == 0" {
		t.Fatalf("unexpected condition expression: %q", condPayload.Expression)
	}
	if condPayload.Message != "Queue clear for world" {
		t.Fatalf("unexpected conditional message: %q", condPayload.Message)
	}

	var pausePayload models.PausePayload
	if err := json.Unmarshal(items[2].Payload, &pausePayload); err != nil {
		t.Fatalf("unmarshal pause payload: %v", err)
	}
	if pausePayload.DurationSeconds != 2 {
		t.Fatalf("unexpected pause duration: %d", pausePayload.DurationSeconds)
	}
}

func TestRenderSequenceRequired(t *testing.T) {
	seq := &Sequence{
		Name: "required",
		Variables: []SequenceVar{
			{Name: "who", Required: true},
		},
		Steps: []SequenceStep{
			{Type: StepTypeMessage, Content: "Hi {{.who}}"},
		},
	}

	if _, err := RenderSequence(seq, map[string]string{}); err == nil {
		t.Fatalf("expected error for missing required variable")
	}
}

func TestLoadBuiltinSequences(t *testing.T) {
	sequences, err := LoadBuiltinSequences()
	if err != nil {
		t.Fatalf("LoadBuiltinSequences: %v", err)
	}
	if len(sequences) < 3 {
		t.Fatalf("expected at least 3 builtin sequences, got %d", len(sequences))
	}

	for _, seq := range sequences {
		if seq.Source != "builtin" {
			t.Fatalf("expected builtin source, got %q", seq.Source)
		}
		if seq.Name == "" {
			t.Fatalf("builtin sequence missing name")
		}
	}
}
