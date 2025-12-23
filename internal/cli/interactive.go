// Package cli provides helpers for interactive mode detection and confirmations.
package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

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

// SkipConfirmation reports whether confirmation prompts should be skipped.
// Returns true if --yes, --non-interactive, --json, or --jsonl is set,
// or if there's no TTY.
func SkipConfirmation() bool {
	if yesFlag {
		return true
	}
	if IsNonInteractive() {
		return true
	}
	if IsJSONOutput() || IsJSONLOutput() {
		return true
	}
	return false
}

// ConfirmAction prompts the user to confirm a destructive action.
// It displays the resource type, identifier, and impact summary.
// Returns true if the action was confirmed, false if cancelled.
// If SkipConfirmation() returns true, this returns true without prompting.
//
// Example usage:
//
//	if !ConfirmAction("node", node.Name, "This will unregister the node from the swarm.") {
//	    return nil // User cancelled
//	}
func ConfirmAction(resourceType, resourceID, impact string) bool {
	if SkipConfirmation() {
		return true
	}

	fmt.Fprintf(os.Stderr, "\nAbout to %s %s: %s\n", getActionVerb(resourceType), resourceType, resourceID)
	if impact != "" {
		fmt.Fprintf(os.Stderr, "Impact: %s\n", impact)
	}
	fmt.Fprintf(os.Stderr, "\nContinue? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// ConfirmDestructiveAction is like ConfirmAction but emphasizes the destructive nature.
// Use this for irreversible operations like delete, kill, or terminate.
func ConfirmDestructiveAction(resourceType, resourceID, impact string) bool {
	if SkipConfirmation() {
		return true
	}

	fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Destructive action\n")
	fmt.Fprintf(os.Stderr, "About to %s %s: %s\n", getActionVerb(resourceType), resourceType, resourceID)
	if impact != "" {
		fmt.Fprintf(os.Stderr, "Impact: %s\n", impact)
	}
	fmt.Fprintf(os.Stderr, "\nThis action cannot be undone. Continue? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// getActionVerb returns an appropriate verb for the resource type.
func getActionVerb(resourceType string) string {
	switch resourceType {
	case "node":
		return "remove"
	case "workspace":
		return "destroy"
	case "agent":
		return "terminate"
	default:
		return "delete"
	}
}
