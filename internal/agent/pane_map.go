// Package agent provides agent-related helpers.
package agent

import (
	"errors"
	"sync"
)

var (
	// ErrInvalidAgentID indicates a missing agent ID.
	ErrInvalidAgentID = errors.New("agent id is required")
	// ErrInvalidPaneID indicates a missing pane ID.
	ErrInvalidPaneID = errors.New("pane id is required")
	// ErrInvalidPaneTarget indicates a missing pane target.
	ErrInvalidPaneTarget = errors.New("pane target is required")
	// ErrPaneNotFound indicates a pane mapping was not found.
	ErrPaneNotFound = errors.New("pane not found")
	// ErrAgentNotFound indicates an agent mapping was not found.
	ErrAgentNotFound = errors.New("agent not found")
	// ErrMappingConflict indicates an existing mapping is owned by another agent.
	ErrMappingConflict = errors.New("mapping conflict")
)

// PaneInfo contains pane metadata for an agent.
type PaneInfo struct {
	PaneID     string
	PaneTarget string
}

// PaneMap tracks bidirectional mappings between agents and tmux panes.
type PaneMap struct {
	mu           sync.RWMutex
	byAgent      map[string]PaneInfo
	byPaneID     map[string]string
	byPaneTarget map[string]string
}

// NewPaneMap initializes a new pane map.
func NewPaneMap() *PaneMap {
	return &PaneMap{
		byAgent:      make(map[string]PaneInfo),
		byPaneID:     make(map[string]string),
		byPaneTarget: make(map[string]string),
	}
}

// Register associates an agent with a pane id and target.
func (m *PaneMap) Register(agentID, paneID, paneTarget string) error {
	if agentID == "" {
		return ErrInvalidAgentID
	}
	if paneID == "" {
		return ErrInvalidPaneID
	}
	if paneTarget == "" {
		return ErrInvalidPaneTarget
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.byPaneID[paneID]; ok && existing != agentID {
		return ErrMappingConflict
	}
	if existing, ok := m.byPaneTarget[paneTarget]; ok && existing != agentID {
		return ErrMappingConflict
	}

	// Remove previous mapping for agent if it exists.
	if prev, ok := m.byAgent[agentID]; ok {
		delete(m.byPaneID, prev.PaneID)
		delete(m.byPaneTarget, prev.PaneTarget)
	}

	m.byAgent[agentID] = PaneInfo{PaneID: paneID, PaneTarget: paneTarget}
	m.byPaneID[paneID] = agentID
	m.byPaneTarget[paneTarget] = agentID

	return nil
}

// UpdatePaneTarget updates the pane target for a pane id.
func (m *PaneMap) UpdatePaneTarget(paneID, newTarget string) error {
	if paneID == "" {
		return ErrInvalidPaneID
	}
	if newTarget == "" {
		return ErrInvalidPaneTarget
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	agentID, ok := m.byPaneID[paneID]
	if !ok {
		return ErrPaneNotFound
	}

	if existing, ok := m.byPaneTarget[newTarget]; ok && existing != agentID {
		return ErrMappingConflict
	}

	info := m.byAgent[agentID]
	delete(m.byPaneTarget, info.PaneTarget)
	info.PaneTarget = newTarget
	m.byAgent[agentID] = info
	m.byPaneTarget[newTarget] = agentID

	return nil
}

// AgentForPaneID returns the agent id for a pane id.
func (m *PaneMap) AgentForPaneID(paneID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	agentID, ok := m.byPaneID[paneID]
	return agentID, ok
}

// AgentForPaneTarget returns the agent id for a pane target.
func (m *PaneMap) AgentForPaneTarget(target string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	agentID, ok := m.byPaneTarget[target]
	return agentID, ok
}

// PaneInfoForAgent returns pane info for an agent.
func (m *PaneMap) PaneInfoForAgent(agentID string) (PaneInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	info, ok := m.byAgent[agentID]
	return info, ok
}

// UnregisterAgent removes the mapping for an agent id.
func (m *PaneMap) UnregisterAgent(agentID string) error {
	if agentID == "" {
		return ErrInvalidAgentID
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	info, ok := m.byAgent[agentID]
	if !ok {
		return ErrAgentNotFound
	}

	delete(m.byAgent, agentID)
	delete(m.byPaneID, info.PaneID)
	delete(m.byPaneTarget, info.PaneTarget)

	return nil
}

// UnregisterPaneID removes the mapping for a pane id.
func (m *PaneMap) UnregisterPaneID(paneID string) error {
	if paneID == "" {
		return ErrInvalidPaneID
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	agentID, ok := m.byPaneID[paneID]
	if !ok {
		return ErrPaneNotFound
	}

	info := m.byAgent[agentID]
	delete(m.byAgent, agentID)
	delete(m.byPaneID, paneID)
	delete(m.byPaneTarget, info.PaneTarget)

	return nil
}

// UnregisterPaneTarget removes the mapping for a pane target.
func (m *PaneMap) UnregisterPaneTarget(target string) error {
	if target == "" {
		return ErrInvalidPaneTarget
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	agentID, ok := m.byPaneTarget[target]
	if !ok {
		return ErrPaneNotFound
	}

	info := m.byAgent[agentID]
	delete(m.byAgent, agentID)
	delete(m.byPaneID, info.PaneID)
	delete(m.byPaneTarget, target)

	return nil
}
