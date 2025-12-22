// Package tui implements the Swarm terminal user interface.
package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// Run launches the Swarm TUI program.
func Run() error {
	program := tea.NewProgram(initialModel(), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

type model struct {
	width       int
	height      int
	styles      styles.Styles
	view        viewID
	lastUpdated time.Time
	now         time.Time
}

const (
	minWidth   = 60
	minHeight  = 15
	staleAfter = 30 * time.Second
)

func initialModel() model {
	now := time.Now()
	return model{
		styles:      styles.DefaultStyles(),
		view:        viewDashboard,
		lastUpdated: now,
		now:         now,
	}
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "1":
			m.view = viewDashboard
		case "2":
			m.view = viewWorkspace
		case "3":
			m.view = viewAgent
		case "g":
			m.view = nextView(m.view)
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()
	}
	return m, nil
}

func (m model) View() string {
	if m.width > 0 && m.height > 0 {
		if m.width < minWidth || m.height < minHeight {
			return fmt.Sprintf("%s\n", joinLines(m.smallViewLines()))
		}
	}

	lines := []string{
		m.styles.Title.Render("Swarm TUI (preview)"),
		"",
	}

	lines = append(lines, m.viewLines()...)
	lines = append(lines, "", m.styles.Muted.Render(m.lastUpdatedLine()))
	lines = append(lines, "", m.styles.Muted.Render("Press q to quit."))

	if m.width > 0 && m.height > 0 {
		lines = append(lines, "")
		lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("Viewport: %dx%d", m.width, m.height)))
	}

	lines = append(lines, "", m.styles.Muted.Render("Shortcuts: q quit | ? help | g goto | / search | 1/2/3 views"))

	return fmt.Sprintf("%s\n", joinLines(lines))
}

func (m model) smallViewLines() []string {
	message := fmt.Sprintf("Terminal too small (%dx%d).", m.width, m.height)
	hint := fmt.Sprintf("Resize to at least %dx%d.", minWidth, minHeight)

	return []string{
		m.styles.Warning.Render(message),
		m.styles.Muted.Render(hint),
		m.styles.Muted.Render("Press q to quit."),
	}
}

type viewID int

const (
	viewDashboard viewID = iota
	viewWorkspace
	viewAgent
)

func nextView(current viewID) viewID {
	switch current {
	case viewDashboard:
		return viewWorkspace
	case viewWorkspace:
		return viewAgent
	default:
		return viewDashboard
	}
}

func (m model) viewLines() []string {
	switch m.view {
	case viewWorkspace:
		return []string{
			m.styles.Accent.Render("Workspace view"),
			m.styles.Text.Render("Workspace routing placeholder."),
		}
	case viewAgent:
		return []string{
			m.styles.Accent.Render("Agent view"),
			m.styles.Text.Render("Agent routing placeholder."),
		}
	default:
		return []string{
			m.styles.Accent.Render("Dashboard view"),
			m.styles.Text.Render("Dashboard routing placeholder."),
		}
	}
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for _, line := range lines[1:] {
		out += "\n" + line
	}
	return out
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) lastUpdatedLine() string {
	if m.lastUpdated.IsZero() {
		return "Last updated: --"
	}
	label := m.lastUpdated.Format("15:04:05")
	if m.isStale() {
		label += " (stale)"
	}
	return fmt.Sprintf("Last updated: %s", label)
}

func (m model) isStale() bool {
	if m.lastUpdated.IsZero() || m.now.IsZero() {
		return false
	}
	return m.now.Sub(m.lastUpdated) > staleAfter
}
