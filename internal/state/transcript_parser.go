// Package state provides agent state management and change notifications.
package state

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

// ParseTranscript inspects transcript text and returns a detected state if a pattern matches.
func ParseTranscript(text string) *models.StateInfo {
	lower := strings.ToLower(text)

	if containsAny(lower, "error", "exception", "panic", "failed") {
		return &models.StateInfo{
			State:      models.AgentStateError,
			Confidence: models.StateConfidenceMedium,
			Reason:     "error indicator detected in transcript",
		}
	}

	if containsAny(lower, "rate limit", "too many requests", "quota exceeded", "429") {
		info := &models.StateInfo{
			State:      models.AgentStateRateLimited,
			Confidence: models.StateConfidenceMedium,
			Reason:     "rate limit indicator detected in transcript",
		}
		if retryAfter, ok := extractRetryAfter(lower); ok {
			info.Evidence = []string{fmt.Sprintf("retry_after=%s", retryAfter)}
		}
		return info
	}

	if containsAny(lower, "approve", "confirm", "allow", "proceed?", "[y/n]", "(y/n)") {
		return &models.StateInfo{
			State:      models.AgentStateAwaitingApproval,
			Confidence: models.StateConfidenceLow,
			Reason:     "approval prompt detected in transcript",
		}
	}

	return nil
}

var retryAfterPattern = regexp.MustCompile(`(?i)(retry after|try again in)\s+(\d+)\s*([a-z]+)?`)

func extractRetryAfter(text string) (time.Duration, bool) {
	match := retryAfterPattern.FindStringSubmatch(text)
	if len(match) < 3 {
		return 0, false
	}

	value, err := strconv.Atoi(match[2])
	if err != nil {
		return 0, false
	}

	unit := normalizeDurationUnit(match[3])
	if unit == "" {
		unit = "s"
	}

	duration, err := time.ParseDuration(fmt.Sprintf("%d%s", value, unit))
	if err != nil {
		return 0, false
	}

	return duration, true
}

func normalizeDurationUnit(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "s", "sec", "secs", "second", "seconds":
		return "s"
	case "m", "min", "mins", "minute", "minutes":
		return "m"
	case "h", "hr", "hrs", "hour", "hours":
		return "h"
	default:
		return ""
	}
}
