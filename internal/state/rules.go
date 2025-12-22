// Package state provides agent state management and change notifications.
package state

import "github.com/opencode-ai/swarm/internal/models"

// ApplyRuleBasedInference adjusts detection results using transcript rules.
func ApplyRuleBasedInference(result *DetectionResult, screen string) {
	if result == nil {
		return
	}

	info := ParseTranscript(screen)
	if info == nil {
		return
	}

	result.State = info.State
	result.Confidence = info.Confidence
	result.Reason = info.Reason
}

// CombineResults merges two detection results, favoring higher confidence.
func CombineResults(primary, secondary *DetectionResult) *DetectionResult {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}

	if confidenceRank(primary.Confidence) < confidenceRank(secondary.Confidence) {
		primary, secondary = secondary, primary
	}

	combined := *primary
	if combined.Evidence == nil {
		combined.Evidence = []string{}
	}
	combined.Evidence = append(combined.Evidence, secondary.Evidence...)
	if combined.Reason != secondary.Reason && secondary.Reason != "" {
		combined.Reason = combined.Reason + "; " + secondary.Reason
	}

	return &combined
}

func confidenceRank(conf models.StateConfidence) int {
	switch conf {
	case models.StateConfidenceHigh:
		return 3
	case models.StateConfidenceMedium:
		return 2
	case models.StateConfidenceLow:
		return 1
	default:
		return 0
	}
}
