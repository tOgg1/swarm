// Package cli provides helpers for interactive mode detection.
package cli

import "os"

// IsNonInteractive reports whether prompts should be skipped and defaults used.
func IsNonInteractive() bool {
	if nonInteractive {
		return true
	}
	if _, ok := os.LookupEnv("SWARM_NON_INTERACTIVE"); ok {
		return true
	}
	return !hasTTY()
}

// IsInteractive reports whether the session can prompt for user input.
func IsInteractive() bool {
	return !IsNonInteractive()
}
