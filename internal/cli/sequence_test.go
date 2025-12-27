// Package cli provides tests for sequence CLI helpers.
package cli

import (
	"testing"

	"github.com/opencode-ai/swarm/internal/sequences"
)

func TestFilterSequences(t *testing.T) {
	items := []*sequences.Sequence{
		{Name: "a", Tags: []string{"git", "code"}},
		{Name: "b", Tags: []string{"review"}},
		{Name: "c", Tags: []string{"git"}},
		{Name: "d", Tags: nil},
	}

	tests := []struct {
		name     string
		tags     []string
		expected int
	}{
		{"no filter", nil, 4},
		{"filter git", []string{"git"}, 2},
		{"filter review", []string{"review"}, 1},
		{"filter multiple", []string{"git", "review"}, 3},
		{"filter nonexistent", []string{"nonexistent"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterSequences(items, tt.tags)
			if len(result) != tt.expected {
				t.Errorf("filterSequences() = %d items, want %d", len(result), tt.expected)
			}
		})
	}
}

func TestFindSequenceByName(t *testing.T) {
	items := []*sequences.Sequence{
		{Name: "bugfix"},
		{Name: "feature"},
		{Name: "review"},
	}

	tests := []struct {
		name    string
		search  string
		wantNil bool
	}{
		{"exact match", "bugfix", false},
		{"case insensitive", "FEATURE", false},
		{"not found", "nonexistent", true},
		{"partial match fails", "bug", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findSequenceByName(items, tt.search)
			if (result == nil) != tt.wantNil {
				t.Errorf("findSequenceByName(%q) nil = %v, want nil = %v", tt.search, result == nil, tt.wantNil)
			}
		})
	}
}

func TestParseSequenceVars(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		wantLen int
		wantErr bool
	}{
		{"single var", []string{"key=value"}, 1, false},
		{"multiple vars", []string{"k1=v1", "k2=v2"}, 2, false},
		{"comma separated", []string{"k1=v1,k2=v2"}, 2, false},
		{"empty value", []string{"key="}, 1, false},
		{"missing equals", []string{"invalid"}, 0, true},
		{"empty key", []string{"=value"}, 0, true},
		{"empty input", nil, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseSequenceVars(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSequenceVars() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(result) != tt.wantLen {
				t.Errorf("parseSequenceVars() = %d vars, want %d", len(result), tt.wantLen)
			}
		})
	}
}

func TestNormalizeSequenceName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple name", "mysequence", false},
		{"with dashes", "my-sequence", false},
		{"with underscores", "my_sequence", false},
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"with slash", "foo/bar", true},
		{"with dots", "foo..bar", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeSequenceName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("normalizeSequenceName(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestSequenceSourceLabel(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		userDir    string
		projectDir string
		want       string
	}{
		{"builtin", "builtin", "/home/user/.config/swarm/sequences", "/project/.swarm/sequences", "builtin"},
		{"user sequence", "/home/user/.config/swarm/sequences/foo.yaml", "/home/user/.config/swarm/sequences", "", "user"},
		{"project sequence", "/project/.swarm/sequences/bar.yaml", "", "/project/.swarm/sequences", "project"},
		{"other file", "/some/other/path.yaml", "/home/user/.config/swarm/sequences", "/project/.swarm/sequences", "file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sequenceSourceLabel(tt.source, tt.userDir, tt.projectDir)
			if result != tt.want {
				t.Errorf("sequenceSourceLabel() = %q, want %q", result, tt.want)
			}
		})
	}
}

func TestFormatSequenceStep(t *testing.T) {
	step := sequences.SequenceStep{
		Type:       sequences.StepTypeConditional,
		When:       "",
		Expression: "queue_length == 0",
		Content:    "Continue",
	}

	got := formatSequenceStep(step)
	want := "[conditional:expr] Continue (expr: queue_length == 0)"
	if got != want {
		t.Fatalf("formatSequenceStep() = %q, want %q", got, want)
	}

	short := formatSequenceStepShort(step)
	if short != "conditional:expr" {
		t.Fatalf("formatSequenceStepShort() = %q, want %q", short, "conditional:expr")
	}
}
