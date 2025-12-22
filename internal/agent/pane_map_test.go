package agent

import "testing"

func TestPaneMap_RegisterLookupUpdate(t *testing.T) {
	m := NewPaneMap()
	if err := m.Register("agent-1", "%1", "session:0.1"); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	if agentID, ok := m.AgentForPaneID("%1"); !ok || agentID != "agent-1" {
		t.Fatalf("expected agent-1 for pane id")
	}
	if agentID, ok := m.AgentForPaneTarget("session:0.1"); !ok || agentID != "agent-1" {
		t.Fatalf("expected agent-1 for pane target")
	}

	info, ok := m.PaneInfoForAgent("agent-1")
	if !ok || info.PaneTarget != "session:0.1" {
		t.Fatalf("unexpected pane info: %+v", info)
	}

	if err := m.UpdatePaneTarget("%1", "session:1.2"); err != nil {
		t.Fatalf("update target failed: %v", err)
	}

	if _, ok := m.AgentForPaneTarget("session:0.1"); ok {
		t.Fatalf("expected old target to be removed")
	}
	if agentID, ok := m.AgentForPaneTarget("session:1.2"); !ok || agentID != "agent-1" {
		t.Fatalf("expected updated target mapping")
	}
}

func TestPaneMap_Conflicts(t *testing.T) {
	m := NewPaneMap()
	if err := m.Register("agent-1", "%1", "session:0.1"); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := m.Register("agent-2", "%1", "session:0.2"); err == nil {
		t.Fatalf("expected conflict on pane id")
	}
	if err := m.Register("agent-2", "%2", "session:0.1"); err == nil {
		t.Fatalf("expected conflict on pane target")
	}
}

func TestPaneMap_Unregister(t *testing.T) {
	m := NewPaneMap()
	if err := m.Register("agent-1", "%1", "session:0.1"); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	if err := m.UnregisterAgent("agent-1"); err != nil {
		t.Fatalf("unregister agent failed: %v", err)
	}
	if _, ok := m.AgentForPaneID("%1"); ok {
		t.Fatalf("expected mapping removed")
	}

	if err := m.Register("agent-2", "%2", "session:0.2"); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := m.UnregisterPaneID("%2"); err != nil {
		t.Fatalf("unregister pane id failed: %v", err)
	}
	if _, ok := m.AgentForPaneTarget("session:0.2"); ok {
		t.Fatalf("expected mapping removed")
	}
}

func TestPaneMap_RegisterReplaces(t *testing.T) {
	m := NewPaneMap()
	if err := m.Register("agent-1", "%1", "session:0.1"); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := m.Register("agent-1", "%2", "session:1.1"); err != nil {
		t.Fatalf("register replace failed: %v", err)
	}
	if _, ok := m.AgentForPaneID("%1"); ok {
		t.Fatalf("expected old pane id mapping removed")
	}
	if agentID, ok := m.AgentForPaneID("%2"); !ok || agentID != "agent-1" {
		t.Fatalf("expected new pane id mapping")
	}
}
