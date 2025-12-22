// Package main is the entry point for the swarm CLI/TUI application.
// Swarm is a control plane for running and supervising AI coding agents
// across multiple repositories and servers.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/opencode-ai/swarm/internal/cli"
)

// Version information (set by goreleaser)
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cli.Execute(version, commit, date); err != nil {
		var exitErr *cli.ExitError
		if errors.As(err, &exitErr) {
			if !exitErr.Printed {
				fmt.Fprintf(os.Stderr, "Error: %v\n", exitErr.Err)
			}
			os.Exit(exitErr.Code)
		}

		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
