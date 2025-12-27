// Package sequences provides loading and rendering of multi-step message sequences.
package sequences

// Sequence represents an ordered list of queue operations.
type Sequence struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Steps       []SequenceStep `yaml:"steps"`
	Variables   []SequenceVar  `yaml:"variables,omitempty"`
	Tags        []string       `yaml:"tags,omitempty"`
	Source      string         // file path or "builtin"
}

// SequenceStep represents a single operation in a sequence.
type SequenceStep struct {
	Type       StepType `yaml:"type"`
	Content    string   `yaml:"content,omitempty"`
	Message    string   `yaml:"message,omitempty"`
	Duration   string   `yaml:"duration,omitempty"`
	When       string   `yaml:"when,omitempty"`
	Reason     string   `yaml:"reason,omitempty"`
	Expression string   `yaml:"expression,omitempty"`
}

// SequenceVar describes a variable used in a sequence.
type SequenceVar struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Default     string `yaml:"default,omitempty"`
	Required    bool   `yaml:"required"`
}

// StepType defines the kind of sequence step.
type StepType string

const (
	StepTypeMessage     StepType = "message"
	StepTypePause       StepType = "pause"
	StepTypeConditional StepType = "conditional"
)
