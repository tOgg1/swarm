// Package cli provides progress output helpers for long-running commands.
package cli

import (
	"fmt"
	"os"
	"time"
)

type progressStep struct {
	label   string
	started time.Time
	enabled bool
}

func startProgress(label string) *progressStep {
	if !progressEnabled() {
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s... ", label)
	return &progressStep{
		label:   label,
		started: time.Now(),
		enabled: true,
	}
}

func (p *progressStep) Done() {
	if p == nil || !p.enabled {
		return
	}
	fmt.Fprintf(os.Stderr, "done (%s)\n", formatDuration(time.Since(p.started)))
}

func (p *progressStep) Fail(err error) {
	if p == nil || !p.enabled {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed: %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "failed")
}

func progressEnabled() bool {
	if IsJSONOutput() || IsJSONLOutput() {
		return false
	}
	if noProgress {
		return false
	}
	if _, ok := os.LookupEnv("SWARM_NO_PROGRESS"); ok {
		return false
	}
	if _, ok := os.LookupEnv("NO_PROGRESS"); ok {
		return false
	}
	return true
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return d.String()
	}
	if d < time.Second {
		return d.Round(10 * time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
