// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// TranscriptViewer displays a scrollable transcript with syntax highlighting.
type TranscriptViewer struct {
	Lines        []string
	ScrollOffset int
	Height       int
	Width        int
	SearchQuery  string
	SearchIndex  int   // Current search match index
	searchHits   []int // Line indices that match search
}

// NewTranscriptViewer creates a new transcript viewer.
func NewTranscriptViewer() *TranscriptViewer {
	return &TranscriptViewer{
		Lines:  make([]string, 0),
		Height: 20,
		Width:  60,
	}
}

// SetLines sets the transcript content.
func (v *TranscriptViewer) SetLines(lines []string) {
	v.Lines = lines
	v.clampScroll()
	v.updateSearchHits()
}

// SetContent sets the transcript from a single string.
func (v *TranscriptViewer) SetContent(content string) {
	if content == "" {
		v.Lines = nil
	} else {
		v.Lines = strings.Split(content, "\n")
	}
	v.clampScroll()
	v.updateSearchHits()
}

// ScrollUp scrolls the view up by n lines.
func (v *TranscriptViewer) ScrollUp(n int) {
	v.ScrollOffset -= n
	v.clampScroll()
}

// ScrollDown scrolls the view down by n lines.
func (v *TranscriptViewer) ScrollDown(n int) {
	v.ScrollOffset += n
	v.clampScroll()
}

// ScrollToTop scrolls to the top.
func (v *TranscriptViewer) ScrollToTop() {
	v.ScrollOffset = 0
}

// ScrollToBottom scrolls to the bottom.
func (v *TranscriptViewer) ScrollToBottom() {
	maxOffset := len(v.Lines) - v.visibleLines()
	if maxOffset < 0 {
		maxOffset = 0
	}
	v.ScrollOffset = maxOffset
}

// SetSearch sets the search query and finds matches.
func (v *TranscriptViewer) SetSearch(query string) {
	v.SearchQuery = query
	v.SearchIndex = 0
	v.updateSearchHits()
	if len(v.searchHits) > 0 {
		v.scrollToLine(v.searchHits[0])
	}
}

// ClearSearch clears the search.
func (v *TranscriptViewer) ClearSearch() {
	v.SearchQuery = ""
	v.SearchIndex = 0
	v.searchHits = nil
}

// NextSearchHit moves to the next search result.
func (v *TranscriptViewer) NextSearchHit() {
	if len(v.searchHits) == 0 {
		return
	}
	v.SearchIndex = (v.SearchIndex + 1) % len(v.searchHits)
	v.scrollToLine(v.searchHits[v.SearchIndex])
}

// PrevSearchHit moves to the previous search result.
func (v *TranscriptViewer) PrevSearchHit() {
	if len(v.searchHits) == 0 {
		return
	}
	v.SearchIndex--
	if v.SearchIndex < 0 {
		v.SearchIndex = len(v.searchHits) - 1
	}
	v.scrollToLine(v.searchHits[v.SearchIndex])
}

// SearchHitCount returns the number of search matches.
func (v *TranscriptViewer) SearchHitCount() int {
	return len(v.searchHits)
}

func (v *TranscriptViewer) updateSearchHits() {
	v.searchHits = nil
	if v.SearchQuery == "" {
		return
	}
	query := strings.ToLower(v.SearchQuery)
	for i, line := range v.Lines {
		if strings.Contains(strings.ToLower(line), query) {
			v.searchHits = append(v.searchHits, i)
		}
	}
}

func (v *TranscriptViewer) scrollToLine(lineIdx int) {
	visible := v.visibleLines()
	if lineIdx < v.ScrollOffset {
		v.ScrollOffset = lineIdx
	} else if lineIdx >= v.ScrollOffset+visible {
		v.ScrollOffset = lineIdx - visible + 1
	}
	v.clampScroll()
}

func (v *TranscriptViewer) visibleLines() int {
	if v.Height <= 2 {
		return 1
	}
	return v.Height - 2 // Reserve space for header/footer
}

func (v *TranscriptViewer) clampScroll() {
	maxOffset := len(v.Lines) - v.visibleLines()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if v.ScrollOffset > maxOffset {
		v.ScrollOffset = maxOffset
	}
	if v.ScrollOffset < 0 {
		v.ScrollOffset = 0
	}
}

// Render renders the transcript viewer.
func (v *TranscriptViewer) Render(styleSet styles.Styles) string {
	if len(v.Lines) == 0 {
		return styleSet.Muted.Render("No transcript data.")
	}

	visible := v.visibleLines()
	endIdx := v.ScrollOffset + visible
	if endIdx > len(v.Lines) {
		endIdx = len(v.Lines)
	}

	// Build visible lines
	var rendered []string
	lineNumWidth := len(fmt.Sprintf("%d", len(v.Lines)))

	for i := v.ScrollOffset; i < endIdx; i++ {
		line := v.Lines[i]
		lineNum := fmt.Sprintf("%*d", lineNumWidth, i+1)

		// Check if this line is a search hit
		isHit := false
		isCurrentHit := false
		for hitIdx, hitLine := range v.searchHits {
			if hitLine == i {
				isHit = true
				isCurrentHit = hitIdx == v.SearchIndex
				break
			}
		}

		// Apply syntax highlighting
		styledLine := v.highlightLine(styleSet, line)

		// Highlight search matches in the line
		if v.SearchQuery != "" && isHit {
			styledLine = v.highlightSearchMatch(styleSet, line, isCurrentHit)
		}

		// Format with line number
		lineNumStyle := styleSet.Muted
		if isCurrentHit {
			lineNumStyle = styleSet.Accent
		}
		renderedLine := fmt.Sprintf("%s │ %s", lineNumStyle.Render(lineNum), styledLine)

		// Truncate if too wide
		if v.Width > 0 && lipgloss.Width(renderedLine) > v.Width {
			renderedLine = truncateString(renderedLine, v.Width-3) + "..."
		}

		rendered = append(rendered, renderedLine)
	}

	// Add scroll indicator
	scrollInfo := v.scrollIndicator(styleSet)
	if scrollInfo != "" {
		rendered = append(rendered, scrollInfo)
	}

	return strings.Join(rendered, "\n")
}

func (v *TranscriptViewer) scrollIndicator(styleSet styles.Styles) string {
	total := len(v.Lines)
	if total == 0 {
		return ""
	}

	visible := v.visibleLines()
	if total <= visible {
		return styleSet.Muted.Render(fmt.Sprintf("─── %d lines ───", total))
	}

	endLine := v.ScrollOffset + visible
	if endLine > total {
		endLine = total
	}
	percent := 0
	if total > visible {
		percent = (v.ScrollOffset * 100) / (total - visible)
	}

	info := fmt.Sprintf("─── %d-%d of %d (%d%%) ───", v.ScrollOffset+1, endLine, total, percent)
	if v.SearchQuery != "" && len(v.searchHits) > 0 {
		info = fmt.Sprintf("─── %d-%d of %d | Match %d/%d ───",
			v.ScrollOffset+1, endLine, total, v.SearchIndex+1, len(v.searchHits))
	}
	return styleSet.Muted.Render(info)
}

// highlightLine applies basic syntax highlighting.
func (v *TranscriptViewer) highlightLine(styleSet styles.Styles, line string) string {
	// Detect code block markers
	if strings.HasPrefix(strings.TrimSpace(line), "```") {
		return styleSet.Accent.Render(line)
	}

	// Detect prompt indicators (common in agent output)
	if promptMatch := promptPattern.FindStringIndex(line); promptMatch != nil {
		prompt := line[:promptMatch[1]]
		rest := line[promptMatch[1]:]
		return styleSet.Success.Render(prompt) + styleSet.Text.Render(rest)
	}

	// Detect error lines
	if errorMatch := errorPattern.MatchString(line); errorMatch {
		return styleSet.Error.Render(line)
	}

	// Detect warning lines
	if warningMatch := warningPattern.MatchString(line); warningMatch {
		return styleSet.Warning.Render(line)
	}

	// Detect info/status lines
	if infoMatch := infoPattern.MatchString(line); infoMatch {
		return styleSet.Info.Render(line)
	}

	return styleSet.Text.Render(line)
}

func (v *TranscriptViewer) highlightSearchMatch(styleSet styles.Styles, line string, isCurrentHit bool) string {
	if v.SearchQuery == "" {
		return styleSet.Text.Render(line)
	}

	query := strings.ToLower(v.SearchQuery)
	lineLower := strings.ToLower(line)
	idx := strings.Index(lineLower, query)
	if idx < 0 {
		return styleSet.Text.Render(line)
	}

	before := line[:idx]
	match := line[idx : idx+len(v.SearchQuery)]
	after := line[idx+len(v.SearchQuery):]

	matchStyle := styleSet.Warning
	if isCurrentHit {
		matchStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#000000")).
			Background(lipgloss.Color("#FFD700")).
			Bold(true)
	}

	return styleSet.Text.Render(before) + matchStyle.Render(match) + styleSet.Text.Render(after)
}

func truncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

// Regex patterns for syntax highlighting
var (
	promptPattern  = regexp.MustCompile(`^[\$#>»] `)
	errorPattern   = regexp.MustCompile(`(?i)^(error|err|fatal|exception|panic|failed)`)
	warningPattern = regexp.MustCompile(`(?i)^(warning|warn|caution)`)
	infoPattern    = regexp.MustCompile(`(?i)^(info|note|hint|\[.*\])`)
)

// RenderTranscriptPanel renders a titled transcript panel.
func RenderTranscriptPanel(styleSet styles.Styles, viewer *TranscriptViewer, title string, width int) string {
	if viewer == nil {
		return styleSet.Muted.Render("No transcript viewer.")
	}

	viewer.Width = width - 4 // Account for panel padding/borders
	content := viewer.Render(styleSet)

	header := styleSet.Accent.Render(title)
	if viewer.SearchQuery != "" {
		searchInfo := fmt.Sprintf(" (/%s)", viewer.SearchQuery)
		if len(viewer.searchHits) > 0 {
			searchInfo = fmt.Sprintf(" (/%s %d/%d)", viewer.SearchQuery, viewer.SearchIndex+1, len(viewer.searchHits))
		}
		header += styleSet.Muted.Render(searchInfo)
	}

	panelContent := header + "\n" + content
	return styleSet.Panel.Copy().Width(width).Padding(0, 1).Render(panelContent)
}
