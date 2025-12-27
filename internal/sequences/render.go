package sequences

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

// RenderSequence renders a sequence into queue items with variables applied.
func RenderSequence(seq *Sequence, vars map[string]string) ([]models.QueueItem, error) {
	if seq == nil {
		return nil, fmt.Errorf("sequence is required")
	}

	data := make(map[string]string, len(vars))
	for key, value := range vars {
		data[key] = value
	}

	for _, variable := range seq.Variables {
		value := strings.TrimSpace(data[variable.Name])
		if value == "" {
			if variable.Default != "" {
				data[variable.Name] = variable.Default
				continue
			}
			if variable.Required {
				return nil, fmt.Errorf("missing required variable %q", variable.Name)
			}
		}
	}

	items := make([]models.QueueItem, 0, len(seq.Steps))
	for i, step := range seq.Steps {
		switch step.Type {
		case StepTypeMessage:
			text, err := renderText(seq.Name, step.Content, data)
			if err != nil {
				return nil, fmt.Errorf("render sequence %q step %d: %w", seq.Name, i+1, err)
			}
			payload := models.MessagePayload{Text: text}
			payloadBytes, _ := json.Marshal(payload)
			items = append(items, models.QueueItem{
				Type:    models.QueueItemTypeMessage,
				Status:  models.QueueItemStatusPending,
				Payload: payloadBytes,
			})

		case StepTypePause:
			duration, err := time.ParseDuration(step.Duration)
			if err != nil {
				return nil, fmt.Errorf("render sequence %q step %d: invalid pause duration: %w", seq.Name, i+1, err)
			}
			if duration <= 0 {
				return nil, fmt.Errorf("render sequence %q step %d: pause duration must be greater than 0", seq.Name, i+1)
			}
			payload := models.PausePayload{
				DurationSeconds: int(duration.Round(time.Second).Seconds()),
				Reason:          step.Reason,
			}
			if payload.DurationSeconds <= 0 {
				return nil, fmt.Errorf("render sequence %q step %d: pause duration must be at least 1s", seq.Name, i+1)
			}
			payloadBytes, _ := json.Marshal(payload)
			items = append(items, models.QueueItem{
				Type:    models.QueueItemTypePause,
				Status:  models.QueueItemStatusPending,
				Payload: payloadBytes,
			})

		case StepTypeConditional:
			text, err := renderText(seq.Name, step.Content, data)
			if err != nil {
				return nil, fmt.Errorf("render sequence %q step %d: %w", seq.Name, i+1, err)
			}
			condType, expr, err := conditionTypeFromStep(step)
			if err != nil {
				return nil, fmt.Errorf("render sequence %q step %d: %w", seq.Name, i+1, err)
			}
			payload := models.ConditionalPayload{
				ConditionType: condType,
				Expression:    expr,
				Message:       text,
			}
			payloadBytes, _ := json.Marshal(payload)
			items = append(items, models.QueueItem{
				Type:    models.QueueItemTypeConditional,
				Status:  models.QueueItemStatusPending,
				Payload: payloadBytes,
			})

		default:
			return nil, fmt.Errorf("render sequence %q step %d: unknown step type %q", seq.Name, i+1, step.Type)
		}
	}

	return items, nil
}

func renderText(name, content string, data map[string]string) (string, error) {
	parsed, err := template.New(name).
		Funcs(template.FuncMap{"default": defaultValue}).
		Option("missingkey=zero").
		Parse(content)
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", name, err)
	}

	var out strings.Builder
	if err := parsed.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render template %q: %w", name, err)
	}

	return out.String(), nil
}

func defaultValue(def string, value any) string {
	if value == nil {
		return def
	}

	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return def
		}
		return v
	default:
		text := strings.TrimSpace(fmt.Sprint(v))
		if text == "" {
			return def
		}
		return text
	}
}

func conditionTypeFromStep(step SequenceStep) (models.ConditionType, string, error) {
	whenRaw := strings.TrimSpace(step.When)
	whenLower := strings.ToLower(whenRaw)

	switch {
	case strings.HasPrefix(whenLower, "expr:"):
		expr := strings.TrimSpace(whenRaw[len("expr:"):])
		if expr == "" {
			return "", "", fmt.Errorf("conditional expression is required")
		}
		return models.ConditionTypeCustomExpression, expr, nil
	case strings.HasPrefix(whenLower, "expression:"):
		expr := strings.TrimSpace(whenRaw[len("expression:"):])
		if expr == "" {
			return "", "", fmt.Errorf("conditional expression is required")
		}
		return models.ConditionTypeCustomExpression, expr, nil
	}

	normalized := strings.ReplaceAll(strings.ToLower(whenRaw), "_", "-")

	switch normalized {
	case "idle", "when-idle", "whenidle":
		return models.ConditionTypeWhenIdle, "", nil
	case "after-cooldown", "cooldown", "cooldown-over", "aftercooldown":
		return models.ConditionTypeAfterCooldown, strings.TrimSpace(step.Expression), nil
	case "after-previous", "afterprevious":
		return models.ConditionTypeAfterPrevious, "", nil
	case "queue-empty", "queueempty":
		return models.ConditionTypeCustomExpression, "queue_length == 0", nil
	case "custom", "expression", "expr":
		expr := strings.TrimSpace(step.Expression)
		if expr == "" {
			return "", "", fmt.Errorf("conditional expression is required")
		}
		return models.ConditionTypeCustomExpression, expr, nil
	default:
		return "", "", fmt.Errorf("unknown conditional when %q", step.When)
	}
}
