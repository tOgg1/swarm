// Package tmux provides tmux helpers and utilities.
package tmux

import (
	"context"
	"fmt"
	"strings"
)

// LayoutPreset identifies a tmux layout preset.
type LayoutPreset string

const (
	LayoutPresetTiled          LayoutPreset = "tiled"
	LayoutPresetEvenHorizontal LayoutPreset = "even-horizontal"
	LayoutPresetEvenVertical   LayoutPreset = "even-vertical"
	LayoutPresetMainHorizontal LayoutPreset = "main-horizontal"
	LayoutPresetMainVertical   LayoutPreset = "main-vertical"
)

// LayoutOption configures a LayoutManager.
type LayoutOption func(*LayoutManager)

// LayoutManager manages pane layout operations.
type LayoutManager struct {
	client *Client
	preset LayoutPreset
	window string
}

// NewLayoutManager creates a new LayoutManager.
func NewLayoutManager(client *Client, opts ...LayoutOption) *LayoutManager {
	manager := &LayoutManager{
		client: client,
		preset: LayoutPresetTiled,
	}
	for _, opt := range opts {
		opt(manager)
	}
	if strings.TrimSpace(string(manager.preset)) == "" {
		manager.preset = LayoutPresetTiled
	}
	return manager
}

// WithLayoutPreset sets the layout preset to apply.
func WithLayoutPreset(preset LayoutPreset) LayoutOption {
	return func(m *LayoutManager) {
		m.preset = preset
	}
}

// WithLayoutWindow sets a window name to target (session:window).
func WithLayoutWindow(window string) LayoutOption {
	return func(m *LayoutManager) {
		m.window = strings.TrimSpace(window)
	}
}

// EnsureLayout ensures a pane count and applies the layout preset.
func (m *LayoutManager) EnsureLayout(ctx context.Context, session string, paneCount int, workDir string) error {
	if m == nil || m.client == nil {
		return fmt.Errorf("tmux client is required")
	}
	if strings.TrimSpace(session) == "" {
		return fmt.Errorf("session is required")
	}
	if paneCount <= 0 {
		return fmt.Errorf("pane count must be positive")
	}

	target := m.target(session)
	panes, err := m.client.ListPanes(ctx, target)
	if err != nil {
		return err
	}

	current := len(panes)
	for current < paneCount {
		horizontal := shouldSplitHorizontal(current, m.preset)
		if _, err := m.client.SplitWindow(ctx, target, horizontal, workDir); err != nil {
			return err
		}
		current++
	}

	return m.applyLayout(ctx, target)
}

// Balance reapplies the layout preset to rebalance panes after resizes.
func (m *LayoutManager) Balance(ctx context.Context, session string) error {
	if m == nil || m.client == nil {
		return fmt.Errorf("tmux client is required")
	}
	if strings.TrimSpace(session) == "" {
		return fmt.Errorf("session is required")
	}

	target := m.target(session)
	return m.applyLayout(ctx, target)
}

func (m *LayoutManager) target(session string) string {
	if m.window == "" {
		return session
	}
	return fmt.Sprintf("%s:%s", session, m.window)
}

func (m *LayoutManager) applyLayout(ctx context.Context, target string) error {
	preset := m.preset
	if strings.TrimSpace(string(preset)) == "" {
		preset = LayoutPresetTiled
	}
	return m.client.SelectLayout(ctx, target, string(preset))
}

func shouldSplitHorizontal(current int, preset LayoutPreset) bool {
	switch preset {
	case LayoutPresetEvenHorizontal, LayoutPresetMainHorizontal:
		return true
	case LayoutPresetEvenVertical, LayoutPresetMainVertical:
		return false
	default:
		return current%2 == 0
	}
}
