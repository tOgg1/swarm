// Package cli provides structured error output helpers.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrorEnvelope is the JSON/JSONL error response shape.
type ErrorEnvelope struct {
	Error ErrorPayload `json:"error"`
}

// ErrorPayload carries structured error details.
type ErrorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// ExitError carries an exit code and whether output was already printed.
type ExitError struct {
	Code    int
	Err     error
	Printed bool
}

func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func handleCLIError(err error) error {
	if err == nil {
		return nil
	}

	exitCode := exitCodeFromError(err)

	if IsJSONOutput() || IsJSONLOutput() {
		envelope := buildErrorEnvelope(err)
		_ = WriteOutput(os.Stdout, envelope)
	} else {
		fmt.Fprintln(os.Stderr, err.Error())
	}

	return &ExitError{
		Code:    exitCode,
		Err:     err,
		Printed: true,
	}
}

func buildErrorEnvelope(err error) ErrorEnvelope {
	code, message, hint, details, _ := classifyError(err)
	return ErrorEnvelope{
		Error: ErrorPayload{
			Code:    code,
			Message: message,
			Hint:    hint,
			Details: details,
		},
	}
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code != 0 {
		return exitErr.Code
	}
	_, _, _, _, code := classifyError(err)
	return code
}

func classifyError(err error) (code, message, hint string, details map[string]any, exitCode int) {
	exitCode = 1
	if err == nil {
		return "ERR_UNKNOWN", "", "", nil, exitCode
	}

	message = err.Error()

	var preflight *PreflightError
	if errors.As(err, &preflight) {
		code = "ERR_PREFLIGHT"
		message = preflight.Message
		if preflight.Err != nil {
			message = fmt.Sprintf("%s: %v", preflight.Message, preflight.Err)
		}
		hint = preflight.Hint
		if preflight.NextStep != "" {
			details = map[string]any{
				"next_step": preflight.NextStep,
			}
		}
		return code, message, hint, details, 2
	}

	lower := strings.ToLower(message)

	switch {
	case strings.Contains(lower, "ambiguous"):
		code = "ERR_AMBIGUOUS"
		hint = "Use a longer prefix or full ID."
	case strings.Contains(lower, "not found"):
		code = "ERR_NOT_FOUND"
		resource, id := inferResourceAndID(lower, message)
		if resource != "" {
			details = map[string]any{
				"resource": resource,
			}
			if id != "" {
				details["id"] = id
			}
			hint = listHintForResource(resource)
		}
	case strings.Contains(lower, "already exists"):
		code = "ERR_EXISTS"
	case strings.Contains(lower, "unknown flag"):
		code = "ERR_INVALID_FLAG"
	case strings.Contains(lower, "invalid") || strings.Contains(lower, "required") || strings.Contains(lower, "usage") || strings.Contains(lower, "must"):
		code = "ERR_INVALID"
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "timeout") || strings.Contains(lower, "connection"):
		code = "ERR_OPERATION_FAILED"
		exitCode = 2
	case strings.Contains(lower, "failed to") || strings.Contains(lower, "unable to"):
		code = "ERR_OPERATION_FAILED"
		exitCode = 2
	default:
		code = "ERR_UNKNOWN"
	}

	return code, message, hint, details, exitCode
}

func inferResourceAndID(lower, original string) (string, string) {
	resource := ""
	switch {
	case strings.Contains(lower, "workspace"):
		resource = "workspace"
	case strings.Contains(lower, "node"):
		resource = "node"
	case strings.Contains(lower, "agent"):
		resource = "agent"
	}

	return resource, extractQuotedValue(original)
}

func extractQuotedValue(message string) string {
	start := strings.Index(message, "'")
	if start == -1 {
		return ""
	}
	end := strings.Index(message[start+1:], "'")
	if end == -1 {
		return ""
	}
	return message[start+1 : start+1+end]
}

func listHintForResource(resource string) string {
	switch resource {
	case "node":
		return "Run `swarm node list` to see valid IDs."
	case "workspace":
		return "Run `swarm ws list` to see valid IDs."
	case "agent":
		return "Run `swarm agent list` to see valid IDs."
	default:
		return ""
	}
}
