// Package tui implements the Swarm terminal user interface.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/state"
)

// StateChangeMsg wraps a state engine StateChange for the TUI.
type StateChangeMsg struct {
	AgentID       string
	PreviousState models.AgentState
	CurrentState  models.AgentState
	StateInfo     models.StateInfo
	Timestamp     time.Time
}

// toStateChangeMsg converts a state.StateChange to a StateChangeMsg.
func toStateChangeMsg(change state.StateChange) StateChangeMsg {
	return StateChangeMsg{
		AgentID:       change.AgentID,
		PreviousState: change.PreviousState,
		CurrentState:  change.CurrentState,
		StateInfo:     change.StateInfo,
		Timestamp:     change.Timestamp,
	}
}

// SubscriptionErrorMsg indicates a subscription failure.
type SubscriptionErrorMsg struct {
	Err error
}

// stateSubscriber bridges the state engine to the TUI.
type stateSubscriber struct {
	program *tea.Program
}

// OnStateChange implements state.Subscriber.
func (s *stateSubscriber) OnStateChange(change state.StateChange) {
	if s.program != nil {
		s.program.Send(toStateChangeMsg(change))
	}
}

// SubscribeToStateChanges returns a tea.Cmd that subscribes to state changes.
// The returned command will send StateChangeMsg messages to the program.
// Call with the state engine and subscriber ID.
func SubscribeToStateChanges(engine *state.Engine, subscriberID string) func(*tea.Program) tea.Cmd {
	return func(program *tea.Program) tea.Cmd {
		return func() tea.Msg {
			if engine == nil {
				return SubscriptionErrorMsg{Err: nil}
			}

			subscriber := &stateSubscriber{program: program}
			if err := engine.Subscribe(subscriberID, subscriber); err != nil {
				return SubscriptionErrorMsg{Err: err}
			}

			// Return nil - state changes will come as separate messages
			return nil
		}
	}
}

// UnsubscribeFromStateChanges returns a tea.Cmd that unsubscribes from state changes.
func UnsubscribeFromStateChanges(engine *state.Engine, subscriberID string) tea.Cmd {
	return func() tea.Msg {
		if engine != nil {
			_ = engine.Unsubscribe(subscriberID)
		}
		return nil
	}
}

// ConnectionStatusMsg indicates connection/reconnection status.
type ConnectionStatusMsg struct {
	Connected bool
	Error     error
}

// ReconnectionAttemptMsg indicates a reconnection is being attempted.
type ReconnectionAttemptMsg struct {
	Attempt     int
	MaxAttempts int
}

// InitialAgentsMsg contains the initial list of agents loaded on startup.
type InitialAgentsMsg struct {
	Agents []*models.Agent
	Err    error
}

// LoadInitialAgents returns a tea.Cmd that loads all agents from the state engine.
func LoadInitialAgents(engine *state.Engine) tea.Cmd {
	return func() tea.Msg {
		if engine == nil {
			return InitialAgentsMsg{Agents: nil, Err: nil}
		}
		agents, err := engine.ListAgents(context.Background())
		return InitialAgentsMsg{Agents: agents, Err: err}
	}
}
