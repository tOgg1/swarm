// Package tui implements the Swarm terminal user interface.
package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/state"
	"github.com/opencode-ai/swarm/internal/tui/components"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

const tuiSubscriberID = "tui-main"

// Config contains TUI configuration.
type Config struct {
	// StateEngine for subscribing to state changes.
	StateEngine *state.Engine
}

// Run launches the Swarm TUI program.
func Run() error {
	return RunWithConfig(Config{})
}

// RunWithConfig launches the Swarm TUI program with configuration.
func RunWithConfig(cfg Config) error {
	m := initialModel()
	m.stateEngine = cfg.StateEngine

	program := tea.NewProgram(m, tea.WithAltScreen())

	// If we have a state engine, set up subscription after program starts
	if cfg.StateEngine != nil {
		go func() {
			// Small delay to let the program initialize
			time.Sleep(50 * time.Millisecond)
			cmd := SubscribeToStateChanges(cfg.StateEngine, tuiSubscriberID)(program)
			if cmd != nil {
				program.Send(cmd())
			}
		}()
	}

	_, err := program.Run()

	// Clean up subscription on exit
	if cfg.StateEngine != nil {
		_ = cfg.StateEngine.Unsubscribe(tuiSubscriberID)
	}

	return err
}

type model struct {
	width            int
	height           int
	styles           styles.Styles
	view             viewID
	selectedView     viewID
	showHelp         bool
	showInspector    bool
	selectedAgent    string
	pausedAll        bool
	statusMsg        string
	statusWarn       bool
	searchOpen       bool
	searchQuery      string
	searchTarget     viewID
	agentFilter      string
	paletteOpen      bool
	paletteQuery     string
	paletteIndex     int
	lastUpdated      time.Time
	stale            bool
	stateEngine      *state.Engine
	agentStates      map[string]models.AgentState
	agentInfo        map[string]models.StateInfo
	agentLast        map[string]time.Time
	stateChanges     []StateChangeMsg
	nodes            []nodeSummary
	nodesPreview     bool
	workspaceGrid    *components.WorkspaceGrid
	selectedWsID     string // Selected workspace ID for drill-down
	transcriptViewer *components.TranscriptViewer
	showTranscript   bool
}

type nodeSummary struct {
	Name        string
	Status      models.NodeStatus
	AgentCount  int
	LoadPercent int
	Alerts      int
}

const (
	minWidth   = 60
	minHeight  = 15
	staleAfter = 30 * time.Second
)

func initialModel() model {
	now := time.Now()
	grid := components.NewWorkspaceGrid()
	grid.SetWorkspaces(sampleWorkspaces())
	tv := components.NewTranscriptViewer()
	tv.SetContent(sampleTranscript())
	return model{
		styles:           styles.DefaultStyles(),
		view:             viewDashboard,
		selectedView:     viewDashboard,
		lastUpdated:      now,
		agentStates:      make(map[string]models.AgentState),
		agentInfo:        make(map[string]models.StateInfo),
		agentLast:        make(map[string]time.Time),
		stateChanges:     make([]StateChangeMsg, 0),
		nodes:            sampleNodes(),
		nodesPreview:     true,
		workspaceGrid:    grid,
		transcriptViewer: tv,
	}
}

func (m model) Init() tea.Cmd {
	return staleCheckCmd(m.lastUpdated)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.paletteOpen {
			return m.updatePalette(msg)
		}
		if m.searchOpen {
			return m.updateSearch(msg)
		}
		if msg.String() == "tab" || msg.String() == "i" {
			m.showInspector = !m.showInspector
			return m, nil
		}
		if msg.String() == "t" && m.view == viewAgent {
			m.showTranscript = !m.showTranscript
			return m, nil
		}
		// Transcript navigation when visible
		if m.showTranscript && m.transcriptViewer != nil && m.view == viewAgent {
			switch msg.String() {
			case "up", "k":
				m.transcriptViewer.ScrollUp(1)
				return m, nil
			case "down", "j":
				m.transcriptViewer.ScrollDown(1)
				return m, nil
			case "ctrl+u":
				m.transcriptViewer.ScrollUp(10)
				return m, nil
			case "ctrl+d":
				m.transcriptViewer.ScrollDown(10)
				return m, nil
			case "g":
				m.transcriptViewer.ScrollToTop()
				return m, nil
			case "G":
				m.transcriptViewer.ScrollToBottom()
				return m, nil
			case "n":
				m.transcriptViewer.NextSearchHit()
				return m, nil
			case "N":
				m.transcriptViewer.PrevSearchHit()
				return m, nil
			}
		}
		// Handle grid navigation in workspace view
		if m.view == viewWorkspace && m.workspaceGrid != nil {
			switch msg.String() {
			case "up", "k":
				m.workspaceGrid.MoveUp()
				return m, nil
			case "down", "j":
				m.workspaceGrid.MoveDown()
				return m, nil
			case "left", "h":
				m.workspaceGrid.MoveLeft()
				return m, nil
			case "right", "l":
				m.workspaceGrid.MoveRight()
				return m, nil
			case "enter":
				// Drill down into selected workspace
				if ws := m.workspaceGrid.SelectedWorkspace(); ws != nil {
					m.selectedWsID = ws.ID
					// TODO: Navigate to workspace detail view
				}
				return m, nil
			case "/":
				m.openSearch(viewWorkspace)
				return m, nil
			}
		}
		if m.view == viewAgent {
			switch msg.String() {
			case "/":
				m.openSearch(viewAgent)
				return m, nil
			}
		}
		switch msg.String() {
		case "1":
			m.view = viewDashboard
			m.selectedView = m.view
			m.closeSearch()
		case "2":
			m.view = viewWorkspace
			m.selectedView = m.view
			m.closeSearch()
		case "3":
			m.view = viewAgent
			m.selectedView = m.view
			m.closeSearch()
		case "g":
			m.view = nextView(m.view)
			m.selectedView = m.view
			m.closeSearch()
		case "left", "up":
			if m.view != viewWorkspace {
				m.selectedView = prevView(m.selectedView)
			}
		case "right", "down":
			if m.view != viewWorkspace {
				m.selectedView = nextView(m.selectedView)
			}
		case "enter":
			if m.view != viewWorkspace {
				m.view = m.selectedView
			}
		case "ctrl+k", ":":
			m.openPalette()
		case "?":
			m.showHelp = !m.showHelp
		case "r":
			m.lastUpdated = time.Now()
			m.stale = false
			return m, staleCheckCmd(m.lastUpdated)
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case StateChangeMsg:
		// Update agent state tracking
		m.agentStates[msg.AgentID] = msg.CurrentState
		m.agentInfo[msg.AgentID] = msg.StateInfo
		activityAt := msg.StateInfo.DetectedAt
		if activityAt.IsZero() {
			activityAt = msg.Timestamp
		}
		m.agentLast[msg.AgentID] = activityAt
		m.lastUpdated = msg.Timestamp
		m.stale = false

		// Keep recent state changes for display (max 10)
		m.stateChanges = append(m.stateChanges, msg)
		if len(m.stateChanges) > 10 {
			m.stateChanges = m.stateChanges[1:]
		}
		return m, staleCheckCmd(msg.Timestamp)
	case staleMsg:
		if msg.Since.Equal(m.lastUpdated) && !m.stale {
			m.stale = true
		}
	case SubscriptionErrorMsg:
		// Log subscription errors (for now just ignore)
		// In production, might show a status indicator
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
		m.breadcrumbLine(),
		m.navLine(),
		"",
	}

	if m.paletteOpen {
		lines = append(lines, m.paletteLines()...)
		lines = append(lines, "")
	}

	if m.showHelp {
		lines = append(lines, m.helpLines()...)
		lines = append(lines, "")
	}

	lines = append(lines, m.viewLines()...)
	if status := m.statusLine(); status != "" {
		lines = append(lines, "", status)
	}
	lines = append(lines, "", m.lastUpdatedView())
	lines = append(lines, "", m.styles.Muted.Render("Press q to quit."))

	if m.width > 0 && m.height > 0 {
		lines = append(lines, "")
		lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("Viewport: %dx%d", m.width, m.height)))
	}

	lines = append(lines, "", m.styles.Muted.Render("Shortcuts: arrows select | enter open | i inspector | q quit | ? help | g goto | / search | r refresh | 1/2/3 views"))

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

func prevView(current viewID) viewID {
	switch current {
	case viewDashboard:
		return viewAgent
	case viewWorkspace:
		return viewDashboard
	default:
		return viewWorkspace
	}
}

func (m *model) viewLines() []string {
	switch m.view {
	case viewWorkspace:
		mainWidth := m.width
		if m.showInspector {
			if width, _, _, _ := m.inspectorLayout(); width > 0 {
				mainWidth = width
			}
		}
		// Update grid dimensions based on window size
		if m.workspaceGrid != nil {
			m.workspaceGrid.Width = mainWidth
			m.workspaceGrid.Height = m.height - 10 // Reserve space for header/footer
		}

		lines := []string{
			m.styles.Accent.Render("Workspace view"),
			m.styles.Muted.Render("↑↓←→/hjkl: navigate | Enter: open | /: filter"),
		}
		if line := m.searchLine(viewWorkspace); line != "" {
			lines = append(lines, line)
		}
		lines = append(lines, "")
		if m.workspaceGrid != nil {
			lines = append(lines, m.workspaceGrid.Render(m.styles))
		} else {
			lines = append(lines, m.styles.Muted.Render("No workspaces. Use 'swarm ws create' to create one."))
		}
		return m.renderWithInspector(lines, "Workspace inspector", m.workspaceInspectorLines())
	case viewAgent:
		transcriptHint := "t: transcript"
		if m.showTranscript {
			transcriptHint = "t: hide transcript | jk/↑↓: scroll | g/G: top/bottom | n/N: search"
		}
		lines := []string{
			m.styles.Accent.Render("Agent view"),
			m.styles.Muted.Render(fmt.Sprintf("Confidence shows how certain state detection is. | %s", transcriptHint)),
		}
		if line := m.searchLine(viewAgent); line != "" {
			lines = append(lines, line)
		}
		if m.showTranscript && m.transcriptViewer != nil {
			// Show transcript view instead of agent cards
			lines = append(lines, "")
			_, inspectorWidth, _, _ := m.inspectorLayout()
			m.transcriptViewer.Width = m.width - inspectorWidth - 4
			m.transcriptViewer.Height = m.height - 10
			lines = append(lines, m.transcriptViewer.Render(m.styles))
			return m.renderWithInspector(lines, "Agent inspector", m.agentInspectorLines())
		}
		if len(m.agentStates) == 0 {
			lines = append(lines, m.styles.Muted.Render("Sample data (no agents tracked yet)."))
		} else {
			lines = append(lines, m.styles.Text.Render(fmt.Sprintf("Tracking %d agent(s):", len(m.agentStates))))
		}
		for _, card := range m.agentCards() {
			lines = append(lines, components.RenderAgentCard(m.styles, card), "")
		}
		return m.renderWithInspector(lines, "Agent inspector", m.agentInspectorLines())
	default:
		return []string{m.dashboardView()}
	}
}

func (m model) renderWithInspector(mainLines []string, title string, inspectorLines []string) []string {
	if !m.showInspector {
		return mainLines
	}

	mainWidth, inspectorWidth, gap, vertical := m.inspectorLayout()
	if inspectorWidth <= 0 {
		inspectorWidth = mainWidth
	}

	mainContent := joinLines(mainLines)
	mainPanel := lipgloss.NewStyle().Width(mainWidth).MaxWidth(mainWidth).Render(mainContent)
	inspector := components.RenderInspectorPanel(m.styles, title, inspectorLines, inspectorWidth)

	if vertical {
		lines := append([]string{}, mainLines...)
		lines = append(lines, "", inspector)
		return lines
	}

	spacer := strings.Repeat(" ", gap)
	return []string{lipgloss.JoinHorizontal(lipgloss.Top, mainPanel, spacer, inspector)}
}

func (m model) inspectorLayout() (mainWidth, inspectorWidth, gap int, vertical bool) {
	total := m.width
	if total == 0 {
		total = 96
	}

	gap = 1
	inspectorWidth = clampInt(total/4, 22, 30)
	mainWidth = total - inspectorWidth - gap

	if mainWidth < 32 {
		inspectorWidth = clampInt(total-32-gap, 18, inspectorWidth)
		mainWidth = total - inspectorWidth - gap
	}

	if mainWidth < 20 || inspectorWidth < 18 {
		return total, total, gap, true
	}

	return mainWidth, inspectorWidth, gap, false
}

func (m model) inspectorContentWidth() int {
	_, inspectorWidth, _, _ := m.inspectorLayout()
	if inspectorWidth <= 0 {
		inspectorWidth = 40
	}
	contentWidth := inspectorWidth - 4
	if contentWidth < 20 {
		contentWidth = 20
	}
	return contentWidth
}

func (m model) workspaceInspectorLines() []string {
	if m.workspaceGrid == nil {
		return []string{m.styles.Muted.Render("No workspace data loaded.")}
	}
	ws := m.workspaceGrid.SelectedWorkspace()
	if ws == nil {
		return []string{m.styles.Muted.Render("No workspace selected.")}
	}

	name := ws.Name
	if name == "" {
		name = ws.ID
	}

	repo := ws.RepoPath
	if repo != "" {
		repo = filepath.Base(repo)
	}
	if repo == "" {
		repo = "--"
	}

	lines := []string{
		m.styles.Text.Render(fmt.Sprintf("Name: %s", name)),
		m.styles.Muted.Render(fmt.Sprintf("ID: %s", defaultLabel(ws.ID))),
		m.styles.Muted.Render(fmt.Sprintf("Repo: %s", repo)),
		m.styles.Muted.Render(fmt.Sprintf("Node: %s", defaultLabel(ws.NodeID))),
		fmt.Sprintf("Status: %s", renderWorkspaceStatusLabel(m.styles, ws.Status)),
		m.styles.Text.Render(fmt.Sprintf("Agents: %d", ws.AgentCount)),
	}

	if ws.GitInfo != nil {
		branch := defaultLabel(ws.GitInfo.Branch)
		line := m.styles.Muted.Render(fmt.Sprintf("Branch: %s", branch))
		if ws.GitInfo.IsDirty {
			line += m.styles.Warning.Render(" *dirty")
		}
		lines = append(lines, line)
	}

	if len(ws.Alerts) > 0 {
		lines = append(lines, m.styles.Warning.Render(fmt.Sprintf("Alerts: %d", len(ws.Alerts))))
	}

	return lines
}

func (m model) agentInspectorLines() []string {
	cards := m.agentCards()
	if len(cards) == 0 {
		return []string{m.styles.Muted.Render("No agents available.")}
	}

	var card *components.AgentCard
	if strings.TrimSpace(m.selectedAgent) != "" {
		card = findAgentCardByName(cards, m.selectedAgent)
	}
	if card == nil {
		card = mostRecentAgentCard(cards)
	}
	if card == nil {
		return []string{m.styles.Muted.Render("No agent selected.")}
	}

	stateBadge := components.RenderAgentStateBadge(m.styles, card.State)
	lines := []string{
		m.styles.Text.Render(fmt.Sprintf("Name: %s", defaultLabel(card.Name))),
		m.styles.Muted.Render(fmt.Sprintf("Type: %s", defaultLabel(string(card.Type)))),
		m.styles.Muted.Render(fmt.Sprintf("Model: %s", defaultLabel(card.Model))),
		fmt.Sprintf("State: %s", stateBadge),
		m.styles.Muted.Render(fmt.Sprintf("Confidence: %s", formatConfidence(card.Confidence))),
	}

	reason := strings.TrimSpace(card.Reason)
	if reason != "" {
		lines = append(lines, m.styles.Muted.Render("Reason:"))
		for _, line := range wrapText(reason, m.inspectorContentWidth()) {
			lines = append(lines, m.styles.Muted.Render("  "+line))
		}
	}

	queue := "--"
	if card.QueueLength >= 0 {
		queue = fmt.Sprintf("%d", card.QueueLength)
	}
	lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("Queue: %s", queue)))
	lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("Last: %s", formatActivityTime(card.LastActivity))))

	return lines
}

func findAgentCardByName(cards []components.AgentCard, name string) *components.AgentCard {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for i := range cards {
		if strings.EqualFold(cards[i].Name, name) {
			return &cards[i]
		}
	}
	return nil
}

func mostRecentAgentCard(cards []components.AgentCard) *components.AgentCard {
	if len(cards) == 0 {
		return nil
	}
	best := cards[0]
	for _, card := range cards[1:] {
		if card.LastActivity == nil {
			continue
		}
		if best.LastActivity == nil || card.LastActivity.After(*best.LastActivity) {
			best = card
		}
	}
	return &best
}

func formatConfidence(confidence models.StateConfidence) string {
	switch confidence {
	case models.StateConfidenceHigh:
		return "High"
	case models.StateConfidenceMedium:
		return "Medium"
	case models.StateConfidenceLow:
		return "Low"
	default:
		return "Unknown"
	}
}

func formatActivityTime(ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return "--"
	}
	return ts.Format("15:04:05")
}

func defaultLabel(value string) string {
	if strings.TrimSpace(value) == "" {
		return "--"
	}
	return value
}

func truncateText(value string, maxLen int) string {
	if maxLen < 3 {
		maxLen = 3
	}
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen-3] + "..."
}

func wrapText(value string, maxWidth int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{}
	}
	if maxWidth < 10 {
		maxWidth = 10
	}

	words := strings.Fields(value)
	lines := make([]string, 0, len(words))
	current := ""

	for _, word := range words {
		wordLen := len([]rune(word))
		if wordLen > maxWidth {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			parts := splitLongWord(word, maxWidth)
			if len(parts) > 0 {
				lines = append(lines, parts[:len(parts)-1]...)
				current = parts[len(parts)-1]
			}
			continue
		}

		currentLen := len([]rune(current))
		if current == "" {
			current = word
			continue
		}
		if currentLen+1+wordLen <= maxWidth {
			current += " " + word
		} else {
			lines = append(lines, current)
			current = word
		}
	}

	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func splitLongWord(word string, width int) []string {
	if width < 1 {
		return []string{word}
	}
	runes := []rune(word)
	parts := make([]string, 0, (len(runes)+width-1)/width)
	for i := 0; i < len(runes); i += width {
		end := i + width
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[i:end]))
	}
	return parts
}

func (m model) dashboardView() string {
	leftWidth, rightWidth, gap := m.dashboardWidths()
	nodesPanel := m.renderNodesPanel(leftWidth)
	activityPanel := m.renderActivityPanel(rightWidth)
	spacer := strings.Repeat(" ", gap)
	return lipgloss.JoinHorizontal(lipgloss.Top, nodesPanel, spacer, activityPanel)
}

func (m model) dashboardWidths() (leftWidth, rightWidth, gap int) {
	total := m.width
	if total == 0 {
		total = 96
	}

	gap = 1
	leftWidth = clampInt(total/3, 24, 36)
	rightWidth = total - leftWidth - gap
	if rightWidth < 30 {
		rightWidth = 30
		leftWidth = total - rightWidth - gap
	}
	if leftWidth < 24 {
		leftWidth = 24
	}
	return leftWidth, rightWidth, gap
}

func (m model) renderNodesPanel(width int) string {
	contentWidth := panelContentWidth(width)
	lines := []string{m.nodeHeaderRow(contentWidth)}

	if len(m.nodes) == 0 {
		lines = append(lines, m.styles.Muted.Render("No nodes registered yet."))
		lines = append(lines, m.styles.Muted.Render("Run `swarm node add`."))
	} else {
		for _, node := range m.nodes {
			lines = append(lines, m.nodeRow(contentWidth, node))
		}
	}

	title := "Nodes"
	if m.nodesPreview {
		title = "Nodes (preview)"
	}
	return m.renderPanel(title, width, lines)
}

func (m model) renderActivityPanel(width int) string {
	lines := make([]string, 0, 6)
	if len(m.stateChanges) > 0 {
		for i := len(m.stateChanges) - 1; i >= 0 && i >= len(m.stateChanges)-5; i-- {
			change := m.stateChanges[i]
			prevBadge := components.RenderAgentStateBadge(m.styles, change.PreviousState)
			currBadge := components.RenderAgentStateBadge(m.styles, change.CurrentState)
			line := fmt.Sprintf("%s: %s -> %s", shortID(change.AgentID), prevBadge, currBadge)
			lines = append(lines, line)
		}
	} else {
		lines = append(lines, m.styles.Muted.Render("No state changes received yet."))
		if m.stateEngine == nil {
			lines = append(lines, m.styles.Warning.Render("(State engine not configured)"))
		}
	}
	return m.renderPanel("Activity", width, lines)
}

func (m model) renderPanel(title string, width int, lines []string) string {
	content := append([]string{m.styles.Accent.Render(title)}, lines...)
	return m.styles.Panel.Copy().Width(width).Padding(0, 1).Render(joinLines(content))
}

func panelContentWidth(width int) int {
	contentWidth := width - 4
	if contentWidth < 1 {
		return 1
	}
	return contentWidth
}

type nodeColumnWidths struct {
	status int
	name   int
	agents int
	load   int
	alerts int
}

func nodeColumns(total int) nodeColumnWidths {
	gap := 1
	cols := nodeColumnWidths{
		status: 2,
		agents: 4,
		load:   4,
		alerts: 4,
	}
	fixed := cols.status + cols.agents + cols.load + cols.alerts + gap*4
	cols.name = total - fixed
	if cols.name < 1 {
		cols.name = 1
	}
	return cols
}

func (m model) nodeHeaderRow(width int) string {
	cols := nodeColumns(width)
	return m.formatNodeRow(
		cols,
		m.styles.Muted.Render("S"),
		m.styles.Muted.Render("Node"),
		m.styles.Muted.Render("Agt"),
		m.styles.Muted.Render("Load"),
		m.styles.Muted.Render("Alert"),
	)
}

func (m model) nodeRow(width int, node nodeSummary) string {
	cols := nodeColumns(width)
	status := renderNodeStatus(m.styles, node.Status)
	agents := fmt.Sprintf("%da", node.AgentCount)
	load := renderNodeLoad(m.styles, node.LoadPercent)
	alerts := renderNodeAlerts(m.styles, node.Alerts)
	return m.formatNodeRow(cols, status, node.Name, agents, load, alerts)
}

func (m model) formatNodeRow(cols nodeColumnWidths, status, name, agents, load, alerts string) string {
	sep := " "
	return padCell(cols.status, status, lipgloss.Left) +
		sep + padCell(cols.name, name, lipgloss.Left) +
		sep + padCell(cols.agents, agents, lipgloss.Right) +
		sep + padCell(cols.load, load, lipgloss.Right) +
		sep + padCell(cols.alerts, alerts, lipgloss.Right)
}

func padCell(width int, value string, align lipgloss.Position) string {
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Align(align).Render(value)
}

func renderNodeStatus(styleSet styles.Styles, status models.NodeStatus) string {
	switch status {
	case models.NodeStatusOnline:
		return styleSet.Success.Render("O")
	case models.NodeStatusOffline:
		return styleSet.Error.Render("X")
	default:
		return styleSet.Muted.Render("?")
	}
}

func renderNodeLoad(styleSet styles.Styles, percent int) string {
	if percent < 0 {
		return styleSet.Muted.Render("--")
	}
	return fmt.Sprintf("%d%%", percent)
}

func renderNodeAlerts(styleSet styles.Styles, count int) string {
	if count <= 0 {
		return styleSet.Muted.Render("-")
	}
	return styleSet.Warning.Render(fmt.Sprintf("!%d", count))
}

func renderWorkspaceStatusLabel(styleSet styles.Styles, status models.WorkspaceStatus) string {
	switch status {
	case models.WorkspaceStatusActive:
		return styleSet.Success.Render("Active")
	case models.WorkspaceStatusInactive:
		return styleSet.Muted.Render("Inactive")
	case models.WorkspaceStatusError:
		return styleSet.Error.Render("Error")
	default:
		return styleSet.Muted.Render("Unknown")
	}
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
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

func (m model) breadcrumbLine() string {
	parts := []string{
		m.styles.Muted.Render("Dashboard"),
		m.styles.Muted.Render("Workspace"),
		m.styles.Muted.Render("Agent"),
	}

	switch m.view {
	case viewDashboard:
		parts[0] = m.styles.Accent.Render("Dashboard")
	case viewWorkspace:
		parts[1] = m.styles.Accent.Render("Workspace")
	case viewAgent:
		parts[2] = m.styles.Accent.Render("Agent")
	}

	return fmt.Sprintf("%s > %s > %s", parts[0], parts[1], parts[2])
}

func (m model) navLine() string {
	dash := m.navLabel(viewDashboard)
	ws := m.navLabel(viewWorkspace)
	agent := m.navLabel(viewAgent)
	return fmt.Sprintf("Navigate: %s  %s  %s", dash, ws, agent)
}

func (m model) navLabel(view viewID) string {
	label := viewLabel(view)
	if m.selectedView == view {
		return m.styles.Accent.Render("[" + label + "]")
	}
	if m.view == view {
		return m.styles.Text.Render(label)
	}
	return m.styles.Muted.Render(label)
}

func viewLabel(view viewID) string {
	switch view {
	case viewDashboard:
		return "Dashboard"
	case viewWorkspace:
		return "Workspace"
	case viewAgent:
		return "Agent"
	default:
		return "Unknown"
	}
}

func (m model) helpLines() []string {
	return []string{
		m.styles.Accent.Render("Help"),
		m.styles.Muted.Render("Arrows: select view"),
		m.styles.Muted.Render("Enter: open selected view"),
		m.styles.Muted.Render("i/tab: toggle inspector"),
		m.styles.Muted.Render("g: cycle views"),
		m.styles.Muted.Render("q: quit"),
	}
}

type paletteAction struct {
	ID    string
	Label string
	Hint  string
}

func (m *model) openPalette() {
	m.paletteOpen = true
	m.paletteQuery = ""
	m.paletteIndex = 0
}

func (m *model) closePalette() {
	m.paletteOpen = false
	m.paletteQuery = ""
	m.paletteIndex = 0
}

func (m model) paletteLines() []string {
	actions := m.filteredPaletteActions()
	lines := []string{
		m.styles.Accent.Render("Command Palette"),
		m.styles.Muted.Render("Type to filter. Enter to run. Esc to close."),
		m.styles.Text.Render(fmt.Sprintf("> %s", m.paletteQuery)),
	}

	if len(actions) == 0 {
		lines = append(lines, m.styles.Muted.Render("No matches."))
		return lines
	}

	for i, action := range actions {
		prefix := "  "
		label := action.Label
		if action.Hint != "" {
			label = fmt.Sprintf("%s — %s", action.Label, action.Hint)
		}
		if i == m.paletteIndex {
			lines = append(lines, m.styles.Accent.Render(prefix+label))
		} else {
			lines = append(lines, m.styles.Muted.Render(prefix+label))
		}
	}

	return lines
}

func (m model) paletteActions() []paletteAction {
	helpID := "help.show"
	helpLabel := "Show help"
	if m.showHelp {
		helpID = "help.hide"
		helpLabel = "Hide help"
	}

	actions := []paletteAction{
		{ID: "view.dashboard", Label: "Navigate to dashboard", Hint: "1"},
		{ID: "view.workspace", Label: "Navigate to workspace", Hint: "2"},
		{ID: "view.agent", Label: "Navigate to agents", Hint: "3"},
		{ID: "agent.spawn", Label: "Spawn agent", Hint: "workspace"},
	}

	if m.pausedAll {
		actions = append(actions, paletteAction{ID: "agents.resume_all", Label: "Resume all agents"})
	} else {
		actions = append(actions, paletteAction{ID: "agents.pause_all", Label: "Pause all agents"})
	}

	actions = append(actions,
		paletteAction{ID: "refresh", Label: "Refresh", Hint: "r"},
		paletteAction{ID: "settings", Label: "Settings"},
		paletteAction{ID: helpID, Label: helpLabel, Hint: "?"},
		paletteAction{ID: "quit", Label: "Quit", Hint: "q"},
	)

	return actions
}

func (m model) filteredPaletteActions() []paletteAction {
	query := strings.TrimSpace(strings.ToLower(m.paletteQuery))
	actions := m.paletteActions()
	if query == "" {
		return actions
	}

	tokens := strings.Fields(query)
	filtered := make([]paletteAction, 0, len(actions))
	for _, action := range actions {
		haystack := strings.ToLower(action.Label + " " + action.Hint)
		matched := true
		for _, token := range tokens {
			if !strings.Contains(haystack, token) {
				matched = false
				break
			}
		}
		if matched {
			filtered = append(filtered, action)
		}
	}
	return filtered
}

func (m model) paletteSelectionMax() int {
	actions := m.filteredPaletteActions()
	if len(actions) == 0 {
		return 0
	}
	return len(actions) - 1
}

func (m *model) clampPaletteIndex() {
	max := m.paletteSelectionMax()
	if m.paletteIndex < 0 {
		m.paletteIndex = 0
	}
	if m.paletteIndex > max {
		m.paletteIndex = max
	}
}

func (m model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyRunes:
		m.paletteQuery += string(msg.Runes)
		m.paletteIndex = 0
	case tea.KeyBackspace, tea.KeyDelete:
		m.paletteQuery = trimLastRune(m.paletteQuery)
		m.paletteIndex = 0
	}

	switch msg.String() {
	case "esc":
		m.closePalette()
	case "enter":
		actions := m.filteredPaletteActions()
		if len(actions) == 0 {
			m.closePalette()
			return m, nil
		}
		action := actions[m.paletteIndex]
		m.closePalette()
		return m, m.applyPaletteAction(action)
	case "up":
		m.paletteIndex--
	case "down":
		m.paletteIndex++
	case "ctrl+n":
		m.paletteIndex++
	case "ctrl+p":
		m.paletteIndex--
	}

	m.clampPaletteIndex()
	return m, nil
}

func (m *model) applyPaletteAction(action paletteAction) tea.Cmd {
	switch action.ID {
	case "view.dashboard":
		m.view = viewDashboard
		m.selectedView = viewDashboard
	case "view.workspace":
		m.view = viewWorkspace
		m.selectedView = viewWorkspace
	case "view.agent":
		m.view = viewAgent
		m.selectedView = viewAgent
	case "agent.spawn":
		m.setStatus("Spawn agent not wired yet.", true)
	case "agents.pause_all":
		m.pausedAll = true
		m.setStatus("Paused all agents (preview).", false)
	case "agents.resume_all":
		m.pausedAll = false
		m.setStatus("Resumed all agents (preview).", false)
	case "refresh":
		m.lastUpdated = time.Now()
		m.stale = false
		return staleCheckCmd(m.lastUpdated)
	case "settings":
		m.setStatus("Settings panel not wired yet.", true)
	case "help.show":
		m.showHelp = true
	case "help.hide":
		m.showHelp = false
	case "quit":
		return tea.Quit
	}
	return nil
}

func trimLastRune(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	return string(runes[:len(runes)-1])
}

type staleMsg struct {
	Since time.Time
}

func staleCheckCmd(since time.Time) tea.Cmd {
	if since.IsZero() {
		return nil
	}
	return tea.Tick(staleAfter, func(time.Time) tea.Msg {
		return staleMsg{Since: since}
	})
}

func (m model) lastUpdatedLine() string {
	if m.lastUpdated.IsZero() {
		return "Last updated: --"
	}
	label := m.lastUpdated.Format("15:04:05")
	if m.stale {
		label += " (stale)"
	}
	return fmt.Sprintf("Last updated: %s", label)
}

func (m model) lastUpdatedView() string {
	line := m.lastUpdatedLine()
	if m.stale {
		return m.styles.Warning.Render(line)
	}
	return m.styles.Muted.Render(line)
}

func (m model) statusLine() string {
	if strings.TrimSpace(m.statusMsg) == "" {
		return ""
	}
	if m.statusWarn {
		return m.styles.Warning.Render(m.statusMsg)
	}
	return m.styles.Info.Render(m.statusMsg)
}

func (m *model) setStatus(message string, warning bool) {
	m.statusMsg = message
	m.statusWarn = warning
}

func shortID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func workspaceDisplayName(ws *models.Workspace) string {
	if ws == nil {
		return "--"
	}
	if strings.TrimSpace(ws.Name) != "" {
		return ws.Name
	}
	if strings.TrimSpace(ws.RepoPath) != "" {
		return filepath.Base(ws.RepoPath)
	}
	if strings.TrimSpace(ws.ID) != "" {
		return ws.ID
	}
	return "workspace"
}

func (m model) agentCards() []components.AgentCard {
	if len(m.agentStates) == 0 {
		return filterAgentCards(sampleAgentCards(), m.agentFilter)
	}

	cards := make([]components.AgentCard, 0, len(m.agentStates))
	for id, state := range m.agentStates {
		info := m.agentInfo[id]
		var lastPtr *time.Time
		if ts, ok := m.agentLast[id]; ok && !ts.IsZero() {
			last := ts
			lastPtr = &last
		}
		card := components.AgentCard{
			Name:         fmt.Sprintf("Agent %s", shortID(id)),
			State:        state,
			Confidence:   info.Confidence,
			Reason:       info.Reason,
			QueueLength:  -1,
			LastActivity: lastPtr,
		}
		if !matchesSearch(m.agentFilter, agentCardHaystack(card)) {
			continue
		}
		cards = append(cards, card)
	}

	sort.Slice(cards, func(i, j int) bool {
		return cards[i].Name < cards[j].Name
	})
	return cards
}

func sampleNodes() []nodeSummary {
	return []nodeSummary{
		{
			Name:        "local",
			Status:      models.NodeStatusOnline,
			AgentCount:  3,
			LoadPercent: 45,
			Alerts:      0,
		},
		{
			Name:        "gpu-box",
			Status:      models.NodeStatusOnline,
			AgentCount:  2,
			LoadPercent: 80,
			Alerts:      1,
		},
		{
			Name:        "prod-02",
			Status:      models.NodeStatusOffline,
			AgentCount:  0,
			LoadPercent: -1,
			Alerts:      2,
		},
	}
}

func sampleWorkspaces() []*models.Workspace {
	return []*models.Workspace{
		{
			ID:       "ws-1",
			Name:     "api-service",
			NodeID:   "local",
			RepoPath: "/home/user/projects/api-service",
			Status:   models.WorkspaceStatusActive,
			GitInfo: &models.GitInfo{
				Branch:  "main",
				IsDirty: false,
			},
			AgentCount: 3,
		},
		{
			ID:       "ws-2",
			Name:     "frontend-ui",
			NodeID:   "gpu-box",
			RepoPath: "/home/user/projects/frontend-ui",
			Status:   models.WorkspaceStatusActive,
			GitInfo: &models.GitInfo{
				Branch:  "feature/redesign",
				IsDirty: true,
			},
			AgentCount: 3,
			Alerts: []models.Alert{
				{Type: models.AlertTypeApprovalNeeded, Message: "Approval needed"},
			},
		},
		{
			ID:       "ws-3",
			Name:     "data-pipeline",
			NodeID:   "prod-02",
			RepoPath: "/opt/apps/data-pipeline",
			Status:   models.WorkspaceStatusError,
			GitInfo: &models.GitInfo{
				Branch:  "release/1.4",
				IsDirty: false,
			},
			AgentCount: 4,
			Alerts: []models.Alert{
				{Type: models.AlertTypeRateLimit, Message: "Rate limited"},
			},
		},
		{
			ID:       "ws-4",
			Name:     "ml-models",
			NodeID:   "gpu-box",
			RepoPath: "/home/user/projects/ml-models",
			Status:   models.WorkspaceStatusInactive,
			GitInfo: &models.GitInfo{
				Branch:  "main",
				IsDirty: false,
			},
			AgentCount: 0,
		},
	}
}

func sampleWorkspaceCards() []components.WorkspaceCard {
	return []components.WorkspaceCard{
		{
			Repo:          "api-service",
			Node:          "local",
			Branch:        "main",
			Pulse:         "active",
			AgentsWorking: 2,
			AgentsIdle:    1,
			AgentsBlocked: 0,
		},
		{
			Repo:          "frontend-ui",
			Node:          "gpu-box",
			Branch:        "feature/redesign",
			Pulse:         "idle",
			AgentsWorking: 0,
			AgentsIdle:    2,
			AgentsBlocked: 1,
			Alerts:        []string{"Approval needed"},
		},
		{
			Repo:          "data-pipeline",
			Node:          "prod-02",
			Branch:        "release/1.4",
			Pulse:         "active",
			AgentsWorking: 3,
			AgentsIdle:    0,
			AgentsBlocked: 1,
			Alerts:        []string{"Rate limited"},
		},
	}
}

func sampleAgentCards() []components.AgentCard {
	now := time.Now()
	return []components.AgentCard{
		{
			Name:         "Agent A1",
			Type:         models.AgentTypeOpenCode,
			Model:        "gpt-5",
			Profile:      "primary",
			State:        models.AgentStateWorking,
			Confidence:   models.StateConfidenceHigh,
			Reason:       "Processing queue: update workspace view",
			QueueLength:  2,
			LastActivity: timePtr(now.Add(-2 * time.Minute)),
		},
		{
			Name:         "Agent B7",
			Type:         models.AgentTypeClaudeCode,
			Model:        "sonnet-4",
			Profile:      "ops",
			State:        models.AgentStateAwaitingApproval,
			Confidence:   models.StateConfidenceMedium,
			Reason:       "Awaiting approval for file changes",
			QueueLength:  0,
			LastActivity: timePtr(now.Add(-12 * time.Minute)),
		},
		{
			Name:         "Agent C3",
			Type:         models.AgentTypeCodex,
			Model:        "gpt-5-codex",
			Profile:      "backup",
			State:        models.AgentStateIdle,
			Confidence:   models.StateConfidenceLow,
			Reason:       "Idle: no queued tasks",
			QueueLength:  0,
			LastActivity: timePtr(now.Add(-35 * time.Minute)),
		},
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func (m *model) openSearch(target viewID) {
	m.searchOpen = true
	m.searchTarget = target
	switch target {
	case viewWorkspace:
		if m.workspaceGrid != nil {
			m.searchQuery = m.workspaceGrid.Filter
		}
	case viewAgent:
		m.searchQuery = m.agentFilter
	}
	m.applySearchQuery()
}

func (m *model) closeSearch() {
	m.searchOpen = false
	m.searchQuery = ""
}

func (m *model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeSearch()
		return m, nil
	case "enter":
		m.applySearchSelection()
		m.closeSearch()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}

	switch msg.Type {
	case tea.KeyRunes:
		m.searchQuery += string(msg.Runes)
	case tea.KeyBackspace, tea.KeyDelete:
		m.searchQuery = trimLastRune(m.searchQuery)
	}

	m.applySearchQuery()
	return m, nil
}

func (m *model) applySearchQuery() {
	switch m.searchTarget {
	case viewWorkspace:
		if m.workspaceGrid != nil {
			m.workspaceGrid.SetFilter(m.searchQuery)
		}
	case viewAgent:
		m.agentFilter = m.searchQuery
	}
}

func (m *model) applySearchSelection() {
	if strings.TrimSpace(m.searchQuery) == "" {
		return
	}
	switch m.searchTarget {
	case viewWorkspace:
		if m.workspaceGrid == nil {
			return
		}
		ws := m.workspaceGrid.SelectedWorkspace()
		if ws == nil {
			return
		}
		m.selectedWsID = ws.ID
		m.setStatus(fmt.Sprintf("Jumped to workspace %s", workspaceDisplayName(ws)), false)
	case viewAgent:
		cards := m.agentCards()
		if len(cards) == 0 {
			return
		}
		m.selectedAgent = cards[0].Name
		m.setStatus(fmt.Sprintf("Jumped to agent %s", cards[0].Name), false)
	}
}

func (m model) searchLine(target viewID) string {
	var query string
	switch target {
	case viewWorkspace:
		if m.workspaceGrid != nil {
			query = m.workspaceGrid.Filter
		}
	case viewAgent:
		query = m.agentFilter
	}
	if target == viewWorkspace && !(m.searchOpen && m.searchTarget == target) {
		return ""
	}
	label := "Filter:"
	style := m.styles.Muted
	if m.searchOpen && m.searchTarget == target {
		label = "Search:"
		style = m.styles.Info
	}
	if strings.TrimSpace(query) == "" && label == "Filter:" {
		return ""
	}
	return style.Render(fmt.Sprintf("%s %s", label, query))
}

func filterAgentCards(cards []components.AgentCard, query string) []components.AgentCard {
	if strings.TrimSpace(query) == "" {
		return cards
	}
	filtered := make([]components.AgentCard, 0, len(cards))
	for _, card := range cards {
		if matchesSearch(query, agentCardHaystack(card)) {
			filtered = append(filtered, card)
		}
	}
	return filtered
}

func agentCardHaystack(card components.AgentCard) string {
	return strings.ToLower(strings.Join([]string{
		card.Name,
		string(card.Type),
		card.Model,
		card.Profile,
	}, " "))
}

func matchesSearch(query, haystack string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return true
	}
	tokens := strings.Fields(query)
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			continue
		}
		if !fuzzyMatch(haystack, token) {
			return false
		}
	}
	return true
}

func fuzzyMatch(haystack, token string) bool {
	if token == "" {
		return true
	}
	haystack = strings.ToLower(haystack)
	token = strings.ToLower(strings.ReplaceAll(token, " ", ""))
	if token == "" {
		return true
	}
	needle := []rune(token)
	matchIdx := 0
	for _, r := range []rune(haystack) {
		if r == needle[matchIdx] {
			matchIdx++
			if matchIdx == len(needle) {
				return true
			}
		}
	}
	return false
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
