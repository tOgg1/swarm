package sequences

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadSequence reads a single sequence from disk.
func LoadSequence(path string) (*Sequence, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("sequence path is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sequence %s: %w", path, err)
	}

	seq, err := parseSequence(data)
	if err != nil {
		return nil, fmt.Errorf("parse sequence %s: %w", path, err)
	}
	seq.Source = path
	return seq, nil
}

// LoadSequencesFromDir loads all sequences from a directory.
func LoadSequencesFromDir(dir string) ([]*Sequence, error) {
	if strings.TrimSpace(dir) == "" {
		return []*Sequence{}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Sequence{}, nil
		}
		return nil, fmt.Errorf("read sequences dir %s: %w", dir, err)
	}

	sequences := make([]*Sequence, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, name)
		seq, err := LoadSequence(path)
		if err != nil {
			return nil, err
		}
		sequences = append(sequences, seq)
	}

	sort.Slice(sequences, func(i, j int) bool {
		return sequences[i].Name < sequences[j].Name
	})

	return sequences, nil
}

func parseSequence(data []byte) (*Sequence, error) {
	var seq Sequence
	if err := yaml.Unmarshal(data, &seq); err != nil {
		return nil, err
	}

	seq.Name = strings.TrimSpace(seq.Name)
	if seq.Name == "" {
		return nil, fmt.Errorf("sequence name is required")
	}
	seq.Description = strings.TrimSpace(seq.Description)

	if len(seq.Steps) == 0 {
		return nil, fmt.Errorf("sequence steps are required")
	}

	seen := make(map[string]struct{})
	for i := range seq.Variables {
		name := strings.TrimSpace(seq.Variables[i].Name)
		if name == "" {
			return nil, fmt.Errorf("sequence variable name is required")
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate sequence variable %q", name)
		}
		seen[name] = struct{}{}
		seq.Variables[i].Name = name
	}

	for i := range seq.Steps {
		if err := normalizeStep(&seq.Steps[i]); err != nil {
			return nil, fmt.Errorf("sequence step %d: %w", i+1, err)
		}
	}

	return &seq, nil
}

func normalizeStep(step *SequenceStep) error {
	stepType := strings.ToLower(strings.TrimSpace(string(step.Type)))
	step.Type = StepType(stepType)

	step.Content = strings.TrimSpace(step.Content)
	step.Message = strings.TrimSpace(step.Message)
	step.Duration = strings.TrimSpace(step.Duration)
	step.When = strings.TrimSpace(step.When)
	step.Reason = strings.TrimSpace(step.Reason)
	step.Expression = strings.TrimSpace(step.Expression)

	if step.Content == "" && step.Message != "" {
		step.Content = step.Message
	}
	if step.Content != "" && step.Message != "" && step.Content != step.Message {
		return fmt.Errorf("content and message disagree")
	}

	switch step.Type {
	case StepTypeMessage:
		if step.Content == "" {
			return fmt.Errorf("message content is required")
		}

	case StepTypePause:
		if step.Duration == "" {
			return fmt.Errorf("pause duration is required")
		}
		duration, err := time.ParseDuration(step.Duration)
		if err != nil {
			return fmt.Errorf("invalid pause duration: %w", err)
		}
		if duration <= 0 {
			return fmt.Errorf("pause duration must be greater than 0")
		}

	case StepTypeConditional:
		if step.Content == "" {
			return fmt.Errorf("conditional message is required")
		}
		if step.When == "" && step.Expression != "" {
			step.When = "custom"
		}
		if step.When == "" {
			return fmt.Errorf("conditional when is required")
		}

	default:
		return fmt.Errorf("unknown step type %q", step.Type)
	}

	return nil
}
