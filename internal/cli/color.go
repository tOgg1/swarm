// Package cli provides color helpers for human output.
package cli

import "os"

const (
	colorReset   = "\x1b[0m"
	colorGreen   = "\x1b[32m"
	colorYellow  = "\x1b[33m"
	colorRed     = "\x1b[31m"
	colorCyan    = "\x1b[36m"
	colorMagenta = "\x1b[35m"
)

func colorEnabled() bool {
	if IsJSONOutput() || IsJSONLOutput() {
		return false
	}
	if noColor {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return true
}

func colorize(text, color string) string {
	if !colorEnabled() || color == "" {
		return text
	}
	return color + text + colorReset
}
