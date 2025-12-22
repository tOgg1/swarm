// Package tui implements the Swarm terminal user interface.
package tui

import (
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

func sampleTranscriptLines() ([]string, []time.Time) {
	content := sampleTranscript()
	lines := strings.Split(content, "\n")
	now := time.Now()
	start := now.Add(-time.Duration(len(lines)) * 8 * time.Second)
	timestamps := make([]time.Time, len(lines))
	for i := range lines {
		timestamps[i] = start.Add(time.Duration(i) * 8 * time.Second)
	}
	return lines, timestamps
}

func sampleTranscript() string {
	return `$ opencode
[info] Starting OpenCode agent...
[info] Model: gpt-5
[info] Workspace: /home/user/projects/api-service

> Analyzing codebase structure...

Found 42 files in src/
Found 15 files in tests/

> Running initial assessment...

The codebase appears to be a REST API service with:
- Express.js server setup
- PostgreSQL database connection
- JWT authentication middleware
- Basic CRUD endpoints for users and products

> Ready for instructions.

$ What tests are failing?

> Checking test results...

Running: npm test

` + "```" + `
FAIL src/users.test.js
  ✓ should create a new user (45ms)
  ✗ should validate email format (12ms)
    Expected: validation error
    Received: success

PASS src/products.test.js
  ✓ should list products (23ms)
  ✓ should filter by category (31ms)
` + "```" + `

Error: 1 test failed in users.test.js
The email validation test expects an error but receives success.

> Investigating the issue...

Looking at src/validators/email.js:
The regex pattern is missing the TLD validation.

> Suggested fix:

` + "```javascript" + `
// Current (incorrect)
const emailRegex = /^[^@]+@[^@]+$/

// Fixed
const emailRegex = /^[^@]+@[^@]+\.[a-zA-Z]{2,}$/
` + "```" + `

? Apply this fix? [y/n]
`
}

func sampleQueueEditors() map[string]*queueEditorState {
	agents := []string{"Agent A1", "Agent B7", "Agent C3", "Agent D4"}
	editors := make(map[string]*queueEditorState, len(agents))
	for _, agent := range agents {
		editors[agent] = &queueEditorState{
			Items:    sampleQueueItems(),
			Selected: 0,
		}
	}
	return editors
}

func sampleQueueItems() []queueItem {
	return []queueItem{
		{
			ID:      "q-01",
			Kind:    models.QueueItemTypeMessage,
			Summary: "Summarize latest PR feedback and next steps.",
			Status:  models.QueueItemStatusPending,
		},
		{
			ID:      "q-02",
			Kind:    models.QueueItemTypePause,
			Summary: "Pause 5m (waiting on reviewer).",
			Status:  models.QueueItemStatusPending,
		},
		{
			ID:      "q-03",
			Kind:    models.QueueItemTypeMessage,
			Summary: "Draft follow-up message to the team.",
			Status:  models.QueueItemStatusPending,
		},
	}
}

func sampleApprovals() map[string][]approvalItem {
	now := time.Now()
	return map[string][]approvalItem{
		"ws-1": {
			{
				ID:          "appr-101",
				Agent:       "Agent A1",
				RequestType: models.ApprovalRequestType("file_changes"),
				Summary:     "Apply config changes to auth middleware.",
				Status:      models.ApprovalStatusPending,
				Risk:        "medium",
				Details:     "Agent proposes edits to `auth.go` to allow token refresh.",
				Snippet:     "+ if tokenExpired {\n+   return refreshToken(ctx)\n+ }",
				CreatedAt:   now.Add(-8 * time.Minute),
			},
		},
		"ws-2": {
			{
				ID:          "appr-204",
				Agent:       "Agent B7",
				RequestType: models.ApprovalRequestType("shell_command"),
				Summary:     "Run database migration.",
				Status:      models.ApprovalStatusPending,
				Risk:        "high",
				Details:     "Command will apply pending migrations to production DB.",
				Snippet:     "make migrate-up ENV=prod",
				CreatedAt:   now.Add(-3 * time.Minute),
			},
			{
				ID:          "appr-205",
				Agent:       "Agent B7",
				RequestType: models.ApprovalRequestType("file_changes"),
				Summary:     "Update README badges and version.",
				Status:      models.ApprovalStatusPending,
				Risk:        "low",
				Details:     "Documentation-only change requested.",
				Snippet:     "+ Swarm v0.3.1",
				CreatedAt:   now.Add(-1 * time.Minute),
			},
		},
	}
}
