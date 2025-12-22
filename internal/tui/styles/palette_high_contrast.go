package styles

// HighContrastTheme favors visibility on low-contrast terminals.
var HighContrastTheme = Theme{
	Name: "high-contrast",
	Tokens: ThemeTokens{
		Background: "#000000",
		Panel:      "#0A0A0A",
		Text:       "#FFFFFF",
		TextMuted:  "#C0C0C0",
		Border:     "#FFFFFF",
		Accent:     "#00A2FF",
		Focus:      "#FFD400",
		Success:    "#00FF5A",
		Warning:    "#FFB000",
		Error:      "#FF4040",
		Info:       "#66CCFF",
	},
}
