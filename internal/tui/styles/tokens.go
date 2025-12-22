package styles

// ThemeTokens defines the semantic color roles for the TUI.
type ThemeTokens struct {
	Background string
	Panel      string
	Text       string
	TextMuted  string
	Border     string
	Accent     string
	Focus      string
	Success    string
	Warning    string
	Error      string
	Info       string
}

// Theme bundles a palette with a name.
type Theme struct {
	Name   string
	Tokens ThemeTokens
}

// Themes lists available palettes by name.
var Themes = map[string]Theme{
	"default":       DefaultTheme,
	"high-contrast": HighContrastTheme,
}
