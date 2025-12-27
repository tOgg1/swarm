// Package templates provides message template loading and rendering.
package templates

// Template represents a single message template.
type Template struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Message     string        `yaml:"message"`
	Variables   []TemplateVar `yaml:"variables,omitempty"`
	Tags        []string      `yaml:"tags,omitempty"`
	Source      string        // file path or "builtin"
}

// TemplateVar describes a variable used in a template.
type TemplateVar struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Default     string `yaml:"default,omitempty"`
	Required    bool   `yaml:"required"`
}
