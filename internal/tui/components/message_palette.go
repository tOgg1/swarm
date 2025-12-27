// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"sort"
	"strings"

	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// MessagePaletteKind identifies palette entry type.
type MessagePaletteKind string

const (
	MessagePaletteKindTemplate MessagePaletteKind = "template"
	MessagePaletteKindSequence MessagePaletteKind = "sequence"
)

// MessagePaletteSection identifies the active palette section.
type MessagePaletteSection int

const (
	MessagePaletteSectionTemplates MessagePaletteSection = iota
	MessagePaletteSectionSequences
)

// MessagePaletteItem represents a template or sequence entry.
type MessagePaletteItem struct {
	Kind        MessagePaletteKind
	Name        string
	Description string
	Tags        []string
}

// MessagePalette stores state for the message palette UI.
type MessagePalette struct {
	Query     string
	Section   MessagePaletteSection
	Index     int
	Templates []MessagePaletteItem
	Sequences []MessagePaletteItem
}

// NewMessagePalette creates a new message palette state.
func NewMessagePalette() *MessagePalette {
	return &MessagePalette{Section: MessagePaletteSectionTemplates}
}

// SetTemplates updates the template list.
func (p *MessagePalette) SetTemplates(items []MessagePaletteItem) {
	p.Templates = cloneMessagePaletteItems(items)
	sortMessagePaletteItems(p.Templates)
}

// SetSequences updates the sequence list.
func (p *MessagePalette) SetSequences(items []MessagePaletteItem) {
	p.Sequences = cloneMessagePaletteItems(items)
	sortMessagePaletteItems(p.Sequences)
}

// Reset clears the palette state.
func (p *MessagePalette) Reset() {
	p.Query = ""
	p.Section = MessagePaletteSectionTemplates
	p.Index = 0
}

// ResetIndex resets the selection index for the active section.
func (p *MessagePalette) ResetIndex() {
	p.Index = 0
	p.ClampIndex()
}

// NextSection cycles the active section.
func (p *MessagePalette) NextSection() {
	if p.Section == MessagePaletteSectionTemplates {
		p.Section = MessagePaletteSectionSequences
	} else {
		p.Section = MessagePaletteSectionTemplates
	}
	p.Index = 0
}

// Move shifts the selection within the active section.
func (p *MessagePalette) Move(delta int) {
	items := p.activeItems()
	if len(items) == 0 {
		p.Index = 0
		return
	}
	if delta == 0 {
		return
	}
	idx := p.Index
	if idx < 0 || idx >= len(items) {
		idx = 0
	}
	idx += delta
	if idx < 0 {
		idx = len(items) - 1
	} else if idx >= len(items) {
		idx = 0
	}
	p.Index = idx
}

// ClampIndex ensures the selection index stays in bounds.
func (p *MessagePalette) ClampIndex() {
	items := p.activeItems()
	if len(items) == 0 {
		p.Index = 0
		return
	}
	if p.Index < 0 {
		p.Index = 0
	}
	if p.Index >= len(items) {
		p.Index = len(items) - 1
	}
}

// SelectedItem returns the currently selected entry.
func (p *MessagePalette) SelectedItem() *MessagePaletteItem {
	items := p.activeItems()
	if len(items) == 0 {
		return nil
	}
	if p.Index < 0 || p.Index >= len(items) {
		return nil
	}
	selected := items[p.Index]
	return &selected
}

// Render renders the message palette lines.
func (p *MessagePalette) Render(styleSet styles.Styles) []string {
	lines := []string{
		styleSet.Accent.Render("Message Palette"),
		styleSet.Muted.Render("Type to filter. Enter to select. Esc to close. Tab switches sections."),
		styleSet.Text.Render(fmt.Sprintf("> %s", p.Query)),
	}

	templates := p.filteredItems(p.Templates)
	sequences := p.filteredItems(p.Sequences)
	if len(templates) == 0 && len(sequences) == 0 {
		lines = append(lines, styleSet.Muted.Render("No templates or sequences found."))
		return lines
	}

	lines = append(lines, p.renderSection(styleSet, "TEMPLATES", templates, p.Section == MessagePaletteSectionTemplates)...)
	lines = append(lines, "")
	lines = append(lines, p.renderSection(styleSet, "SEQUENCES", sequences, p.Section == MessagePaletteSectionSequences)...)
	return lines
}

func (p *MessagePalette) renderSection(styleSet styles.Styles, title string, items []MessagePaletteItem, active bool) []string {
	headingStyle := styleSet.Muted
	if active {
		headingStyle = styleSet.Accent
	}
	lines := []string{headingStyle.Render(title)}
	if len(items) == 0 {
		lines = append(lines, styleSet.Muted.Render("  (none)"))
		return lines
	}
	for idx, item := range items {
		label := item.Name
		if desc := strings.TrimSpace(item.Description); desc != "" {
			label = fmt.Sprintf("%s - %s", item.Name, desc)
		}
		label = truncate(label, 80)
		line := fmt.Sprintf("  %s", label)
		if active && idx == p.Index {
			lines = append(lines, styleSet.Focus.Render("> "+label))
			continue
		}
		lines = append(lines, styleSet.Muted.Render(line))
	}
	return lines
}

func (p *MessagePalette) filteredItems(items []MessagePaletteItem) []MessagePaletteItem {
	query := strings.TrimSpace(strings.ToLower(p.Query))
	if query == "" {
		return items
	}
	tokens := strings.Fields(query)
	filtered := make([]MessagePaletteItem, 0, len(items))
	for _, item := range items {
		haystack := strings.ToLower(strings.TrimSpace(strings.Join([]string{item.Name, item.Description, strings.Join(item.Tags, " ")}, " ")))
		if matchesTokens(haystack, tokens) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (p *MessagePalette) activeItems() []MessagePaletteItem {
	if p.Section == MessagePaletteSectionSequences {
		return p.filteredItems(p.Sequences)
	}
	return p.filteredItems(p.Templates)
}

func cloneMessagePaletteItems(items []MessagePaletteItem) []MessagePaletteItem {
	if len(items) == 0 {
		return nil
	}
	clone := make([]MessagePaletteItem, len(items))
	copy(clone, items)
	return clone
}

func sortMessagePaletteItems(items []MessagePaletteItem) {
	if len(items) == 0 {
		return
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}
