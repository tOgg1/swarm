package styles

import "github.com/charmbracelet/lipgloss"

// Styles contains lipgloss styles derived from theme tokens.
type Styles struct {
	Theme        Theme
	Title        lipgloss.Style
	Text         lipgloss.Style
	Muted        lipgloss.Style
	Accent       lipgloss.Style
	Panel        lipgloss.Style
	Border       lipgloss.Style
	Focus        lipgloss.Style
	Success      lipgloss.Style
	Warning      lipgloss.Style
	Error        lipgloss.Style
	Info         lipgloss.Style
	StatusIdle   lipgloss.Style
	StatusWork   lipgloss.Style
	StatusError  lipgloss.Style
	StatusPaused lipgloss.Style
}

// DefaultStyles builds styles from the default theme.
func DefaultStyles() Styles {
	return BuildStyles(DefaultTheme)
}

// BuildStyles converts theme tokens into lipgloss styles.
func BuildStyles(theme Theme) Styles {
	tokens := theme.Tokens

	return Styles{
		Theme:        theme,
		Title:        lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Text)).Bold(true),
		Text:         lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Text)),
		Muted:        lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.TextMuted)),
		Accent:       lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Accent)),
		Panel:        lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Text)).Background(lipgloss.Color(tokens.Panel)).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(tokens.Border)),
		Border:       lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Border)),
		Focus:        lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Focus)).Bold(true),
		Success:      lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Success)),
		Warning:      lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Warning)),
		Error:        lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Error)),
		Info:         lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Info)),
		StatusIdle:   lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.TextMuted)),
		StatusWork:   lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Success)),
		StatusError:  lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Error)),
		StatusPaused: lipgloss.NewStyle().Foreground(lipgloss.Color(tokens.Warning)),
	}
}
