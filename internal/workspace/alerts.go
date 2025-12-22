// Package workspace provides helpers for workspace lifecycle management.
package workspace

import "github.com/opencode-ai/swarm/internal/models"

// BuildAlerts derives alert entries from agent states.
func BuildAlerts(agents []*models.Agent) []models.Alert {
	alerts := make([]models.Alert, 0)

	for _, agent := range agents {
		switch agent.State {
		case models.AgentStateAwaitingApproval:
			alerts = append(alerts, models.Alert{
				Type:     models.AlertTypeApprovalNeeded,
				Severity: models.AlertSeverityWarning,
				Message:  "Approval needed",
				AgentID:  agent.ID,
			})
		case models.AgentStateRateLimited:
			alerts = append(alerts, models.Alert{
				Type:     models.AlertTypeRateLimit,
				Severity: models.AlertSeverityWarning,
				Message:  "Agent rate limited",
				AgentID:  agent.ID,
			})
		case models.AgentStateError:
			alerts = append(alerts, models.Alert{
				Type:     models.AlertTypeError,
				Severity: models.AlertSeverityError,
				Message:  "Agent error",
				AgentID:  agent.ID,
			})
		case models.AgentStatePaused:
			alerts = append(alerts, models.Alert{
				Type:     models.AlertTypeCooldown,
				Severity: models.AlertSeverityInfo,
				Message:  "Agent paused",
				AgentID:  agent.ID,
			})
		}
	}

	return alerts
}
