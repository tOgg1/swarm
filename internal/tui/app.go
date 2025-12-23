// Package tui implements the Swarm terminal user interface.
package tui

import (
	"context"
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
	// Theme is the color theme name (default, high-contrast).
	Theme string
	// AgentMail configures optional mailbox integration.
	AgentMail AgentMailConfig
}

// Run launches the Swarm TUI program.
func Run() error {
	return RunWithConfig(Config{})
}

// RunWithConfig launches the Swarm TUI program with configuration.
func RunWithConfig(cfg Config) error {
	m := initialModel()
	m.stateEngine = cfg.StateEngine

	// Apply theme from config
	if cfg.Theme != "" {
		if theme, ok := styles.Themes[cfg.Theme]; ok {
			m.styles = styles.BuildStyles(theme)
		}
	}
	mailCfg := normalizeAgentMailConfig(cfg.AgentMail)
	if mailCfg.Enabled() {
		m.mailClient = newAgentMailClient(mailCfg)
		m.mailPollInterval = mailCfg.PollInterval
		m.mailThreads = nil
		m.mailSelected = -1
	}

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
	width                  int
	height                 int
	styles                 styles.Styles
	view                   viewID
	selectedView           viewID
	showHelp               bool
	showInspector          bool
	selectedAgent          string
	selectedAgentIndex     int
	pausedAll              bool
	statusMsg              string
	statusSeverity         statusSeverity
	statusExpiresAt        time.Time
	searchOpen             bool
	searchQuery            string
	searchPrevQuery        string
	searchTarget           viewID
	agentFilter            string
	approvalsOpen          bool
	approvalsByWorkspace   map[string][]approvalItem
	approvalsSelected      int
	approvalsMarked        map[string]map[string]bool
	approvalsBulkConfirm   bool
	approvalsBulkAction    models.ApprovalStatus
	approvalsBulkTargets   []string
	approvalsBulkWorkspace string
	auditItems             []auditItem
	auditFilter            string
	auditSelected          int
	mailThreads            []mailThread
	mailFilter             string
	mailSelected           int
	mailClient             *agentMailClient
	mailPollInterval       time.Duration
	mailSyncErr            string
	mailLastSynced         time.Time
	mailRead               map[string]bool
	actionConfirmOpen      bool
	actionConfirmAction    string
	actionConfirmAgent     string
	profileSelectOpen      bool
	profileSelectIndex     int
	profileSelectOptions   []string
	profileSelectAgent     string
	profileSwitchPending   string
	actionInFlightAction   string
	actionInFlightAgent    string
	queueEditorOpen        bool
	queueEditors           map[string]*queueEditorState
	queueEditOpen          bool
	queueEditBuffer        string
	queueEditIndex         int
	queueEditAgent         string
	queueEditMode          queueEditMode
	queueEditKind          models.QueueItemType
	queueEditPrompt        string
	transcriptSearchOpen   bool
	transcriptSearchQuery  string
	paletteOpen            bool
	paletteQuery           string
	paletteIndex           int
	lastUpdated            time.Time
	refreshingUntil        time.Time
	stale                  bool
	stateEngine            *state.Engine
	agentStates            map[string]models.AgentState
	agentInfo              map[string]models.StateInfo
	agentLast              map[string]time.Time
	agentCooldowns         map[string]time.Time
	agentRecentEvents      map[string][]time.Time // Recent state change timestamps per agent
	workspaceRecentEvents  map[string][]time.Time // Recent state change timestamps per workspace
	agentWorkspaces        map[string]string      // Cached workspace IDs per agent
	agentProfileOverrides  map[string]string
	stateChanges           []StateChangeMsg
	nodes                  []nodeSummary
	nodesPreview           bool
	workspacesPreview      bool
	workspaceGrid          *components.WorkspaceGrid
	beadsCache             map[string]beadsSnapshot
	selectedWsID           string // Selected workspace ID for drill-down
	pulseFrame             int
	transcriptViewer       *components.TranscriptViewer
	transcriptPreview      bool
	showTranscript         bool
	transcriptAutoScroll   bool
}

type nodeSummary struct {
	Name        string
	Status      models.NodeStatus
	AgentCount  int
	LoadPercent int
	Alerts      int
}

type accountSummary struct {
	Profile       string
	Provider      models.Provider
	Active        bool
	CooldownUntil *time.Time
}

type queueItem struct {
	ID      string
	Kind    models.QueueItemType
	Summary string
	Status  models.QueueItemStatus
}

type queueEditMode int

const (
	queueEditModeEdit queueEditMode = iota
	queueEditModeAdd
)

type queueEditorState struct {
	Items    []queueItem
	Selected int
}

type approvalItem struct {
	ID          string
	Agent       string
	RequestType models.ApprovalRequestType
	Summary     string
	Status      models.ApprovalStatus
	Risk        string
	Details     string
	Snippet     string
	CreatedAt   time.Time
}

type auditItem struct {
	ID         string
	Timestamp  time.Time
	Type       models.EventType
	EntityType models.EntityType
	EntityID   string
	Summary    string
	Detail     string
}

type mailThread struct {
	ID       string
	Subject  string
	Messages []mailMessage
}

type mailMessage struct {
	ID        string
	From      string
	Body      string
	CreatedAt time.Time
	Read      bool
}

const (
	minWidth             = 60
	minHeight            = 15
	refreshInterval      = 15 * time.Second
	refreshPulseDuration = 2 * time.Second
	staleAfter           = 2 * refreshInterval
	statusToastDuration  = 5 * time.Second
	maxWorkspaceEvents   = 20
)

func initialModel() model {
	now := time.Now()
	grid := components.NewWorkspaceGrid()
	grid.SetWorkspaces(sampleWorkspaces())
	tv := components.NewTranscriptViewer()
	lines, timestamps := sampleTranscriptLines()
	tv.SetLinesWithTimestamps(lines, timestamps)
	tv.ScrollToBottom()
	queueEditors := sampleQueueEditors()
	approvals := sampleApprovals()
	auditItems := sampleAuditItems()
	mailThreads := sampleMailboxThreads()
	beadsCache := sampleBeadsSnapshots()
	return model{
		styles:                styles.DefaultStyles(),
		view:                  viewDashboard,
		selectedView:          viewDashboard,
		lastUpdated:           now,
		selectedAgentIndex:    -1,
		agentStates:           make(map[string]models.AgentState),
		agentInfo:             make(map[string]models.StateInfo),
		agentLast:             make(map[string]time.Time),
		agentCooldowns:        make(map[string]time.Time),
		agentRecentEvents:     make(map[string][]time.Time),
		workspaceRecentEvents: make(map[string][]time.Time),
		agentWorkspaces:       make(map[string]string),
		agentProfileOverrides: make(map[string]string),
		stateChanges:          make([]StateChangeMsg, 0),
		nodes:                 sampleNodes(),
		nodesPreview:          true,
		workspaceGrid:         grid,
		workspacesPreview:     true,
		beadsCache:            beadsCache,
		queueEditors:          queueEditors,
		approvalsByWorkspace:  approvals,
		approvalsMarked:       make(map[string]map[string]bool),
		auditItems:            auditItems,
		auditSelected:         0,
		mailThreads:           mailThreads,
		mailSelected:          0,
		mailRead:              make(map[string]bool),
		transcriptViewer:      tv,
		transcriptPreview:     true,
		transcriptAutoScroll:  true,
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{staleCheckCmd(m.lastUpdated), toastTickCmd()}
	if m.mailClient != nil {
		cmds = append(cmds, m.mailboxRefreshCmd(), mailboxPollCmd(m.mailPollInterval))
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.actionConfirmOpen {
			return m.updateActionConfirm(msg)
		}
		if m.profileSelectOpen {
			return m.updateProfileSelector(msg)
		}
		if m.approvalsBulkConfirm && m.view == viewWorkspace {
			return m.updateBulkApprovalConfirm(msg)
		}
		if m.paletteOpen {
			return m.updatePalette(msg)
		}
		if m.transcriptSearchOpen {
			return m.updateTranscriptSearch(msg)
		}
		if m.searchOpen {
			return m.updateSearch(msg)
		}
		if m.queueEditOpen {
			return m.updateQueueEdit(msg)
		}
		if m.queueEditorOpen && m.view == viewAgent {
			if handled, cmd := m.updateQueueEditor(msg); handled {
				return m, cmd
			}
		}
		if m.approvalsOpen && m.view == viewWorkspace {
			if handled, cmd := m.updateApprovalsInbox(msg); handled {
				return m, cmd
			}
		}
		if msg.String() == "tab" || msg.String() == "i" {
			m.showInspector = !m.showInspector
			return m, nil
		}
		if msg.String() == "t" && m.view == viewAgent {
			m.showTranscript = !m.showTranscript
			if !m.showTranscript {
				m.closeTranscriptSearch()
			} else if m.transcriptAutoScroll && m.transcriptViewer != nil {
				m.transcriptViewer.ScrollToBottom()
			}
			return m, nil
		}
		// Transcript navigation when visible
		if m.showTranscript && m.transcriptViewer != nil && m.view == viewAgent {
			switch msg.String() {
			case "up", "k":
				m.transcriptViewer.ScrollUp(1)
				m.transcriptAutoScroll = false
				return m, nil
			case "down", "j":
				m.transcriptViewer.ScrollDown(1)
				m.transcriptAutoScroll = false
				return m, nil
			case "ctrl+u":
				m.transcriptViewer.ScrollUp(10)
				m.transcriptAutoScroll = false
				return m, nil
			case "ctrl+d":
				m.transcriptViewer.ScrollDown(10)
				m.transcriptAutoScroll = false
				return m, nil
			case "g":
				m.transcriptViewer.ScrollToTop()
				m.transcriptAutoScroll = false
				return m, nil
			case "G":
				m.transcriptViewer.ScrollToBottom()
				m.transcriptAutoScroll = false
				return m, nil
			case "n":
				m.transcriptViewer.NextSearchHit()
				m.transcriptAutoScroll = false
				return m, nil
			case "N":
				m.transcriptViewer.PrevSearchHit()
				m.transcriptAutoScroll = false
				return m, nil
			case "a":
				m.transcriptAutoScroll = !m.transcriptAutoScroll
				if m.transcriptAutoScroll {
					m.transcriptViewer.ScrollToBottom()
				}
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
			case "A":
				m.approvalsOpen = !m.approvalsOpen
				if m.approvalsOpen {
					if ws := m.selectedWorkspace(); ws != nil {
						m.clearApprovalMarks(ws.ID)
					}
				} else {
					m.cancelBulkApproval()
				}
				return m, nil
			}
		}
		if m.view == viewAgent {
			switch msg.String() {
			case "up", "k", "left", "h":
				if !m.showTranscript {
					m.moveAgentSelection(-1)
				}
				return m, nil
			case "down", "j", "right", "l":
				if !m.showTranscript {
					m.moveAgentSelection(1)
				}
				return m, nil
			}
		}
		if m.view == viewAgent {
			switch msg.String() {
			case "enter":
				// Drill down into agent detail view
				if card := m.selectedAgentCard(); card != nil && !m.showTranscript {
					m.view = viewAgentDetail
					m.selectedView = m.view
					m.showTranscript = true // Show transcript in detail view
				}
				return m, nil
			case "/":
				if m.showTranscript {
					m.openTranscriptSearch()
				} else {
					m.openSearch(viewAgent)
				}
				return m, nil
			case "Q":
				m.queueEditorOpen = !m.queueEditorOpen
				if !m.queueEditorOpen {
					m.closeQueueEdit()
				}
				return m, nil
			case "I":
				return m, m.requestAgentAction(actionInterrupt, true)
			case "R":
				return m, m.requestAgentAction(actionRestart, true)
			case "E":
				return m, m.requestAgentAction(actionExportLogs, false)
			case "P":
				// Toggle pause/resume based on current agent state
				action := actionPause
				if card := m.selectedAgentCard(); card != nil && card.State == models.AgentStatePaused {
					action = actionResume
				}
				return m, m.requestAgentAction(action, true)
			case "V":
				// View action - toggle inspector and focus on selected agent
				m.showInspector = true
				return m, nil
			}
		}
		if m.view == viewAudit {
			switch msg.String() {
			case "up", "k":
				m.moveAuditSelection(-1)
				return m, nil
			case "down", "j":
				m.moveAuditSelection(1)
				return m, nil
			case "/":
				m.openSearch(viewAudit)
				return m, nil
			case "enter":
				m.showInspector = true
				return m, nil
			}
		}
		if m.view == viewMailbox {
			switch msg.String() {
			case "up", "k":
				m.moveMailSelection(-1)
				return m, nil
			case "down", "j":
				m.moveMailSelection(1)
				return m, nil
			case "/":
				m.openSearch(viewMailbox)
				return m, nil
			case "enter":
				m.showInspector = true
				if thread, ok := m.selectedMailThread(); ok {
					m.markMailThreadRead(thread.ID)
				}
				return m, nil
			}
		}
		if m.view == viewAgentDetail {
			switch msg.String() {
			case "p":
				m.openProfileSelector()
				return m, nil
			case "I":
				return m, m.requestAgentAction(actionInterrupt, true)
			case "R":
				return m, m.requestAgentAction(actionRestart, true)
			case "P":
				action := actionPause
				if card := m.selectedAgentCard(); card != nil && card.State == models.AgentStatePaused {
					action = actionResume
				}
				return m, m.requestAgentAction(action, true)
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
			m.syncAgentSelection()
		case "4":
			m.view = viewAudit
			m.selectedView = m.view
			m.closeSearch()
			m.ensureAuditSelection(m.filteredAuditItems())
		case "5":
			m.view = viewMailbox
			m.selectedView = m.view
			m.closeSearch()
			m.ensureMailSelection(m.filteredMailThreads())
		case "g":
			m.view = nextView(m.view)
			m.selectedView = m.view
			m.closeSearch()
			if m.view == viewAgent {
				m.syncAgentSelection()
			} else if m.view == viewAudit {
				m.ensureAuditSelection(m.filteredAuditItems())
			} else if m.view == viewMailbox {
				m.ensureMailSelection(m.filteredMailThreads())
			}
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
				if m.view == viewAgent {
					m.syncAgentSelection()
				}
			}
		case "ctrl+k", ":":
			m.openPalette()
		case "ctrl+l":
			if m.view == viewWorkspace || m.view == viewAgent || m.view == viewAudit || m.view == viewMailbox {
				m.clearSearchFilter(m.view)
				m.setStatus(fmt.Sprintf("%s filter cleared.", viewLabel(m.view)), statusInfo)
				return m, nil
			}
		case "?":
			m.showHelp = !m.showHelp
		case "r":
			m.lastUpdated = time.Now()
			m.stale = false
			m.refreshingUntil = time.Now().Add(refreshPulseDuration)
			m.refreshBeadsCache()
			cmds := []tea.Cmd{staleCheckCmd(m.lastUpdated)}
			if m.mailClient != nil {
				cmds = append(cmds, m.mailboxRefreshCmd())
			}
			return m, tea.Batch(cmds...)
		case "esc":
			// Go back from agent detail view
			if m.view == viewAgentDetail {
				m.view = viewAgent
				m.selectedView = m.view
				return m, nil
			}
			return m, tea.Quit
		case "q", "ctrl+c":
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
		m.refreshingUntil = time.Now().Add(refreshPulseDuration)

		// Track recent events per agent for activity pulse (max 10 per agent)
		events := m.agentRecentEvents[msg.AgentID]
		events = append(events, activityAt)
		if len(events) > 10 {
			events = events[1:]
		}
		m.agentRecentEvents[msg.AgentID] = events
		m.recordWorkspaceActivity(msg.AgentID, activityAt)

		// Keep recent state changes for display (max 10)
		m.stateChanges = append(m.stateChanges, msg)
		if len(m.stateChanges) > 10 {
			m.stateChanges = m.stateChanges[1:]
		}
		m.syncAgentSelection()
		return m, staleCheckCmd(msg.Timestamp)
	case actionCompleteMsg:
		if msg.Action == m.actionInFlightAction && msg.Agent == m.actionInFlightAgent {
			m.actionInFlightAction = ""
			m.actionInFlightAgent = ""
		}
	case mailboxPollMsg:
		return m, tea.Batch(m.mailboxRefreshCmd(), mailboxPollCmd(m.mailPollInterval))
	case mailboxRefreshMsg:
		if msg.Err != nil {
			m.mailSyncErr = msg.Err.Error()
			return m, nil
		}
		selectedID := ""
		if thread, ok := m.selectedMailThread(); ok {
			selectedID = thread.ID
		}
		if m.mailRead == nil {
			m.mailRead = make(map[string]bool)
		}
		m.mailThreads = buildMailThreads(msg.Messages, m.mailRead)
		m.mailSyncErr = ""
		m.mailLastSynced = msg.ReceivedAt
		m.ensureMailSelection(m.filteredMailThreads())
		if selectedID != "" {
			m.restoreMailSelection(selectedID)
		}
	case staleMsg:
		if msg.Since.Equal(m.lastUpdated) && !m.stale {
			m.stale = true
		}
	case toastTickMsg:
		m.pulseFrame++
		if strings.TrimSpace(m.statusMsg) != "" && !m.statusExpiresAt.IsZero() && time.Now().After(m.statusExpiresAt) {
			m.statusMsg = ""
			m.statusSeverity = statusInfo
			m.statusExpiresAt = time.Time{}
		}
		return m, toastTickCmd()
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
	}
	if mode := m.modeLine(); mode != "" {
		lines = append(lines, mode)
	}
	if m.demoDataActive() {
		lines = append(lines, m.demoBadgeLine())
	}
	lines = append(lines, "")

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
	lines = append(lines, "", m.footerLine())

	if m.width > 0 && m.height > 0 {
		lines = append(lines, "")
		lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("Viewport: %dx%d", m.width, m.height)))
	}

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
	viewAudit
	viewMailbox
	viewAgentDetail // Full-screen agent detail view
)

type statusSeverity int

const (
	statusInfo statusSeverity = iota
	statusWarn
	statusError
)

const (
	actionInterrupt     = "interrupt"
	actionRestart       = "restart"
	actionExportLogs    = "export_logs"
	actionPause         = "pause"
	actionResume        = "resume"
	actionSwitchProfile = "switch_profile"
	actionView          = "view"
)

func nextView(current viewID) viewID {
	switch current {
	case viewDashboard:
		return viewWorkspace
	case viewWorkspace:
		return viewAgent
	case viewAgent:
		return viewAudit
	case viewAudit:
		return viewMailbox
	default:
		return viewDashboard
	}
}

func prevView(current viewID) viewID {
	switch current {
	case viewDashboard:
		return viewMailbox
	case viewWorkspace:
		return viewDashboard
	case viewAgent:
		return viewWorkspace
	case viewAudit:
		return viewAgent
	case viewMailbox:
		return viewAudit
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
			m.workspaceGrid.PulseFrame = m.pulseFrame
			m.workspaceGrid.PulseEvents = m.workspaceRecentEvents
		}

		lines := []string{
			m.styles.Accent.Render("Workspace view"),
			m.styles.Muted.Render("↑↓←→/hjkl: navigate | Enter: open | /: filter | Ctrl+L: clear | A: approvals"),
		}
		if line := m.searchLine(viewWorkspace); line != "" {
			lines = append(lines, line)
		}
		lines = append(lines, "")
		if m.workspaceGrid != nil {
			lines = append(lines, m.workspaceGrid.Render(m.styles))
		} else {
			emptyState := components.EmptyWorkspaces()
			lines = append(lines, emptyState.Render(m.styles))
		}
		inspectorTitle := "Workspace inspector"
		if m.approvalsOpen {
			inspectorTitle = "Approvals inbox"
		}
		return m.renderWithInspector(lines, inspectorTitle, m.workspaceInspectorLines())
	case viewAgent:
		transcriptHint := "t: transcript"
		if m.showTranscript {
			autoHint := "a: auto-scroll on"
			if !m.transcriptAutoScroll {
				autoHint = "a: auto-scroll off"
			}
			transcriptHint = fmt.Sprintf("t: hide transcript | %s | /: search | jk/↑↓: scroll | g/G: top/bottom | n/N: next/prev", autoHint)
		}
		queueHint := "Q: queue"
		if m.queueEditorOpen {
			queueHint = "Q: hide queue"
		}
		lines := []string{
			m.styles.Accent.Render("Agent view"),
			m.styles.Muted.Render(fmt.Sprintf("Confidence shows how certain state detection is. | %s | %s", transcriptHint, queueHint)),
		}
		if line := m.searchLine(viewAgent); line != "" {
			lines = append(lines, line)
		}
		lines = append(lines, m.agentSearchStatusLines()...)
		if line := m.transcriptSearchLine(); line != "" {
			lines = append(lines, line)
		}
		if line := m.actionConfirmLine(); line != "" {
			lines = append(lines, line)
		}
		if m.showTranscript && m.transcriptViewer != nil {
			// Show transcript view instead of agent cards
			lines = append(lines, "")
			if m.transcriptPreview {
				lines = append(lines, m.styles.Warning.Render("Demo transcript (sample)"))
			}
			_, inspectorWidth, _, _ := m.inspectorLayout()
			m.transcriptViewer.Width = m.width - inspectorWidth - 4
			m.transcriptViewer.Height = m.height - 10
			lines = append(lines, m.transcriptViewer.Render(m.styles))
			return m.renderWithInspector(lines, "Agent inspector", m.agentInspectorLines())
		}
		cards := m.agentCards()
		if len(cards) == 0 {
			if strings.TrimSpace(m.agentFilter) != "" {
				emptyState := components.EmptyAgentsFiltered(m.agentFilter)
				lines = append(lines, emptyState.Render(m.styles))
			} else if len(m.agentStates) == 0 {
				emptyState := components.EmptyAgents()
				lines = append(lines, emptyState.Render(m.styles))
			} else {
				lines = append(lines, m.styles.Muted.Render("No agents available."))
			}
			return m.renderWithInspector(lines, "Agent inspector", m.agentInspectorLines())
		}
		if len(m.agentStates) == 0 {
			lines = append(lines, m.styles.Muted.Render("Sample data (no agents tracked yet)."))
		} else {
			lines = append(lines, m.styles.Text.Render(fmt.Sprintf("Tracking %d agent(s):", len(m.agentStates))))
		}
		selectedIndex := m.selectedAgentIndexFor(cards)
		for i, card := range cards {
			lines = append(lines, components.RenderAgentCard(m.styles, card, i == selectedIndex))
			if i == selectedIndex {
				// Show quick action hints below the selected card
				actionHint := components.RenderQuickActionHint(m.styles, card.State)
				if actionHint != "" {
					lines = append(lines, actionHint)
				}
			}
			lines = append(lines, "")
		}
		return m.renderWithInspector(lines, "Agent inspector", m.agentInspectorLines())
	case viewAudit:
		return m.auditViewLines()
	case viewMailbox:
		return m.mailboxViewLines()
	case viewAgentDetail:
		return m.agentDetailView()
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

func (m model) selectedWorkspace() *models.Workspace {
	if m.workspaceGrid == nil {
		return nil
	}
	return m.workspaceGrid.SelectedWorkspace()
}

func (m model) workspaceInspectorLines() []string {
	if m.approvalsOpen {
		return m.approvalsInboxLines()
	}
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

	// Git status section
	lines = append(lines, "", m.styles.Accent.Render("Git"))
	if ws.GitInfo != nil && ws.GitInfo.IsRepo {
		branchStatus := components.RenderGitStatusCompact(m.styles, ws.GitInfo)
		lines = append(lines, fmt.Sprintf("  Branch: %s", branchStatus))

		syncStatus := components.RenderGitSyncStatus(m.styles, ws.GitInfo)
		if syncStatus != "" {
			lines = append(lines, fmt.Sprintf("  Sync: %s", syncStatus))
		}

		gitData := components.GitStatusData{Info: ws.GitInfo}
		changeSummary := components.RenderGitChangeSummary(m.styles, gitData)
		if changeSummary != "" {
			lines = append(lines, fmt.Sprintf("  Changes: %s", changeSummary))
		}

		if ws.GitInfo.LastCommit != "" {
			shortHash := ws.GitInfo.LastCommit
			if len(shortHash) > 7 {
				shortHash = shortHash[:7]
			}
			lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("  Commit: %s", shortHash)))
		}
	} else {
		lines = append(lines, m.styles.Muted.Render("  Not a git repository"))
	}

	lines = append(lines, "")
	lines = append(lines, m.beadsPanelLines(ws)...)

	if len(ws.Alerts) > 0 {
		lines = append(lines, m.styles.Warning.Render(fmt.Sprintf("Alerts: %d", len(ws.Alerts))))
	}

	return lines
}

func (m model) approvalsInboxLines() []string {
	ws := m.selectedWorkspace()
	if ws == nil {
		return []string{m.styles.Warning.Render("No workspace selected.")}
	}
	approvals := m.approvalsForWorkspace(ws.ID)
	if len(approvals) == 0 {
		return []string{
			m.styles.Text.Render("Approvals inbox"),
			m.styles.Muted.Render(fmt.Sprintf("Workspace: %s", workspaceDisplayName(ws))),
			m.styles.Muted.Render("No pending approvals."),
			m.styles.Muted.Render("Press A to close."),
		}
	}

	selectedIdx := approvalSelectionIndex(m.approvalsSelected, len(approvals))
	pending := countApprovalStatus(approvals, models.ApprovalStatusPending)
	selectedCount := m.countMarkedApprovals(ws.ID, approvals)
	lines := []string{
		m.styles.Text.Render("Approvals inbox"),
		m.styles.Muted.Render(fmt.Sprintf("Workspace: %s", workspaceDisplayName(ws))),
		m.styles.Muted.Render(fmt.Sprintf("Pending: %d / %d", pending, len(approvals))),
		m.styles.Muted.Render(fmt.Sprintf("Selected: %d", selectedCount)),
		m.styles.Muted.Render("↑↓/jk: move | x/space: select | Ctrl+A: select all | y: approve | n: deny | Y/N: bulk | esc: close"),
	}

	maxWidth := m.inspectorContentWidth()
	for i, item := range approvals {
		prefix := "  "
		if i == selectedIdx {
			prefix = "> "
		}
		marker := "[ ]"
		if m.isApprovalMarked(ws.ID, item.ID) {
			marker = "[x]"
		}
		status := approvalStatusBadge(m.styles, item.Status)
		risk := approvalRiskBadge(m.styles, item.Risk)
		line := fmt.Sprintf("%s%s %s %s %s", prefix, marker, status, risk, approvalSummary(item))
		if maxWidth > 0 {
			line = truncateText(line, maxWidth)
		}
		if i == selectedIdx {
			lines = append(lines, m.styles.Accent.Render(line))
		} else {
			lines = append(lines, m.styles.Text.Render(line))
		}
	}

	lines = append(lines, "")
	lines = append(lines, m.approvalDetailLines(approvals[selectedIdx], maxWidth)...)
	if m.approvalsBulkConfirm && m.approvalsBulkWorkspace == ws.ID {
		lines = append(lines, "")
		lines = append(lines, m.bulkApprovalConfirmLine())
	}

	return lines
}

func (m model) approvalDetailLines(item approvalItem, maxWidth int) []string {
	lines := []string{
		m.styles.Text.Render("Details"),
		m.styles.Muted.Render(fmt.Sprintf("Agent: %s", defaultLabel(item.Agent))),
		m.styles.Muted.Render(fmt.Sprintf("Type: %s", defaultLabel(string(item.RequestType)))),
		m.styles.Muted.Render(fmt.Sprintf("Status: %s", approvalStatusLabel(m.styles, item.Status))),
	}
	if item.Risk != "" {
		lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("Risk: %s", approvalRiskLabel(m.styles, item.Risk))))
	}
	if summary := strings.TrimSpace(item.Summary); summary != "" {
		lines = append(lines, m.styles.Muted.Render("Summary:"))
		lines = append(lines, wrapIndented(summary, maxWidth, m.styles.Muted, "  ")...)
	}
	if details := strings.TrimSpace(item.Details); details != "" {
		lines = append(lines, m.styles.Muted.Render("Context:"))
		lines = append(lines, wrapIndented(details, maxWidth, m.styles.Muted, "  ")...)
	}
	if snippet := strings.TrimSpace(item.Snippet); snippet != "" {
		lines = append(lines, m.styles.Muted.Render("Snippet:"))
		for _, raw := range strings.Split(snippet, "\n") {
			line := raw
			if maxWidth > 0 {
				line = truncateText(line, maxWidth-2)
			}
			lines = append(lines, m.styles.Muted.Render("  "+line))
		}
	}
	return lines
}

func (m model) approvalsForWorkspace(id string) []approvalItem {
	if strings.TrimSpace(id) == "" || m.approvalsByWorkspace == nil {
		return nil
	}
	return m.approvalsByWorkspace[id]
}

func (m model) bulkApprovalConfirmLine() string {
	count := len(m.approvalsBulkTargets)
	action := "approve"
	if m.approvalsBulkAction == models.ApprovalStatusDenied {
		action = "deny"
	}
	if count == 1 {
		return m.styles.Warning.Render(fmt.Sprintf("Confirm %s 1 approval? y: confirm | n: cancel", action))
	}
	return m.styles.Warning.Render(fmt.Sprintf("Confirm %s %d approvals? y: confirm | n: cancel", action, count))
}

func approvalSelectionIndex(idx, count int) int {
	if count <= 0 {
		return -1
	}
	if idx < 0 {
		return 0
	}
	if idx >= count {
		return count - 1
	}
	return idx
}

func approvalSummary(item approvalItem) string {
	agent := defaultLabel(item.Agent)
	summary := strings.TrimSpace(item.Summary)
	if summary == "" {
		summary = "Approval requested"
	}
	return fmt.Sprintf("%s — %s", agent, summary)
}

func approvalStatusBadge(style styles.Styles, status models.ApprovalStatus) string {
	switch status {
	case models.ApprovalStatusApproved:
		return style.Success.Render("[OK]")
	case models.ApprovalStatusDenied:
		return style.Error.Render("[DENY]")
	case models.ApprovalStatusExpired:
		return style.Muted.Render("[EXP]")
	default:
		return style.Warning.Render("[PEND]")
	}
}

func approvalStatusLabel(style styles.Styles, status models.ApprovalStatus) string {
	switch status {
	case models.ApprovalStatusApproved:
		return style.Success.Render("approved")
	case models.ApprovalStatusDenied:
		return style.Error.Render("denied")
	case models.ApprovalStatusExpired:
		return style.Muted.Render("expired")
	default:
		return style.Warning.Render("pending")
	}
}

func approvalRiskBadge(style styles.Styles, risk string) string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "high":
		return style.Error.Render("[HIGH]")
	case "medium":
		return style.Warning.Render("[MED]")
	case "low":
		return style.Success.Render("[LOW]")
	default:
		return style.Muted.Render("[--]")
	}
}

func approvalRiskLabel(style styles.Styles, risk string) string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "high":
		return style.Error.Render("high")
	case "medium":
		return style.Warning.Render("medium")
	case "low":
		return style.Success.Render("low")
	default:
		return style.Muted.Render("unknown")
	}
}

func countApprovalStatus(items []approvalItem, status models.ApprovalStatus) int {
	count := 0
	for _, item := range items {
		if item.Status == status {
			count++
		}
	}
	return count
}

func (m *model) approvalSelectionMap(wsID string) map[string]bool {
	if strings.TrimSpace(wsID) == "" {
		return nil
	}
	if m.approvalsMarked == nil {
		m.approvalsMarked = make(map[string]map[string]bool)
	}
	if m.approvalsMarked[wsID] == nil {
		m.approvalsMarked[wsID] = make(map[string]bool)
	}
	return m.approvalsMarked[wsID]
}

func (m *model) isApprovalMarked(wsID, approvalID string) bool {
	if m.approvalsMarked == nil {
		return false
	}
	if m.approvalsMarked[wsID] == nil {
		return false
	}
	return m.approvalsMarked[wsID][approvalID]
}

func (m *model) countMarkedApprovals(wsID string, items []approvalItem) int {
	if len(items) == 0 {
		return 0
	}
	if m.approvalsMarked == nil {
		return 0
	}
	marked := m.approvalsMarked[wsID]
	if len(marked) == 0 {
		return 0
	}
	count := 0
	for _, item := range items {
		if marked[item.ID] {
			count++
		}
	}
	return count
}

func (m *model) toggleApprovalMark(wsID string, item approvalItem) bool {
	if item.Status != models.ApprovalStatusPending {
		m.setStatus("Only pending approvals can be selected.", statusWarn)
		return false
	}
	marked := m.approvalSelectionMap(wsID)
	if marked == nil {
		return false
	}
	if marked[item.ID] {
		delete(marked, item.ID)
		return false
	}
	marked[item.ID] = true
	return true
}

func (m *model) clearApprovalMarks(wsID string) {
	if m.approvalsMarked == nil {
		return
	}
	delete(m.approvalsMarked, wsID)
}

func pendingApprovalIDs(items []approvalItem) []string {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.Status == models.ApprovalStatusPending {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func (m *model) bulkApprovalTargets(wsID string, items []approvalItem) []string {
	if m.approvalsMarked != nil {
		if marked := m.approvalsMarked[wsID]; len(marked) > 0 {
			ids := make([]string, 0, len(marked))
			for _, item := range items {
				if marked[item.ID] {
					ids = append(ids, item.ID)
				}
			}
			return ids
		}
	}
	return pendingApprovalIDs(items)
}

func (m *model) toggleAllPendingApprovals(wsID string, items []approvalItem) {
	if strings.TrimSpace(wsID) == "" || len(items) == 0 {
		m.setStatus("No approvals to select.", statusWarn)
		return
	}
	pendingIDs := pendingApprovalIDs(items)
	if len(pendingIDs) == 0 {
		m.setStatus("No pending approvals to select.", statusWarn)
		return
	}
	marked := m.approvalSelectionMap(wsID)
	if marked == nil {
		return
	}
	allSelected := true
	for _, id := range pendingIDs {
		if !marked[id] {
			allSelected = false
			break
		}
	}
	if allSelected {
		for _, id := range pendingIDs {
			delete(marked, id)
		}
		m.setStatus("Cleared approval selection.", statusInfo)
		return
	}
	for _, id := range pendingIDs {
		marked[id] = true
	}
	m.setStatus("Selected all pending approvals.", statusInfo)
}

func (m *model) startBulkApproval(status models.ApprovalStatus) {
	items, wsID := m.approvalsForSelectedWorkspace()
	if wsID == "" || len(items) == 0 {
		m.setStatus("No approvals to update.", statusWarn)
		return
	}
	targets := m.bulkApprovalTargets(wsID, items)
	if len(targets) == 0 {
		m.setStatus("No pending approvals to update.", statusWarn)
		return
	}
	m.approvalsBulkConfirm = true
	m.approvalsBulkAction = status
	m.approvalsBulkTargets = targets
	m.approvalsBulkWorkspace = wsID
}

func (m *model) cancelBulkApproval() {
	m.approvalsBulkConfirm = false
	m.approvalsBulkAction = ""
	m.approvalsBulkTargets = nil
	m.approvalsBulkWorkspace = ""
}

func (m *model) updateBulkApprovalConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		m.applyBulkApproval()
		return m, nil
	case "n", "N", "esc":
		action := "Bulk approval"
		if m.approvalsBulkAction == models.ApprovalStatusDenied {
			action = "Bulk denial"
		}
		m.cancelBulkApproval()
		m.setStatus(fmt.Sprintf("%s canceled.", action), statusInfo)
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m *model) applyBulkApproval() {
	wsID := strings.TrimSpace(m.approvalsBulkWorkspace)
	if wsID == "" {
		m.cancelBulkApproval()
		return
	}
	items := m.approvalsForWorkspace(wsID)
	if len(items) == 0 {
		m.cancelBulkApproval()
		m.setStatus("No approvals to update.", statusWarn)
		return
	}
	targetSet := make(map[string]struct{}, len(m.approvalsBulkTargets))
	for _, id := range m.approvalsBulkTargets {
		if strings.TrimSpace(id) != "" {
			targetSet[id] = struct{}{}
		}
	}
	updated := 0
	for i := range items {
		if _, ok := targetSet[items[i].ID]; !ok {
			continue
		}
		if items[i].Status != models.ApprovalStatusPending {
			continue
		}
		items[i].Status = m.approvalsBulkAction
		updated++
	}
	if m.approvalsByWorkspace == nil {
		m.approvalsByWorkspace = make(map[string][]approvalItem)
	}
	m.approvalsByWorkspace[wsID] = items
	m.clearApprovalMarks(wsID)
	action := "Approved"
	if m.approvalsBulkAction == models.ApprovalStatusDenied {
		action = "Denied"
	}
	if updated == 0 {
		m.setStatus("No pending approvals updated.", statusWarn)
	} else {
		m.setStatus(fmt.Sprintf("%s %d approvals.", action, updated), statusInfo)
	}
	m.cancelBulkApproval()
}

func wrapIndented(text string, maxWidth int, style lipgloss.Style, indent string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	contentWidth := maxWidth
	if contentWidth > 0 && len(indent) < contentWidth {
		contentWidth = maxWidth - len(indent)
	}
	lines := wrapText(text, contentWidth)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, style.Render(indent+line))
	}
	return out
}

func (m model) selectedAgentCard() *components.AgentCard {
	cards := m.agentCards()
	if len(cards) == 0 {
		return nil
	}
	if strings.TrimSpace(m.selectedAgent) != "" {
		if card := findAgentCardByName(cards, m.selectedAgent); card != nil {
			return card
		}
	}
	if m.selectedAgentIndex >= 0 && m.selectedAgentIndex < len(cards) {
		return &cards[m.selectedAgentIndex]
	}
	return &cards[0]
}

func (m model) selectedAgentIndexFor(cards []components.AgentCard) int {
	if len(cards) == 0 {
		return -1
	}
	if name := strings.TrimSpace(m.selectedAgent); name != "" {
		for i := range cards {
			if strings.EqualFold(cards[i].Name, name) {
				return i
			}
		}
	}
	if m.selectedAgentIndex >= 0 && m.selectedAgentIndex < len(cards) {
		return m.selectedAgentIndex
	}
	return 0
}

func (m *model) syncAgentSelection() {
	cards := m.agentCards()
	m.ensureAgentSelection(cards)
}

func (m *model) recordWorkspaceActivity(agentID string, timestamp time.Time) {
	if timestamp.IsZero() {
		return
	}
	wsID := m.workspaceIDForAgent(agentID)
	if wsID == "" {
		return
	}
	if m.workspaceRecentEvents == nil {
		m.workspaceRecentEvents = make(map[string][]time.Time)
	}
	events := m.workspaceRecentEvents[wsID]
	events = append(events, timestamp)
	if len(events) > maxWorkspaceEvents {
		events = events[len(events)-maxWorkspaceEvents:]
	}
	m.workspaceRecentEvents[wsID] = events
}

func (m *model) workspaceIDForAgent(agentID string) string {
	if m.agentWorkspaces == nil {
		m.agentWorkspaces = make(map[string]string)
	}
	if wsID := m.agentWorkspaces[agentID]; wsID != "" {
		return wsID
	}
	if m.stateEngine == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	agent, err := m.stateEngine.GetAgent(ctx, agentID)
	if err != nil || agent == nil {
		return ""
	}
	wsID := strings.TrimSpace(agent.WorkspaceID)
	if wsID == "" {
		return ""
	}
	m.agentWorkspaces[agentID] = wsID
	return wsID
}

func (m *model) ensureAgentSelection(cards []components.AgentCard) {
	if len(cards) == 0 {
		m.selectedAgent = ""
		m.selectedAgentIndex = -1
		return
	}
	if name := strings.TrimSpace(m.selectedAgent); name != "" {
		for i := range cards {
			if strings.EqualFold(cards[i].Name, name) {
				m.selectedAgentIndex = i
				return
			}
		}
	}
	idx := m.selectedAgentIndex
	if idx < 0 {
		idx = 0
	} else if idx >= len(cards) {
		idx = len(cards) - 1
	}
	m.selectedAgentIndex = idx
	m.selectedAgent = cards[idx].Name
}

func (m *model) moveAgentSelection(delta int) {
	cards := m.agentCards()
	if len(cards) == 0 {
		m.selectedAgent = ""
		m.selectedAgentIndex = -1
		return
	}
	m.ensureAgentSelection(cards)
	next := m.selectedAgentIndex + delta
	if next < 0 {
		next = 0
	} else if next >= len(cards) {
		next = len(cards) - 1
	}
	if next == m.selectedAgentIndex {
		return
	}
	m.selectedAgentIndex = next
	m.selectedAgent = cards[next].Name
}

func (m model) filteredAuditItems() []auditItem {
	return filterAuditItems(m.auditItems, m.auditFilter)
}

func (m *model) ensureAuditSelection(items []auditItem) {
	if len(items) == 0 {
		m.auditSelected = -1
		return
	}
	idx := m.auditSelected
	if idx < 0 {
		idx = 0
	} else if idx >= len(items) {
		idx = len(items) - 1
	}
	m.auditSelected = idx
}

func (m *model) moveAuditSelection(delta int) {
	items := m.filteredAuditItems()
	if len(items) == 0 {
		m.auditSelected = -1
		return
	}
	m.ensureAuditSelection(items)
	next := m.auditSelected + delta
	if next < 0 {
		next = 0
	} else if next >= len(items) {
		next = len(items) - 1
	}
	m.auditSelected = next
}

func (m model) auditSelectedIndexFor(items []auditItem) int {
	if len(items) == 0 {
		return -1
	}
	idx := m.auditSelected
	if idx < 0 {
		return 0
	}
	if idx >= len(items) {
		return len(items) - 1
	}
	return idx
}

func (m model) selectedAuditItem() (auditItem, bool) {
	items := m.filteredAuditItems()
	if len(items) == 0 {
		return auditItem{}, false
	}
	idx := m.auditSelectedIndexFor(items)
	return items[idx], true
}

func (m model) filteredMailThreads() []mailThread {
	return filterMailThreads(m.mailThreads, m.mailFilter)
}

func (m *model) ensureMailSelection(threads []mailThread) {
	if len(threads) == 0 {
		m.mailSelected = -1
		return
	}
	idx := m.mailSelected
	if idx < 0 {
		idx = 0
	} else if idx >= len(threads) {
		idx = len(threads) - 1
	}
	m.mailSelected = idx
}

func (m *model) moveMailSelection(delta int) {
	threads := m.filteredMailThreads()
	if len(threads) == 0 {
		m.mailSelected = -1
		return
	}
	m.ensureMailSelection(threads)
	next := m.mailSelected + delta
	if next < 0 {
		next = 0
	} else if next >= len(threads) {
		next = len(threads) - 1
	}
	m.mailSelected = next
}

func (m model) mailSelectedIndexFor(threads []mailThread) int {
	if len(threads) == 0 {
		return -1
	}
	idx := m.mailSelected
	if idx < 0 {
		return 0
	}
	if idx >= len(threads) {
		return len(threads) - 1
	}
	return idx
}

func (m model) selectedMailThread() (mailThread, bool) {
	threads := m.filteredMailThreads()
	if len(threads) == 0 {
		return mailThread{}, false
	}
	idx := m.mailSelectedIndexFor(threads)
	return threads[idx], true
}

func (m *model) restoreMailSelection(threadID string) {
	if strings.TrimSpace(threadID) == "" {
		return
	}
	threads := m.filteredMailThreads()
	for i, thread := range threads {
		if thread.ID == threadID {
			m.mailSelected = i
			return
		}
	}
}

func (m *model) markMailThreadRead(threadID string) {
	if strings.TrimSpace(threadID) == "" {
		return
	}
	if m.mailRead == nil {
		m.mailRead = make(map[string]bool)
	}
	for i := range m.mailThreads {
		if m.mailThreads[i].ID != threadID {
			continue
		}
		for j := range m.mailThreads[i].Messages {
			msg := &m.mailThreads[i].Messages[j]
			msg.Read = true
			m.mailRead[msg.ID] = true
		}
	}
}

func (m model) agentInspectorLines() []string {
	if m.queueEditorOpen {
		return m.queueEditorLines()
	}
	cards := m.agentCards()
	if len(cards) == 0 {
		if strings.TrimSpace(m.agentFilter) != "" {
			return []string{
				m.styles.Warning.Render("No agents match filter."),
				m.styles.Muted.Render("Press / to edit filter."),
			}
		}
		return []string{m.styles.Muted.Render("No agents available.")}
	}
	card := m.selectedAgentCard()
	if card == nil {
		return []string{m.styles.Muted.Render("No agents available.")}
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
	if line := cooldownStatusLine(m.styles, card.CooldownUntil); line != "" {
		lines = append(lines, line)
	}
	lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("Last: %s", formatActivityTime(card.LastActivity))))
	if line := m.actionProgressLine(); line != "" {
		lines = append(lines, line)
	}
	lines = append(lines, m.styles.Muted.Render("Actions: [I] Interrupt | [R] Restart | [E] Export logs"))

	return lines
}

func (m model) auditInspectorLines() []string {
	item, ok := m.selectedAuditItem()
	if !ok {
		if strings.TrimSpace(m.auditFilter) != "" {
			return []string{
				m.styles.Warning.Render("No audit events match filter."),
				m.styles.Muted.Render("Press / to edit filter."),
			}
		}
		return []string{m.styles.Muted.Render("No audit events available.")}
	}

	maxWidth := m.inspectorContentWidth()
	timestamp := item.Timestamp.Local().Format("2006-01-02 15:04:05")
	lines := []string{
		m.styles.Text.Render("Event"),
		m.styles.Muted.Render(fmt.Sprintf("Time: %s", timestamp)),
		m.styles.Muted.Render(fmt.Sprintf("Type: %s", defaultLabel(string(item.Type)))),
		m.styles.Muted.Render(fmt.Sprintf("Entity: %s", defaultLabel(string(item.EntityType)))),
		m.styles.Muted.Render(fmt.Sprintf("Entity ID: %s", defaultLabel(item.EntityID))),
		m.styles.Muted.Render(fmt.Sprintf("Event ID: %s", defaultLabel(item.ID))),
	}

	if summary := strings.TrimSpace(item.Summary); summary != "" {
		lines = append(lines, "", m.styles.Text.Render("Summary"))
		lines = append(lines, wrapIndented(summary, maxWidth, m.styles.Muted, "  ")...)
	}
	if detail := strings.TrimSpace(item.Detail); detail != "" {
		lines = append(lines, "", m.styles.Text.Render("Detail"))
		lines = append(lines, wrapIndented(detail, maxWidth, m.styles.Muted, "  ")...)
	}

	return lines
}

func (m model) mailboxInspectorLines() []string {
	thread, ok := m.selectedMailThread()
	if !ok {
		if strings.TrimSpace(m.mailFilter) != "" {
			return []string{
				m.styles.Warning.Render("No mailbox threads match filter."),
				m.styles.Muted.Render("Press / to edit filter."),
			}
		}
		return []string{m.styles.Muted.Render("No mailbox threads available.")}
	}

	maxWidth := m.inspectorContentWidth()
	unread := mailThreadUnreadCount(thread)
	unreadLabel := fmt.Sprintf("%d unread", unread)
	if unread == 0 {
		unreadLabel = "All read"
	}

	lines := []string{
		m.styles.Text.Render("Thread"),
		m.styles.Muted.Render(fmt.Sprintf("Subject: %s", defaultLabel(thread.Subject))),
		m.styles.Muted.Render(fmt.Sprintf("Messages: %d", len(thread.Messages))),
		m.styles.Muted.Render(fmt.Sprintf("Status: %s", unreadLabel)),
	}

	lines = append(lines, "")
	lines = append(lines, m.styles.Text.Render("Messages"))
	for _, msg := range thread.Messages {
		lines = append(lines, m.mailMessageLines(msg, maxWidth)...)
	}

	return lines
}

func (m model) mailMessageLines(msg mailMessage, maxWidth int) []string {
	status := "read"
	if !msg.Read {
		status = "unread"
	}
	header := fmt.Sprintf("[%s] %s %s", status, msg.CreatedAt.Local().Format("15:04"), defaultLabel(msg.From))
	lines := []string{m.styles.Muted.Render(header)}
	body := strings.TrimSpace(msg.Body)
	if body == "" {
		return lines
	}
	lines = append(lines, wrapIndented(body, maxWidth, m.styles.Muted, "  ")...)
	return lines
}

func mailThreadLastMessage(thread mailThread) *mailMessage {
	if len(thread.Messages) == 0 {
		return nil
	}
	lastIdx := 0
	for i := 1; i < len(thread.Messages); i++ {
		if thread.Messages[i].CreatedAt.After(thread.Messages[lastIdx].CreatedAt) {
			lastIdx = i
		}
	}
	return &thread.Messages[lastIdx]
}

func mailThreadUnreadCount(thread mailThread) int {
	unread := 0
	for _, msg := range thread.Messages {
		if !msg.Read {
			unread++
		}
	}
	return unread
}

func (m model) queueEditorLines() []string {
	card := m.selectedAgentCard()
	agent := "--"
	if card != nil {
		agent = card.Name
	}

	lines := []string{
		m.styles.Text.Render("Queue editor"),
		m.styles.Muted.Render("↑↓/jk: move | J/K or Ctrl+↑/↓: reorder | e: edit | d: delete"),
		m.styles.Muted.Render("m: add msg | p: add pause | c: add conditional | esc: close"),
		m.styles.Muted.Render(fmt.Sprintf("Agent: %s", agent)),
	}

	if card == nil {
		lines = append(lines, m.styles.Warning.Render("No agent selected."))
		return lines
	}

	state := m.queueEditorStateForAgent(agent)
	if state == nil || len(state.Items) == 0 {
		lines = append(lines, m.styles.Muted.Render("Queue is empty. Press m/p to add items."))
		if m.queueEditOpen {
			lines = append(lines, m.queueEditLines()...)
		}
		return lines
	}

	maxWidth := m.inspectorContentWidth()
	for i, item := range state.Items {
		prefix := "  "
		if i == state.Selected {
			prefix = "> "
		}
		label := fmt.Sprintf("[%s] %s", queueItemTypeLabel(item.Kind), queueItemSummary(item))
		if item.Status != "" {
			label = fmt.Sprintf("[%s] %s (%s)", queueItemTypeLabel(item.Kind), queueItemSummary(item), item.Status)
		}
		line := prefix + label
		if maxWidth > 0 {
			line = truncateText(line, maxWidth)
		}
		if i == state.Selected {
			lines = append(lines, m.styles.Accent.Render(line))
		} else {
			lines = append(lines, m.styles.Text.Render(line))
		}
	}

	if m.queueEditOpen {
		lines = append(lines, m.queueEditLines()...)
	}

	return lines
}

func (m model) queueEditorStateForAgent(agent string) *queueEditorState {
	if strings.TrimSpace(agent) == "" || m.queueEditors == nil {
		return nil
	}
	return m.queueEditors[agent]
}

func (m model) queueEditLines() []string {
	buffer := strings.TrimSpace(m.queueEditBuffer)
	if buffer == "" {
		buffer = "..."
	}
	label := "Edit"
	if m.queueEditMode == queueEditModeAdd {
		label = "Add"
	}
	prompt := strings.TrimSpace(m.queueEditPrompt)
	if prompt == "" {
		prompt = label
	} else {
		prompt = fmt.Sprintf("%s: %s", label, prompt)
	}
	return []string{
		m.styles.Info.Render(fmt.Sprintf("%s: %s", prompt, buffer)),
		m.styles.Muted.Render("enter: save | esc: cancel"),
	}
}

func queueItemSummary(item queueItem) string {
	summary := strings.TrimSpace(item.Summary)
	if summary == "" {
		return "--"
	}
	return summary
}

func queueItemTypeLabel(kind models.QueueItemType) string {
	switch kind {
	case models.QueueItemTypeMessage:
		return "MSG"
	case models.QueueItemTypePause:
		return "PAUSE"
	case models.QueueItemTypeConditional:
		return "COND"
	default:
		return strings.ToUpper(string(kind))
	}
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

func cooldownStatusLine(style styles.Styles, until *time.Time) string {
	if until == nil || until.IsZero() {
		return ""
	}
	remaining := time.Until(*until)
	if remaining <= 0 {
		return style.Muted.Render("Cooldown: expired")
	}
	return style.Warning.Render(fmt.Sprintf("Cooldown: %s", formatCooldownDuration(remaining)))
}

func formatCooldownDuration(value time.Duration) string {
	if value < time.Second {
		return "<1s"
	}
	if value < time.Minute {
		return value.Round(time.Second).String()
	}
	if value < time.Hour {
		rounded := value.Round(time.Second)
		minutes := int(rounded.Minutes())
		seconds := int(rounded.Seconds()) % 60
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return value.Round(time.Minute).String()
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
	panels := lipgloss.JoinHorizontal(lipgloss.Top, nodesPanel, spacer, activityPanel)
	if line := m.refreshIndicatorLine(); line != "" {
		return joinLines([]string{line, "", panels})
	}
	return panels
}

func (m model) auditViewLines() []string {
	mainWidth := m.width
	if m.showInspector {
		if width, _, _, _ := m.inspectorLayout(); width > 0 {
			mainWidth = width
		}
	}
	if mainWidth <= 0 {
		mainWidth = 80
	}

	lines := []string{
		m.styles.Accent.Render("Audit log"),
		m.styles.Muted.Render("↑↓/jk: move | Enter: inspect | /: filter | Ctrl+L: clear"),
	}
	if line := m.searchLine(viewAudit); line != "" {
		lines = append(lines, line)
	}
	lines = append(lines, m.auditSearchStatusLines()...)
	lines = append(lines, "")

	items := m.filteredAuditItems()
	if len(items) == 0 {
		if strings.TrimSpace(m.auditFilter) != "" {
			lines = append(lines, m.styles.Warning.Render("No audit events match this filter."))
			lines = append(lines, m.styles.Muted.Render("Press / to edit or clear the filter."))
		} else {
			lines = append(lines, m.styles.Muted.Render("No audit events recorded yet."))
		}
		return m.renderWithInspector(lines, "Audit inspector", m.auditInspectorLines())
	}

	header := fmt.Sprintf("%-19s  %-18s  %-16s  %s", "Time", "Type", "Entity", "Summary")
	lines = append(lines, m.styles.Muted.Render(truncateText(header, mainWidth)))

	selectedIdx := m.auditSelectedIndexFor(items)
	for i, item := range items {
		line := m.auditRowLine(item)
		prefix := "  "
		if i == selectedIdx {
			prefix = "> "
		}
		line = prefix + line
		if mainWidth > 0 {
			line = truncateText(line, mainWidth)
		}
		if i == selectedIdx {
			lines = append(lines, m.styles.Accent.Render(line))
		} else {
			lines = append(lines, m.styles.Text.Render(line))
		}
	}

	return m.renderWithInspector(lines, "Audit inspector", m.auditInspectorLines())
}

func (m model) auditRowLine(item auditItem) string {
	timestamp := item.Timestamp.Local().Format("2006-01-02 15:04:05")
	eventType := truncateText(string(item.Type), 18)
	entity := fmt.Sprintf("%s:%s", defaultLabel(string(item.EntityType)), defaultLabel(item.EntityID))
	entity = truncateText(entity, 16)
	summary := strings.TrimSpace(item.Summary)
	if summary == "" {
		summary = strings.TrimSpace(item.Detail)
	}
	if summary == "" {
		summary = "Event recorded"
	}
	return fmt.Sprintf("%-19s  %-18s  %-16s  %s", timestamp, eventType, entity, summary)
}

func (m model) mailboxViewLines() []string {
	mainWidth := m.width
	if m.showInspector {
		if width, _, _, _ := m.inspectorLayout(); width > 0 {
			mainWidth = width
		}
	}
	if mainWidth <= 0 {
		mainWidth = 80
	}

	lines := []string{
		m.styles.Accent.Render("Mailbox"),
		m.styles.Muted.Render("↑↓/jk: move | Enter: inspect | /: filter | Ctrl+L: clear"),
	}
	if line := m.searchLine(viewMailbox); line != "" {
		lines = append(lines, line)
	}
	lines = append(lines, m.mailSearchStatusLines()...)
	lines = append(lines, m.mailboxStatusLines(mainWidth)...)
	lines = append(lines, "")

	threads := m.filteredMailThreads()
	if len(threads) == 0 {
		if strings.TrimSpace(m.mailFilter) != "" {
			lines = append(lines, m.styles.Warning.Render("No mailbox threads match this filter."))
			lines = append(lines, m.styles.Muted.Render("Press / to edit or clear the filter."))
		} else {
			lines = append(lines, m.styles.Muted.Render("No mailbox messages yet."))
		}
		return m.renderWithInspector(lines, "Mailbox inspector", m.mailboxInspectorLines())
	}

	header := fmt.Sprintf("%-5s  %-9s  %-12s  %s", "Time", "Unread", "From", "Subject")
	lines = append(lines, m.styles.Muted.Render(truncateText(header, mainWidth)))

	selectedIdx := m.mailSelectedIndexFor(threads)
	for i, thread := range threads {
		line := m.mailThreadLine(thread)
		prefix := "  "
		if i == selectedIdx {
			prefix = "> "
		}
		line = prefix + line
		if mainWidth > 0 {
			line = truncateText(line, mainWidth)
		}
		if i == selectedIdx {
			lines = append(lines, m.styles.Accent.Render(line))
		} else {
			lines = append(lines, m.styles.Text.Render(line))
		}
	}

	return m.renderWithInspector(lines, "Mailbox inspector", m.mailboxInspectorLines())
}

func (m model) mailThreadLine(thread mailThread) string {
	last := mailThreadLastMessage(thread)
	timeLabel := "--"
	from := "--"
	if last != nil {
		timeLabel = last.CreatedAt.Local().Format("15:04")
		from = last.From
	}
	unread := mailThreadUnreadCount(thread)
	unreadLabel := "0"
	if unread > 0 {
		unreadLabel = fmt.Sprintf("%d", unread)
	}
	unreadLabel = truncateText(unreadLabel, 9)
	from = truncateText(from, 12)
	subject := defaultLabel(thread.Subject)
	return fmt.Sprintf("%-5s  %-9s  %-12s  %s", timeLabel, unreadLabel, from, subject)
}

// agentDetailView renders a full-screen agent detail view.
func (m model) agentDetailView() []string {
	card := m.selectedAgentCard()
	if card == nil {
		return []string{m.styles.Warning.Render("No agent selected. Press Esc to go back.")}
	}

	lines := []string{}

	// Header with agent name and back hint
	header := m.styles.Title.Render(fmt.Sprintf("Agent: %s", card.Name))
	backHint := m.styles.Muted.Render("Esc: back | p: profile | I: interrupt | R: restart | P: pause")
	lines = append(lines, header, backHint)
	if line := m.actionConfirmLine(); line != "" {
		lines = append(lines, line)
	}
	if line := m.actionProgressLine(); line != "" {
		lines = append(lines, line)
	}
	lines = append(lines, "")

	// Calculate layout widths
	leftWidth := m.width * 2 / 3
	rightWidth := m.width - leftWidth - 2
	if leftWidth < 40 {
		leftWidth = 40
	}
	if rightWidth < 30 {
		rightWidth = 30
	}

	// Left panel: Transcript
	transcriptPanel := m.renderAgentDetailTranscript(leftWidth)

	// Right panel: Profile + Usage + Queue + Activity
	profilePanel := m.renderAgentDetailProfile(card, rightWidth)
	usagePanel := m.renderAgentDetailUsage(card, rightWidth)
	queuePanel := m.renderAgentDetailQueue(card, rightWidth)
	activityPanel := m.renderAgentDetailActivity(card, rightWidth)
	rightPanels := joinLines([]string{profilePanel, "", usagePanel, "", queuePanel, "", activityPanel})

	// Join panels horizontally
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, transcriptPanel, "  ", rightPanels)
	lines = append(lines, mainContent)

	return lines
}

// renderAgentDetailTranscript renders the transcript panel for agent detail view.
func (m model) renderAgentDetailTranscript(width int) string {
	if m.transcriptViewer == nil {
		return m.renderPanel("Transcript", width, []string{
			m.styles.Muted.Render("No transcript available."),
		})
	}

	m.transcriptViewer.Width = width - 4
	m.transcriptViewer.Height = m.height - 8
	content := m.transcriptViewer.Render(m.styles)

	// Wrap in a panel
	panelStyle := m.styles.Panel.Copy().Width(width).Padding(0, 1)
	title := m.styles.Accent.Render("Transcript")
	if m.transcriptPreview {
		title = m.styles.Accent.Render("Transcript") + " " + m.styles.Warning.Render("(sample)")
	}

	autoHint := ""
	if m.transcriptAutoScroll {
		autoHint = m.styles.Muted.Render(" [auto-scroll on]")
	}

	return panelStyle.Render(joinLines([]string{title + autoHint, content}))
}

// renderAgentDetailProfile renders the profile panel for agent detail view.
func (m model) renderAgentDetailProfile(card *components.AgentCard, width int) string {
	lines := []string{}

	// State badge
	stateLine := fmt.Sprintf("State: %s", components.RenderAgentStateBadge(m.styles, card.State))
	lines = append(lines, stateLine)

	// Type and model
	typeLine := m.styles.Text.Render(fmt.Sprintf("Type: %s", defaultIfEmpty(string(card.Type), "unknown")))
	modelLine := m.styles.Text.Render(fmt.Sprintf("Model: %s", defaultIfEmpty(card.Model, "--")))
	lines = append(lines, typeLine, modelLine)

	// Profile
	profileLabel := defaultIfEmpty(card.Profile, "--")
	profileLine := m.styles.Muted.Render(fmt.Sprintf("Profile: %s", profileLabel))
	lines = append(lines, profileLine)
	options := m.profileOptions(card)
	if m.profileSelectOpen && m.profileSelectAgent == card.Name {
		lines = append(lines, m.styles.Muted.Render("Select profile:"))
		if len(m.profileSelectOptions) == 0 {
			lines = append(lines, m.styles.Warning.Render("No profiles available."))
		} else {
			for i, option := range m.profileSelectOptions {
				label := option
				if width > 10 {
					label = truncateText(label, width-6)
				}
				prefix := "  "
				line := fmt.Sprintf("%s%s", prefix, label)
				if i == m.profileSelectIndex {
					lines = append(lines, m.styles.Accent.Render("> "+label))
				} else {
					lines = append(lines, m.styles.Muted.Render(line))
				}
			}
			lines = append(lines, m.styles.Muted.Render("enter: confirm | esc: cancel"))
		}
	} else if len(options) > 1 {
		lines = append(lines, m.styles.Muted.Render("p: switch profile"))
	}

	// Confidence
	confidenceLabel, bars, style := agentConfidenceDescriptor(m.styles, card.Confidence)
	confidenceLine := fmt.Sprintf("Confidence: %s", style.Render(fmt.Sprintf("%s %s", confidenceLabel, bars)))
	lines = append(lines, confidenceLine)

	// Last activity
	lastActivity := "--"
	if card.LastActivity != nil {
		lastActivity = card.LastActivity.Format("15:04:05")
	}
	lastLine := m.styles.Muted.Render(fmt.Sprintf("Last activity: %s", lastActivity))
	lines = append(lines, lastLine)

	// Activity pulse
	pulse := components.NewActivityPulse(card.RecentEvents, card.State, card.LastActivity)
	activityLine := components.RenderActivityLine(m.styles, pulse)
	lines = append(lines, activityLine)

	// Reason
	if card.Reason != "" {
		reasonLine := m.styles.Muted.Render(fmt.Sprintf("Reason: %s", truncateString(card.Reason, width-12)))
		lines = append(lines, reasonLine)
	}

	// Cooldown
	if card.CooldownUntil != nil && !card.CooldownUntil.IsZero() {
		remaining := time.Until(*card.CooldownUntil)
		if remaining > 0 {
			cooldownLine := m.styles.Warning.Render(fmt.Sprintf("Cooldown: %s remaining", formatDurationShort(remaining)))
			lines = append(lines, cooldownLine)
		}
	}

	return m.renderPanel("Profile", width, lines)
}

// renderAgentDetailQueue renders the queue panel for agent detail view.
func (m model) renderAgentDetailQueue(card *components.AgentCard, width int) string {
	lines := []string{}

	// Queue length
	queueLen := card.QueueLength
	if queueLen < 0 {
		queueLen = 0
	}
	queueLine := fmt.Sprintf("Items: %d", queueLen)
	lines = append(lines, m.styles.Text.Render(queueLine))

	// Show queue items if available
	if state, _ := m.currentQueueEditorState(); state != nil && len(state.Items) > 0 {
		lines = append(lines, "")
		for i, item := range state.Items {
			if i >= 5 {
				lines = append(lines, m.styles.Muted.Render(fmt.Sprintf("  ... and %d more", len(state.Items)-5)))
				break
			}
			prefix := "  "
			if i == state.Selected {
				prefix = "> "
			}
			itemLine := fmt.Sprintf("%s%s: %s", prefix, item.ID, truncateString(item.Summary, width-20))
			lines = append(lines, m.styles.Muted.Render(itemLine))
		}
	} else {
		lines = append(lines, m.styles.Muted.Render("No queue items"))
	}

	return m.renderPanel("Queue", width, lines)
}

// renderAgentDetailActivity renders the activity panel for agent detail view.
func (m model) renderAgentDetailActivity(card *components.AgentCard, width int) string {
	lines := []string{}

	// Recent state changes for this agent
	agentChanges := []StateChangeMsg{}
	for _, change := range m.stateChanges {
		if strings.Contains(change.AgentID, card.Name) || strings.HasSuffix(card.Name, shortID(change.AgentID)) {
			agentChanges = append(agentChanges, change)
		}
	}

	if len(agentChanges) > 0 {
		for i := len(agentChanges) - 1; i >= 0 && len(lines) < 5; i-- {
			change := agentChanges[i]
			prevBadge := components.RenderAgentStateBadge(m.styles, change.PreviousState)
			currBadge := components.RenderAgentStateBadge(m.styles, change.CurrentState)
			timeLine := change.Timestamp.Format("15:04:05")
			line := fmt.Sprintf("%s: %s -> %s", timeLine, prevBadge, currBadge)
			lines = append(lines, line)
		}
	} else {
		lines = append(lines, m.styles.Muted.Render("No recent state changes"))
	}

	// Activity sparkline
	pulse := components.NewActivityPulse(card.RecentEvents, card.State, card.LastActivity)
	sparkline := components.RenderActivitySparkline(m.styles, pulse, 16)
	lines = append(lines, "", fmt.Sprintf("Activity: %s", sparkline))

	return m.renderPanel("Recent Activity", width, lines)
}

// renderAgentDetailUsage renders the usage panel for agent detail view.
func (m model) renderAgentDetailUsage(card *components.AgentCard, width int) string {
	data := components.UsagePanelData{
		Metrics:   card.UsageMetrics,
		AgentName: card.Name,
	}
	if card.UsageMetrics != nil {
		data.UpdatedAt = card.UsageMetrics.UpdatedAt
	}
	return components.RenderUsagePanel(m.styles, data, width)
}

// Helper functions for agent detail view
func defaultIfEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."
	}
	return s[:maxLen-3] + "..."
}

func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func agentConfidenceDescriptor(styleSet styles.Styles, confidence models.StateConfidence) (string, string, lipgloss.Style) {
	switch confidence {
	case models.StateConfidenceHigh:
		return "High", "###", styleSet.Success
	case models.StateConfidenceMedium:
		return "Medium", "##-", styleSet.Warning
	case models.StateConfidenceLow:
		return "Low", "#--", styleSet.Error
	default:
		return "Unknown", "---", styleSet.Muted
	}
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
		emptyState := components.EmptyNodes()
		lines = append(lines, emptyState.Render(m.styles))
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
		m.styles.Muted.Render("Audit"),
		m.styles.Muted.Render("Mailbox"),
	}

	switch m.view {
	case viewDashboard:
		parts[0] = m.styles.Accent.Render("Dashboard")
	case viewWorkspace:
		parts[1] = m.styles.Accent.Render("Workspace")
	case viewAgent, viewAgentDetail:
		parts[2] = m.styles.Accent.Render("Agent")
	case viewAudit:
		parts[3] = m.styles.Accent.Render("Audit")
	case viewMailbox:
		parts[4] = m.styles.Accent.Render("Mailbox")
	}

	return fmt.Sprintf("%s > %s > %s > %s > %s", parts[0], parts[1], parts[2], parts[3], parts[4])
}

func (m model) navLine() string {
	dash := m.navLabel(viewDashboard)
	ws := m.navLabel(viewWorkspace)
	agent := m.navLabel(viewAgent)
	audit := m.navLabel(viewAudit)
	mail := m.navLabel(viewMailbox)
	return fmt.Sprintf("Navigate: %s  %s  %s  %s  %s", dash, ws, agent, audit, mail)
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

func (m model) modeLine() string {
	if m.paletteOpen {
		return m.styles.Info.Render("Mode: Palette")
	}
	if m.searchOpen {
		return m.styles.Info.Render(fmt.Sprintf("Mode: Search (%s)", viewLabel(m.searchTarget)))
	}
	if m.transcriptSearchOpen {
		return m.styles.Info.Render("Mode: Transcript search")
	}
	if m.showTranscript && m.view == viewAgent {
		return m.styles.Info.Render("Mode: Transcript")
	}
	return ""
}

func (m model) demoDataActive() bool {
	if m.nodesPreview || m.workspacesPreview || m.transcriptPreview {
		return true
	}
	return len(m.agentStates) == 0
}

func (m model) demoBadgeLine() string {
	return m.styles.Warning.Render("Demo data (simulated)")
}

func (m model) footerLine() string {
	return m.styles.Muted.Render("Keys: ? help | ctrl+k palette | q quit")
}

func viewLabel(view viewID) string {
	switch view {
	case viewDashboard:
		return "Dashboard"
	case viewWorkspace:
		return "Workspace"
	case viewAgent:
		return "Agent"
	case viewAudit:
		return "Audit"
	case viewMailbox:
		return "Mailbox"
	case viewAgentDetail:
		return "Agent Detail"
	default:
		return "Unknown"
	}
}

func (m model) helpLines() []string {
	lines := []string{m.styles.Accent.Render("Help")}
	if m.paletteOpen {
		lines = append(lines, m.styles.Muted.Render("Palette: type to filter | Enter run | Esc close | ! show disabled"))
		return lines
	}
	if m.searchOpen {
		lines = append(lines, m.styles.Muted.Render(
			fmt.Sprintf("Search (%s): type to filter | Enter apply | Esc cancel | Ctrl+L clear", viewLabel(m.searchTarget)),
		))
		return lines
	}
	if m.transcriptSearchOpen {
		lines = append(lines, m.styles.Muted.Render("Transcript search: type to filter | Enter apply | Esc cancel"))
	}

	switch m.view {
	case viewDashboard:
		lines = append(lines, m.styles.Muted.Render("Views: 1/2/3/4/5 switch | g cycle"))
	case viewWorkspace:
		lines = append(lines, m.styles.Muted.Render("Workspace: ↑↓←→/hjkl move | Enter open | / filter | Ctrl+L clear | A approvals"))
	case viewAgent:
		lines = append(lines, m.styles.Muted.Render("Agents: ↑↓←→/hjkl select | / filter | Ctrl+L clear | t transcript | Q queue"))
		if m.showTranscript {
			autoHint := "a auto-scroll on"
			if !m.transcriptAutoScroll {
				autoHint = "a auto-scroll off"
			}
			lines = append(lines, m.styles.Muted.Render(
				fmt.Sprintf("Transcript: jk/↑↓ scroll | g/G top/bottom | n/N next/prev | / search | %s", autoHint),
			))
		}
		lines = append(lines, m.styles.Muted.Render("Actions: I interrupt | R restart | E export"))
	case viewAudit:
		lines = append(lines, m.styles.Muted.Render("Audit: ↑↓/jk move | Enter inspect | / filter | Ctrl+L clear"))
	case viewMailbox:
		lines = append(lines, m.styles.Muted.Render("Mailbox: ↑↓/jk move | Enter inspect | / filter | Ctrl+L clear"))
	case viewAgentDetail:
		lines = append(lines, m.styles.Muted.Render("Agent detail: p profile | I interrupt | R restart | P pause | Esc back"))
	}

	lines = append(lines, m.styles.Muted.Render("Global: ctrl+k palette | i/tab inspector | ? help | q quit"))
	return lines
}

type paletteAction struct {
	ID             string
	Label          string
	Hint           string
	Views          []viewID
	Disabled       bool
	DisabledReason string
}

func (m *model) openPalette() {
	m.paletteOpen = true
	m.paletteQuery = ""
	m.paletteIndex = 0
	m.resetPaletteIndex()
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
		m.styles.Muted.Render("Prefix ! to include disabled actions."),
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
		if action.Disabled {
			reason := action.DisabledReason
			if strings.TrimSpace(reason) == "" {
				reason = "Coming soon"
			}
			label = fmt.Sprintf("%s — %s", label, reason)
		}
		if i == m.paletteIndex && !action.Disabled {
			lines = append(lines, m.styles.Accent.Render(prefix+label))
			continue
		}
		lines = append(lines, m.styles.Muted.Render(prefix+label))
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

	hasAgent := m.selectedAgentCard() != nil

	actions := []paletteAction{
		{ID: "view.dashboard", Label: "Navigate to dashboard", Hint: "1"},
		{ID: "view.workspace", Label: "Navigate to workspace", Hint: "2"},
		{ID: "view.agent", Label: "Navigate to agents", Hint: "3"},
		{ID: "view.audit", Label: "Navigate to audit log", Hint: "4"},
		{ID: "view.mailbox", Label: "Navigate to mailbox", Hint: "5"},
		{
			ID:             "agent.spawn",
			Label:          "Spawn agent",
			Hint:           "workspace",
			Views:          []viewID{viewWorkspace},
			Disabled:       true,
			DisabledReason: "Coming soon",
		},
		{
			ID:       "agent.pause",
			Label:    "Pause agent",
			Hint:     "P",
			Views:    []viewID{viewAgent},
			Disabled: !hasAgent,
		},
		{
			ID:       "agent.resume",
			Label:    "Resume agent",
			Views:    []viewID{viewAgent},
			Disabled: !hasAgent,
		},
		{
			ID:       "agent.restart",
			Label:    "Restart agent",
			Hint:     "R",
			Views:    []viewID{viewAgent},
			Disabled: !hasAgent,
		},
		{
			ID:       "agent.interrupt",
			Label:    "Interrupt agent",
			Hint:     "I",
			Views:    []viewID{viewAgent},
			Disabled: !hasAgent,
		},
		{
			ID:       "agent.export_logs",
			Label:    "Export logs",
			Hint:     "E",
			Views:    []viewID{viewAgent},
			Disabled: !hasAgent,
		},
	}

	if m.pausedAll {
		actions = append(actions, paletteAction{ID: "agents.resume_all", Label: "Resume all agents"})
	} else {
		actions = append(actions, paletteAction{ID: "agents.pause_all", Label: "Pause all agents"})
	}

	actions = append(actions,
		paletteAction{ID: "refresh", Label: "Refresh", Hint: "r"},
		paletteAction{ID: "settings", Label: "Settings", Disabled: true, DisabledReason: "Coming soon"},
		paletteAction{ID: helpID, Label: helpLabel, Hint: "?"},
		paletteAction{ID: "quit", Label: "Quit", Hint: "q"},
	)

	return actions
}

func (m model) filteredPaletteActions() []paletteAction {
	rawQuery := strings.TrimSpace(m.paletteQuery)
	includeDisabled := strings.HasPrefix(rawQuery, "!")
	if includeDisabled {
		rawQuery = strings.TrimSpace(strings.TrimPrefix(rawQuery, "!"))
	}
	query := strings.TrimSpace(strings.ToLower(rawQuery))
	actions := m.paletteActions()
	actions = filterPaletteActionsForView(actions, m.view)
	if !includeDisabled {
		enabled := make([]paletteAction, 0, len(actions))
		for _, action := range actions {
			if action.Disabled {
				continue
			}
			enabled = append(enabled, action)
		}
		actions = enabled
	}
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

func firstSelectableIndex(actions []paletteAction) int {
	for i, action := range actions {
		if !action.Disabled {
			return i
		}
	}
	return -1
}

func (m *model) resetPaletteIndex() {
	actions := m.filteredPaletteActions()
	idx := firstSelectableIndex(actions)
	if idx >= 0 {
		m.paletteIndex = idx
		return
	}
	m.paletteIndex = 0
}

func (m *model) movePaletteIndex(delta int) {
	actions := m.filteredPaletteActions()
	if len(actions) == 0 {
		m.paletteIndex = 0
		return
	}
	if delta == 0 {
		return
	}
	idx := m.paletteIndex
	if idx < 0 || idx >= len(actions) {
		idx = 0
	}
	for i := 0; i < len(actions); i++ {
		idx += delta
		if idx < 0 {
			idx = len(actions) - 1
		} else if idx >= len(actions) {
			idx = 0
		}
		if !actions[idx].Disabled {
			m.paletteIndex = idx
			return
		}
	}
	m.paletteIndex = 0
}

func (m *model) clampPaletteIndex() {
	actions := m.filteredPaletteActions()
	if len(actions) == 0 {
		m.paletteIndex = 0
		return
	}
	max := len(actions) - 1
	if m.paletteIndex < 0 {
		m.paletteIndex = 0
	}
	if m.paletteIndex > max {
		m.paletteIndex = max
	}
	if actions[m.paletteIndex].Disabled {
		if idx := firstSelectableIndex(actions); idx >= 0 {
			m.paletteIndex = idx
		}
	}
}

func (m model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyRunes:
		m.paletteQuery += string(msg.Runes)
		m.resetPaletteIndex()
	case tea.KeyBackspace, tea.KeyDelete:
		m.paletteQuery = trimLastRune(m.paletteQuery)
		m.resetPaletteIndex()
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
		if m.paletteIndex < 0 || m.paletteIndex >= len(actions) {
			m.closePalette()
			return m, nil
		}
		action := actions[m.paletteIndex]
		if action.Disabled {
			reason := action.DisabledReason
			if strings.TrimSpace(reason) == "" {
				reason = "Coming soon"
			}
			m.closePalette()
			m.setStatus(fmt.Sprintf("%s: %s", action.Label, reason), statusWarn)
			return m, nil
		}
		m.closePalette()
		return m, m.applyPaletteAction(action)
	case "up":
		m.movePaletteIndex(-1)
	case "down":
		m.movePaletteIndex(1)
	case "ctrl+n":
		m.movePaletteIndex(1)
	case "ctrl+p":
		m.movePaletteIndex(-1)
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
	case "view.audit":
		m.view = viewAudit
		m.selectedView = viewAudit
		m.ensureAuditSelection(m.filteredAuditItems())
	case "view.mailbox":
		m.view = viewMailbox
		m.selectedView = viewMailbox
		m.ensureMailSelection(m.filteredMailThreads())
	case "agent.spawn":
		m.setStatus("Spawn agent not wired yet.", statusWarn)
	case "agent.pause":
		return m.requestAgentAction(actionPause, true)
	case "agent.resume":
		return m.requestAgentAction(actionResume, true)
	case "agent.restart":
		return m.requestAgentAction(actionRestart, true)
	case "agent.interrupt":
		return m.requestAgentAction(actionInterrupt, true)
	case "agent.export_logs":
		return m.requestAgentAction(actionExportLogs, false)
	case "agents.pause_all":
		m.pausedAll = true
		m.setStatus("Paused all agents (preview).", statusInfo)
	case "agents.resume_all":
		m.pausedAll = false
		m.setStatus("Resumed all agents (preview).", statusInfo)
	case "refresh":
		m.lastUpdated = time.Now()
		m.stale = false
		m.refreshingUntil = time.Now().Add(refreshPulseDuration)
		m.refreshBeadsCache()
		cmds := []tea.Cmd{staleCheckCmd(m.lastUpdated)}
		if m.mailClient != nil {
			cmds = append(cmds, m.mailboxRefreshCmd())
		}
		return tea.Batch(cmds...)
	case "settings":
		m.setStatus("Settings panel not wired yet.", statusWarn)
	case "help.show":
		m.showHelp = true
	case "help.hide":
		m.showHelp = false
	case "quit":
		return tea.Quit
	}
	return nil
}

func filterPaletteActionsForView(actions []paletteAction, view viewID) []paletteAction {
	if len(actions) == 0 {
		return actions
	}

	filtered := make([]paletteAction, 0, len(actions))
	for _, action := range actions {
		if !actionVisibleInView(action, view) {
			continue
		}
		if isRedundantViewAction(action.ID, view) {
			continue
		}
		filtered = append(filtered, action)
	}
	return filtered
}

func actionVisibleInView(action paletteAction, view viewID) bool {
	if len(action.Views) == 0 {
		return true
	}
	for _, v := range action.Views {
		if v == view {
			return true
		}
	}
	return false
}

func isRedundantViewAction(actionID string, view viewID) bool {
	switch view {
	case viewDashboard:
		return actionID == "view.dashboard"
	case viewWorkspace:
		return actionID == "view.workspace"
	case viewAgent:
		return actionID == "view.agent"
	case viewAudit:
		return actionID == "view.audit"
	case viewMailbox:
		return actionID == "view.mailbox"
	default:
		return false
	}
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

type toastTickMsg struct{}

type actionCompleteMsg struct {
	Action string
	Agent  string
}

type mailboxPollMsg struct{}

type mailboxRefreshMsg struct {
	Messages   []agentMailMessage
	Err        error
	ReceivedAt time.Time
}

func staleCheckCmd(since time.Time) tea.Cmd {
	if since.IsZero() {
		return nil
	}
	return tea.Tick(staleAfter, func(time.Time) tea.Msg {
		return staleMsg{Since: since}
	})
}

func toastTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return toastTickMsg{}
	})
}

func actionCompleteCmd(action, agent string) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return actionCompleteMsg{Action: action, Agent: agent}
	})
}

func mailboxPollCmd(interval time.Duration) tea.Cmd {
	if interval <= 0 {
		return nil
	}
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return mailboxPollMsg{}
	})
}

func (m model) mailboxRefreshCmd() tea.Cmd {
	if m.mailClient == nil {
		return nil
	}
	client := m.mailClient
	return func() tea.Msg {
		messages, err := client.fetchInbox(context.Background())
		return mailboxRefreshMsg{
			Messages:   messages,
			Err:        err,
			ReceivedAt: time.Now(),
		}
	}
}

func (m model) refreshIndicatorLine() string {
	label := "--"
	if !m.lastUpdated.IsZero() {
		label = m.lastUpdated.Format("15:04:05")
	}
	line := fmt.Sprintf("Refresh: %s", label)
	if spinner := m.refreshSpinner(); spinner != "" {
		line = fmt.Sprintf("%s %s", line, spinner)
	}
	if m.width > 0 {
		line = truncateText(line, m.width)
	}
	if m.stale {
		return m.styles.Warning.Render(line + " (stale)")
	}
	return m.styles.Muted.Render(line)
}

func (m model) refreshSpinner() string {
	if m.refreshingUntil.IsZero() || time.Now().After(m.refreshingUntil) {
		return ""
	}
	return components.Spinner(m.pulseFrame)
}

func (m model) lastUpdatedLine() string {
	if m.lastUpdated.IsZero() {
		return "Last updated: --"
	}
	label := m.lastUpdated.Format("15:04:05")
	if m.stale {
		label += " (stale - press r)"
	}
	return fmt.Sprintf("Last updated: %s", label)
}

func (m model) lastUpdatedView() string {
	// Show spinner when actively refreshing
	if spinner := m.refreshSpinner(); spinner != "" {
		return m.styles.Accent.Render(spinner) + " " + m.styles.Muted.Render("Refreshing...")
	}

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
	switch m.statusSeverity {
	case statusWarn:
		return m.styles.Warning.Render(m.statusMsg)
	case statusError:
		return m.styles.Error.Render(m.statusMsg)
	}
	return m.styles.Info.Render(m.statusMsg)
}

func (m *model) setStatus(message string, severity statusSeverity) {
	m.statusMsg = message
	m.statusSeverity = severity
	if strings.TrimSpace(message) == "" {
		m.statusExpiresAt = time.Time{}
		return
	}
	m.statusExpiresAt = time.Now().Add(statusToastDuration)
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
	return filterAgentCards(m.allAgentCards(), m.agentFilter)
}

func (m model) allAgentCards() []components.AgentCard {
	if len(m.agentStates) == 0 {
		return m.applyProfileOverrides(sampleAgentCards())
	}

	cards := make([]components.AgentCard, 0, len(m.agentStates))
	for id, state := range m.agentStates {
		info := m.agentInfo[id]
		var lastPtr *time.Time
		if ts, ok := m.agentLast[id]; ok && !ts.IsZero() {
			last := ts
			lastPtr = &last
		}
		var cooldownPtr *time.Time
		if until, ok := m.agentCooldowns[id]; ok && !until.IsZero() {
			cooldown := until
			cooldownPtr = &cooldown
		}
		card := components.AgentCard{
			Name:          fmt.Sprintf("Agent %s", shortID(id)),
			State:         state,
			Confidence:    info.Confidence,
			Reason:        info.Reason,
			QueueLength:   -1,
			LastActivity:  lastPtr,
			CooldownUntil: cooldownPtr,
			RecentEvents:  m.agentRecentEvents[id],
		}
		cards = append(cards, card)
	}

	sort.Slice(cards, func(i, j int) bool {
		return cards[i].Name < cards[j].Name
	})
	return m.applyProfileOverrides(cards)
}

func (m model) applyProfileOverrides(cards []components.AgentCard) []components.AgentCard {
	if len(cards) == 0 || m.agentProfileOverrides == nil {
		return cards
	}
	for i := range cards {
		if profile := strings.TrimSpace(m.agentProfileOverrides[cards[i].Name]); profile != "" {
			cards[i].Profile = profile
		}
	}
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
			RecentEvents: []time.Time{
				now.Add(-2 * time.Minute),
				now.Add(-3 * time.Minute),
				now.Add(-5 * time.Minute),
			},
			UsageMetrics: &models.UsageMetrics{
				TotalCostCents:      12300,
				AvgCostPerDayCents:  6100,
				InputTokens:         45000,
				OutputTokens:        12000,
				CacheReadTokens:     8000,
				CacheWriteTokens:    2000,
				TotalTokens:         67000,
				Sessions:            25,
				Messages:            180,
				Days:                3,
				AvgTokensPerSession: 2680,
				Source:              "opencode.stats",
				UpdatedAt:           now.Add(-5 * time.Minute),
			},
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
			RecentEvents: []time.Time{
				now.Add(-12 * time.Minute),
			},
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
			RecentEvents: []time.Time{}, // No recent activity
		},
		{
			Name:          "Agent D4",
			Type:          models.AgentTypeOpenCode,
			Model:         "gpt-5",
			Profile:       "primary",
			State:         models.AgentStateRateLimited,
			Confidence:    models.StateConfidenceHigh,
			Reason:        "Rate limit hit, cooldown active",
			QueueLength:   0,
			LastActivity:  timePtr(now.Add(-1 * time.Minute)),
			CooldownUntil: timePtr(now.Add(4 * time.Minute)),
			RecentEvents: []time.Time{
				now.Add(-1 * time.Minute),
				now.Add(-2 * time.Minute),
				now.Add(-3 * time.Minute),
				now.Add(-4 * time.Minute),
				now.Add(-5 * time.Minute),
			},
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
	case viewAudit:
		m.searchQuery = m.auditFilter
	case viewMailbox:
		m.searchQuery = m.mailFilter
	}
	m.searchPrevQuery = m.searchQuery
	m.applySearchQuery()
}

func (m *model) closeSearch() {
	m.searchOpen = false
	m.searchQuery = ""
	m.searchPrevQuery = ""
}

func (m *model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cancelSearch()
		return m, nil
	case "enter":
		m.applySearchSelection()
		m.closeSearch()
		return m, nil
	case "ctrl+l":
		m.clearSearchFilter(m.searchTarget)
		m.closeSearch()
		m.setStatus(fmt.Sprintf("%s filter cleared.", viewLabel(m.searchTarget)), statusInfo)
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

func (m *model) cancelSearch() {
	m.searchQuery = m.searchPrevQuery
	m.applySearchQuery()
	m.closeSearch()
}

func (m *model) applySearchQuery() {
	switch m.searchTarget {
	case viewWorkspace:
		if m.workspaceGrid != nil {
			m.workspaceGrid.SetFilter(m.searchQuery)
		}
	case viewAgent:
		m.agentFilter = m.searchQuery
		m.syncAgentSelection()
	case viewAudit:
		m.auditFilter = m.searchQuery
		m.ensureAuditSelection(m.filteredAuditItems())
	case viewMailbox:
		m.mailFilter = m.searchQuery
		m.ensureMailSelection(m.filteredMailThreads())
	}
}

func (m *model) clearSearchFilter(target viewID) {
	switch target {
	case viewWorkspace:
		if m.workspaceGrid != nil {
			m.workspaceGrid.SetFilter("")
		}
	case viewAgent:
		m.agentFilter = ""
		m.syncAgentSelection()
	case viewAudit:
		m.auditFilter = ""
		m.ensureAuditSelection(m.filteredAuditItems())
	case viewMailbox:
		m.mailFilter = ""
		m.ensureMailSelection(m.filteredMailThreads())
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
		m.setStatus(fmt.Sprintf("Jumped to workspace %s", workspaceDisplayName(ws)), statusInfo)
	case viewAgent:
		cards := m.agentCards()
		if len(cards) == 0 {
			m.setStatus("No agents match filter.", statusWarn)
			return
		}
		m.selectedAgentIndex = 0
		m.selectedAgent = cards[0].Name
		m.setStatus(fmt.Sprintf("Jumped to agent %s", cards[0].Name), statusInfo)
	}
}

func (m *model) openProfileSelector() {
	card := m.selectedAgentCard()
	if card == nil {
		m.setStatus("No agent selected.", statusWarn)
		return
	}
	options := m.profileOptions(card)
	if len(options) == 0 {
		m.setStatus("No profiles available.", statusWarn)
		return
	}
	m.profileSelectOptions = options
	m.profileSelectAgent = card.Name
	m.profileSelectIndex = profileIndex(options, card.Profile)
	if m.profileSelectIndex < 0 {
		m.profileSelectIndex = 0
	}
	m.profileSelectOpen = true
}

func (m *model) closeProfileSelector() {
	m.profileSelectOpen = false
	m.profileSelectOptions = nil
	m.profileSelectAgent = ""
	m.profileSelectIndex = 0
}

func (m *model) updateProfileSelector(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.profileSelectOptions) == 0 {
		m.closeProfileSelector()
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.closeProfileSelector()
		return m, nil
	case "up", "k":
		if m.profileSelectIndex > 0 {
			m.profileSelectIndex--
		}
		return m, nil
	case "down", "j":
		if m.profileSelectIndex < len(m.profileSelectOptions)-1 {
			m.profileSelectIndex++
		}
		return m, nil
	case "enter":
		selected := m.profileSelectOptions[m.profileSelectIndex]
		agent := m.profileSelectAgent
		m.closeProfileSelector()
		current := strings.TrimSpace(m.currentProfile(agent))
		if strings.EqualFold(strings.TrimSpace(selected), current) {
			m.setStatus("Profile already active.", statusInfo)
			return m, nil
		}
		m.profileSwitchPending = selected
		m.openActionConfirm(actionSwitchProfile, agent)
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) profileOptions(card *components.AgentCard) []string {
	options := make(map[string]struct{})
	for _, existing := range m.allAgentCards() {
		if profile := strings.TrimSpace(existing.Profile); profile != "" {
			options[profile] = struct{}{}
		}
	}
	if card != nil {
		if profile := strings.TrimSpace(card.Profile); profile != "" {
			options[profile] = struct{}{}
		}
	}
	if len(options) == 0 {
		return nil
	}
	profiles := make([]string, 0, len(options))
	for profile := range options {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	return profiles
}

func (m model) currentProfile(agent string) string {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return ""
	}
	cards := m.allAgentCards()
	if card := findAgentCardByName(cards, agent); card != nil {
		return strings.TrimSpace(card.Profile)
	}
	if m.agentProfileOverrides != nil {
		return strings.TrimSpace(m.agentProfileOverrides[agent])
	}
	return ""
}

func profileIndex(options []string, value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	for i, option := range options {
		if strings.EqualFold(option, value) {
			return i
		}
	}
	return -1
}

func (m *model) requestAgentAction(action string, requiresConfirm bool) tea.Cmd {
	card := m.selectedAgentCard()
	if card == nil {
		m.setStatus("No agent selected.", statusWarn)
		return nil
	}
	agent := card.Name
	if requiresConfirm {
		m.openActionConfirm(action, agent)
		return nil
	}
	return m.applyAgentAction(action, agent)
}

func (m *model) openActionConfirm(action, agent string) {
	m.actionConfirmOpen = true
	m.actionConfirmAction = action
	m.actionConfirmAgent = agent
}

func (m *model) closeActionConfirm() {
	m.actionConfirmOpen = false
	m.actionConfirmAction = ""
	m.actionConfirmAgent = ""
	m.profileSwitchPending = ""
}

func (m *model) updateActionConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		action := m.actionConfirmAction
		agent := m.actionConfirmAgent
		profile := m.profileSwitchPending
		m.closeActionConfirm()
		if action == actionSwitchProfile {
			return m, m.applyProfileSwitch(agent, profile)
		}
		return m, m.applyAgentAction(action, agent)
	case "n", "N", "esc":
		label := actionLabel(m.actionConfirmAction)
		agent := m.actionConfirmAgent
		m.closeActionConfirm()
		if label != "" && agent != "" {
			m.setStatus(fmt.Sprintf("%s canceled for %s.", label, agent), statusInfo)
		} else {
			m.setStatus("Action canceled.", statusInfo)
		}
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m *model) applyAgentAction(action, agent string) tea.Cmd {
	label := actionLabel(action)
	m.actionInFlightAction = action
	m.actionInFlightAgent = agent
	switch action {
	case actionInterrupt:
		m.setStatus(fmt.Sprintf("%s requested for %s (preview).", label, agent), statusInfo)
	case actionRestart:
		m.setStatus(fmt.Sprintf("%s requested for %s (preview).", label, agent), statusInfo)
	case actionExportLogs:
		m.setStatus(fmt.Sprintf("Exporting logs for %s (preview).", agent), statusInfo)
	default:
		m.setStatus(fmt.Sprintf("Action %s sent for %s (preview).", label, agent), statusInfo)
	}
	return actionCompleteCmd(action, agent)
}

func (m *model) applyProfileSwitch(agent, profile string) tea.Cmd {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		m.setStatus("Profile is required.", statusWarn)
		return nil
	}
	if m.agentProfileOverrides == nil {
		m.agentProfileOverrides = make(map[string]string)
	}
	m.agentProfileOverrides[agent] = profile
	m.actionInFlightAction = actionSwitchProfile
	m.actionInFlightAgent = agent
	m.setStatus(fmt.Sprintf("Restart requested for %s with profile %s (preview).", agent, profile), statusInfo)
	return actionCompleteCmd(actionSwitchProfile, agent)
}

func (m *model) updateApprovalsInbox(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "esc", "A":
		m.approvalsOpen = false
		if _, wsID := m.approvalsForSelectedWorkspace(); wsID != "" {
			m.clearApprovalMarks(wsID)
		}
		m.cancelBulkApproval()
		return true, nil
	case "up", "k":
		m.moveApprovalSelection(-1)
		return true, nil
	case "down", "j":
		m.moveApprovalSelection(1)
		return true, nil
	case "x", " ", "space":
		items, wsID := m.approvalsForSelectedWorkspace()
		if wsID == "" || len(items) == 0 {
			m.setStatus("No approvals to select.", statusWarn)
			return true, nil
		}
		idx := approvalSelectionIndex(m.approvalsSelected, len(items))
		if idx < 0 || idx >= len(items) {
			m.setStatus("No approval selected.", statusWarn)
			return true, nil
		}
		m.toggleApprovalMark(wsID, items[idx])
		return true, nil
	case "ctrl+a":
		items, wsID := m.approvalsForSelectedWorkspace()
		m.toggleAllPendingApprovals(wsID, items)
		return true, nil
	case "y", "Y":
		if msg.String() == "Y" {
			m.startBulkApproval(models.ApprovalStatusApproved)
		} else {
			m.resolveSelectedApproval(models.ApprovalStatusApproved)
		}
		return true, nil
	case "n", "N":
		if msg.String() == "N" {
			m.startBulkApproval(models.ApprovalStatusDenied)
		} else {
			m.resolveSelectedApproval(models.ApprovalStatusDenied)
		}
		return true, nil
	}
	return false, nil
}

func (m *model) moveApprovalSelection(delta int) {
	items, _ := m.approvalsForSelectedWorkspace()
	if len(items) == 0 {
		m.approvalsSelected = 0
		return
	}
	idx := approvalSelectionIndex(m.approvalsSelected, len(items))
	idx = clampInt(idx+delta, 0, len(items)-1)
	m.approvalsSelected = idx
}

func (m *model) resolveSelectedApproval(status models.ApprovalStatus) {
	items, wsID := m.approvalsForSelectedWorkspace()
	if wsID == "" || len(items) == 0 {
		m.setStatus("No approvals to update.", statusWarn)
		return
	}
	idx := approvalSelectionIndex(m.approvalsSelected, len(items))
	if idx < 0 || idx >= len(items) {
		m.setStatus("No approval selected.", statusWarn)
		return
	}
	items[idx].Status = status
	if m.approvalsByWorkspace == nil {
		m.approvalsByWorkspace = make(map[string][]approvalItem)
	}
	m.approvalsByWorkspace[wsID] = items
	if marked := m.approvalSelectionMap(wsID); marked != nil {
		delete(marked, items[idx].ID)
	}

	action := "Approved"
	if status == models.ApprovalStatusDenied {
		action = "Denied"
	}
	m.setStatus(fmt.Sprintf("%s approval %s.", action, items[idx].ID), statusInfo)
}

func (m *model) approvalsForSelectedWorkspace() ([]approvalItem, string) {
	ws := m.selectedWorkspace()
	if ws == nil {
		return nil, ""
	}
	return m.approvalsForWorkspace(ws.ID), ws.ID
}

func (m *model) updateQueueEditor(msg tea.KeyMsg) (bool, tea.Cmd) {
	if msg.String() == "esc" {
		m.queueEditorOpen = false
		m.closeQueueEdit()
		return true, nil
	}

	state, agent := m.currentQueueEditorState()
	switch msg.String() {
	case "up", "k":
		if state != nil {
			m.moveQueueSelection(state, -1)
		}
		return true, nil
	case "down", "j":
		if state != nil {
			m.moveQueueSelection(state, 1)
		}
		return true, nil
	case "K":
		if state != nil {
			m.moveQueueItem(state, -1)
		}
		return true, nil
	case "J":
		if state != nil {
			m.moveQueueItem(state, 1)
		}
		return true, nil
	case "ctrl+up", "ctrl+left":
		if state != nil {
			m.moveQueueItem(state, -1)
		}
		return true, nil
	case "ctrl+down", "ctrl+right":
		if state != nil {
			m.moveQueueItem(state, 1)
		}
		return true, nil
	case "d":
		if state != nil {
			m.deleteQueueItem(state)
		} else {
			m.setStatus("No agent selected.", statusWarn)
		}
		return true, nil
	case "m":
		if state != nil {
			m.addQueueItem(state, models.QueueItemTypeMessage, "New message")
		} else {
			m.setStatus("No agent selected.", statusWarn)
		}
		return true, nil
	case "p":
		if state != nil {
			m.openQueueAdd(agent, state, models.QueueItemTypePause, "Pause duration (e.g., 5m)", "5m")
		} else {
			m.setStatus("No agent selected.", statusWarn)
		}
		return true, nil
	case "c":
		if state != nil {
			m.openQueueAdd(agent, state, models.QueueItemTypeConditional, "Condition (e.g., after_cooldown)", "after_cooldown")
		} else {
			m.setStatus("No agent selected.", statusWarn)
		}
		return true, nil
	case "e":
		if state != nil {
			m.openQueueEdit(state, agent)
		} else {
			m.setStatus("No agent selected.", statusWarn)
		}
		return true, nil
	}
	return false, nil
}

func (m *model) updateQueueEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeQueueEdit()
		return m, nil
	case "enter":
		m.applyQueueEdit()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}

	switch msg.Type {
	case tea.KeyRunes:
		m.queueEditBuffer += string(msg.Runes)
	case tea.KeyBackspace, tea.KeyDelete:
		m.queueEditBuffer = trimLastRune(m.queueEditBuffer)
	}
	return m, nil
}

func (m *model) currentQueueEditorState() (*queueEditorState, string) {
	card := m.selectedAgentCard()
	if card == nil {
		return nil, ""
	}
	agent := card.Name
	state := m.ensureQueueEditorState(agent)
	return state, agent
}

func (m *model) ensureQueueEditorState(agent string) *queueEditorState {
	if strings.TrimSpace(agent) == "" {
		return nil
	}
	if m.queueEditors == nil {
		m.queueEditors = make(map[string]*queueEditorState)
	}
	state, ok := m.queueEditors[agent]
	if !ok {
		state = &queueEditorState{}
		m.queueEditors[agent] = state
	}
	return state
}

func (m *model) openQueueEdit(state *queueEditorState, agent string) {
	if state == nil || len(state.Items) == 0 {
		m.setStatus("Queue is empty.", statusWarn)
		return
	}
	if state.Selected < 0 || state.Selected >= len(state.Items) {
		state.Selected = 0
	}
	m.queueEditOpen = true
	m.queueEditAgent = agent
	m.queueEditIndex = state.Selected
	m.queueEditBuffer = state.Items[state.Selected].Summary
	m.queueEditMode = queueEditModeEdit
	m.queueEditKind = state.Items[state.Selected].Kind
	m.queueEditPrompt = "Item summary"
}

func (m *model) openQueueAdd(agent string, state *queueEditorState, kind models.QueueItemType, prompt string, placeholder string) {
	if strings.TrimSpace(agent) == "" {
		m.setStatus("No agent selected.", statusWarn)
		return
	}
	insertIndex := 0
	if state != nil && len(state.Items) > 0 {
		if state.Selected < 0 || state.Selected >= len(state.Items) {
			state.Selected = 0
		}
		insertIndex = state.Selected + 1
		if insertIndex > len(state.Items) {
			insertIndex = len(state.Items)
		}
	}
	m.queueEditOpen = true
	m.queueEditAgent = agent
	m.queueEditIndex = insertIndex
	m.queueEditBuffer = placeholder
	m.queueEditMode = queueEditModeAdd
	m.queueEditKind = kind
	m.queueEditPrompt = prompt
}

func (m *model) closeQueueEdit() {
	m.queueEditOpen = false
	m.queueEditAgent = ""
	m.queueEditIndex = 0
	m.queueEditBuffer = ""
	m.queueEditMode = queueEditModeEdit
	m.queueEditKind = ""
	m.queueEditPrompt = ""
}

func (m *model) applyQueueEdit() {
	state := m.ensureQueueEditorState(m.queueEditAgent)
	if state == nil {
		m.closeQueueEdit()
		m.setStatus("No queue available.", statusWarn)
		return
	}
	if m.queueEditMode == queueEditModeEdit && (m.queueEditIndex < 0 || m.queueEditIndex >= len(state.Items)) {
		m.closeQueueEdit()
		m.setStatus("Queue selection changed; edit canceled.", statusWarn)
		return
	}
	text := strings.TrimSpace(m.queueEditBuffer)
	if text == "" {
		m.closeQueueEdit()
		m.setStatus("Queue edit canceled (empty).", statusWarn)
		return
	}
	if m.queueEditMode == queueEditModeAdd {
		summary := text
		switch m.queueEditKind {
		case models.QueueItemTypePause:
			summary = fmt.Sprintf("Pause %s", text)
		case models.QueueItemTypeConditional:
			summary = fmt.Sprintf("Wait until %s", text)
		}
		item := queueItem{
			ID:      nextQueueItemID(state),
			Kind:    m.queueEditKind,
			Summary: summary,
			Status:  models.QueueItemStatusPending,
		}
		insertIndex := clampInt(m.queueEditIndex, 0, len(state.Items))
		state.Items = append(state.Items, queueItem{})
		copy(state.Items[insertIndex+1:], state.Items[insertIndex:])
		state.Items[insertIndex] = item
		state.Selected = insertIndex
		m.closeQueueEdit()
		m.setStatus("Queue item added.", statusInfo)
		return
	}
	state.Items[m.queueEditIndex].Summary = text
	m.closeQueueEdit()
	m.setStatus("Queue item updated.", statusInfo)
}

func (m *model) moveQueueSelection(state *queueEditorState, delta int) {
	if state == nil || len(state.Items) == 0 {
		if state != nil {
			state.Selected = 0
		}
		return
	}
	state.Selected = clampInt(state.Selected+delta, 0, len(state.Items)-1)
}

func (m *model) moveQueueItem(state *queueEditorState, delta int) {
	if state == nil || len(state.Items) == 0 {
		return
	}
	index := state.Selected
	next := index + delta
	if next < 0 || next >= len(state.Items) {
		return
	}
	state.Items[index], state.Items[next] = state.Items[next], state.Items[index]
	state.Selected = next
	m.setStatus("Queue order updated.", statusInfo)
}

func (m *model) deleteQueueItem(state *queueEditorState) {
	if state == nil || len(state.Items) == 0 {
		return
	}
	index := state.Selected
	state.Items = append(state.Items[:index], state.Items[index+1:]...)
	if len(state.Items) == 0 {
		state.Selected = 0
	} else if index >= len(state.Items) {
		state.Selected = len(state.Items) - 1
	}
	m.setStatus("Queue item deleted.", statusInfo)
}

func (m *model) addQueueItem(state *queueEditorState, kind models.QueueItemType, summary string) {
	if state == nil {
		return
	}
	item := queueItem{
		ID:      nextQueueItemID(state),
		Kind:    kind,
		Summary: summary,
		Status:  models.QueueItemStatusPending,
	}
	state.Items = append(state.Items, item)
	state.Selected = len(state.Items) - 1
	m.setStatus("Queue item added.", statusInfo)
}

func nextQueueItemID(state *queueEditorState) string {
	if state == nil {
		return "q-1"
	}
	return fmt.Sprintf("q-%02d", len(state.Items)+1)
}

func (m *model) openTranscriptSearch() {
	if m.transcriptViewer == nil {
		return
	}
	m.transcriptSearchOpen = true
	m.transcriptSearchQuery = m.transcriptViewer.SearchQuery
}

func (m *model) closeTranscriptSearch() {
	m.transcriptSearchOpen = false
	m.transcriptSearchQuery = ""
}

func (m *model) updateTranscriptSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeTranscriptSearch()
		return m, nil
	case "enter":
		query := strings.TrimSpace(m.transcriptSearchQuery)
		if m.transcriptViewer != nil {
			if query == "" {
				m.transcriptViewer.ClearSearch()
			} else {
				m.transcriptViewer.SetSearch(query)
				m.transcriptAutoScroll = false
			}
		}
		m.closeTranscriptSearch()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}

	switch msg.Type {
	case tea.KeyRunes:
		m.transcriptSearchQuery += string(msg.Runes)
	case tea.KeyBackspace, tea.KeyDelete:
		m.transcriptSearchQuery = trimLastRune(m.transcriptSearchQuery)
	}

	return m, nil
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
	case viewAudit:
		query = m.auditFilter
	case viewMailbox:
		query = m.mailFilter
	}
	activeSearch := m.searchOpen && m.searchTarget == target
	label := "Filter"
	style := m.styles.Muted
	if activeSearch {
		label = "Search"
		style = m.styles.Info
	}
	query = strings.TrimSpace(query)
	if query == "" {
		if !activeSearch {
			return ""
		}
		query = "..."
	}
	return style.Render(fmt.Sprintf("%s (%s): %s", label, viewLabel(target), query))
}

func (m model) agentSearchStatusLines() []string {
	query := strings.TrimSpace(m.agentFilter)
	if query == "" {
		return nil
	}
	allCards := m.allAgentCards()
	filtered := filterAgentCards(allCards, query)
	total := len(allCards)
	matches := len(filtered)
	lines := []string{
		m.styles.Muted.Render(fmt.Sprintf("Matches: %d/%d", matches, total)),
	}
	if matches == 0 {
		lines = append(lines,
			m.styles.Warning.Render("No agents match this filter."),
			m.styles.Muted.Render("Press / to edit or clear the filter."),
		)
	}
	return lines
}

func (m model) auditSearchStatusLines() []string {
	query := strings.TrimSpace(m.auditFilter)
	if query == "" {
		return nil
	}
	allItems := m.auditItems
	filtered := m.filteredAuditItems()
	total := len(allItems)
	matches := len(filtered)
	lines := []string{
		m.styles.Muted.Render(fmt.Sprintf("Matches: %d/%d", matches, total)),
	}
	if matches == 0 {
		lines = append(lines,
			m.styles.Warning.Render("No audit events match this filter."),
			m.styles.Muted.Render("Press / to edit or clear the filter."),
		)
	}
	return lines
}

func (m model) mailSearchStatusLines() []string {
	query := strings.TrimSpace(m.mailFilter)
	if query == "" {
		return nil
	}
	allThreads := m.mailThreads
	filtered := m.filteredMailThreads()
	total := len(allThreads)
	matches := len(filtered)
	lines := []string{
		m.styles.Muted.Render(fmt.Sprintf("Matches: %d/%d", matches, total)),
	}
	if matches == 0 {
		lines = append(lines,
			m.styles.Warning.Render("No mailbox threads match this filter."),
			m.styles.Muted.Render("Press / to edit or clear the filter."),
		)
	}
	return lines
}

func (m model) mailboxStatusLines(maxWidth int) []string {
	if m.mailClient == nil {
		line := "Agent Mail not configured. Set SWARM_AGENT_MAIL_AGENT and SWARM_AGENT_MAIL_PROJECT."
		return []string{m.styles.Warning.Render(truncateText(line, maxWidth))}
	}

	status := "syncing..."
	style := m.styles.Muted
	if strings.TrimSpace(m.mailSyncErr) != "" {
		status = fmt.Sprintf("error: %s", m.mailSyncErr)
		style = m.styles.Warning
	} else if !m.mailLastSynced.IsZero() {
		status = fmt.Sprintf("synced %s", m.mailLastSynced.Format("15:04:05"))
	}

	label := fmt.Sprintf("Agent Mail: %s@%s (%s)", m.mailClient.agent, m.mailClient.project, status)
	return []string{style.Render(truncateText(label, maxWidth))}
}

func (m model) transcriptSearchLine() string {
	if !m.showTranscript || m.transcriptViewer == nil {
		return ""
	}
	query := strings.TrimSpace(m.transcriptViewer.SearchQuery)
	if m.transcriptSearchOpen {
		query = strings.TrimSpace(m.transcriptSearchQuery)
		if query == "" {
			query = "..."
		}
		return m.styles.Info.Render(fmt.Sprintf("Search transcript: %s", query))
	}
	if query == "" {
		return ""
	}
	return m.styles.Muted.Render(fmt.Sprintf("Search transcript: %s", query))
}

func (m model) actionConfirmLine() string {
	if !m.actionConfirmOpen {
		return ""
	}
	label := actionLabel(m.actionConfirmAction)
	agent := m.actionConfirmAgent
	if label == "" {
		label = "Action"
	}
	if agent == "" {
		agent = "agent"
	}
	if m.actionConfirmAction == actionSwitchProfile {
		profile := strings.TrimSpace(m.profileSwitchPending)
		if profile == "" {
			profile = "profile"
		}
		return m.styles.Warning.Render(fmt.Sprintf("Confirm switch to %s for %s? (y/n)", profile, agent))
	}
	return m.styles.Warning.Render(fmt.Sprintf("Confirm %s for %s? (y/n)", strings.ToLower(label), agent))
}

func (m model) actionProgressLine() string {
	if m.actionInFlightAction == "" {
		return ""
	}
	label := actionLabel(m.actionInFlightAction)
	if label == "" {
		label = "Action"
	}
	agent := m.actionInFlightAgent
	if agent == "" {
		agent = "agent"
	}
	return m.styles.Info.Render(fmt.Sprintf("Action: %s (%s)", label, agent))
}

func actionLabel(action string) string {
	switch action {
	case actionInterrupt:
		return "Interrupt"
	case actionRestart:
		return "Restart"
	case actionExportLogs:
		return "Export logs"
	case actionPause:
		return "Pause"
	case actionResume:
		return "Resume"
	case actionSwitchProfile:
		return "Switch profile"
	case actionView:
		return "View"
	default:
		return action
	}
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

func filterAuditItems(items []auditItem, query string) []auditItem {
	if strings.TrimSpace(query) == "" {
		return items
	}
	filtered := make([]auditItem, 0, len(items))
	for _, item := range items {
		if matchesSearch(query, auditItemHaystack(item)) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterMailThreads(threads []mailThread, query string) []mailThread {
	if strings.TrimSpace(query) == "" {
		return threads
	}
	filtered := make([]mailThread, 0, len(threads))
	for _, thread := range threads {
		if matchesSearch(query, mailThreadHaystack(thread)) {
			filtered = append(filtered, thread)
		}
	}
	return filtered
}

func agentCardHaystack(card components.AgentCard) string {
	fields := []string{
		card.Name,
		string(card.Type),
		card.Model,
		card.Profile,
	}
	if card.CooldownUntil != nil && time.Until(*card.CooldownUntil) > 0 {
		fields = append(fields, "cooldown")
	}
	return strings.ToLower(strings.Join(fields, " "))
}

func auditItemHaystack(item auditItem) string {
	fields := []string{
		item.ID,
		item.Timestamp.Format("2006-01-02 15:04:05"),
		string(item.Type),
		string(item.EntityType),
		item.EntityID,
		item.Summary,
		item.Detail,
	}
	return strings.ToLower(strings.Join(fields, " "))
}

func mailThreadHaystack(thread mailThread) string {
	fields := []string{thread.ID, thread.Subject}
	for _, msg := range thread.Messages {
		fields = append(fields,
			msg.ID,
			msg.From,
			msg.Body,
			msg.CreatedAt.Format("2006-01-02 15:04:05"),
		)
	}
	return strings.ToLower(strings.Join(fields, " "))
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
