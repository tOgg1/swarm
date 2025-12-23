package cli

import (
	"os"
	"testing"
)

func TestSkipConfirmation(t *testing.T) {
	// Save original values
	origYes := yesFlag
	origNonInteractive := nonInteractive
	origJSON := jsonOutput
	origJSONL := jsonlOutput
	defer func() {
		yesFlag = origYes
		nonInteractive = origNonInteractive
		jsonOutput = origJSON
		jsonlOutput = origJSONL
	}()

	tests := []struct {
		name           string
		yesFlag        bool
		nonInteractive bool
		jsonOutput     bool
		jsonlOutput    bool
		want           bool
	}{
		{
			name:    "all defaults",
			want:    true, // no TTY in test environment
		},
		{
			name:    "yes flag set",
			yesFlag: true,
			want:    true,
		},
		{
			name:           "non-interactive set",
			nonInteractive: true,
			want:           true,
		},
		{
			name:       "json output",
			jsonOutput: true,
			want:       true,
		},
		{
			name:        "jsonl output",
			jsonlOutput: true,
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yesFlag = tt.yesFlag
			nonInteractive = tt.nonInteractive
			jsonOutput = tt.jsonOutput
			jsonlOutput = tt.jsonlOutput

			if got := SkipConfirmation(); got != tt.want {
				t.Errorf("SkipConfirmation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkipConfirmation_EnvVar(t *testing.T) {
	// Test SWARM_NON_INTERACTIVE environment variable
	origVal := os.Getenv("SWARM_NON_INTERACTIVE")
	defer os.Setenv("SWARM_NON_INTERACTIVE", origVal)

	// Save original flags
	origYes := yesFlag
	origNonInteractive := nonInteractive
	origJSON := jsonOutput
	origJSONL := jsonlOutput
	defer func() {
		yesFlag = origYes
		nonInteractive = origNonInteractive
		jsonOutput = origJSON
		jsonlOutput = origJSONL
	}()

	// Reset all flags
	yesFlag = false
	nonInteractive = false
	jsonOutput = false
	jsonlOutput = false

	// Set env var
	os.Setenv("SWARM_NON_INTERACTIVE", "1")

	if !SkipConfirmation() {
		t.Error("SkipConfirmation() should return true when SWARM_NON_INTERACTIVE is set")
	}
}

func TestGetActionVerb(t *testing.T) {
	tests := []struct {
		resourceType string
		want         string
	}{
		{"node", "remove"},
		{"workspace", "destroy"},
		{"agent", "terminate"},
		{"unknown", "delete"},
		{"", "delete"},
	}

	for _, tt := range tests {
		t.Run(tt.resourceType, func(t *testing.T) {
			if got := getActionVerb(tt.resourceType); got != tt.want {
				t.Errorf("getActionVerb(%q) = %v, want %v", tt.resourceType, got, tt.want)
			}
		})
	}
}
