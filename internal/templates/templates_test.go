package templates

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.yaml")

	yaml := `name: example
description: Example template
message: |
  Hello {{.name}}
variables:
  - name: name
    description: Person name
    required: true
`

	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	tmpl, err := LoadTemplate(path)
	if err != nil {
		t.Fatalf("LoadTemplate: %v", err)
	}

	if tmpl.Name != "example" {
		t.Fatalf("expected name example, got %q", tmpl.Name)
	}
	if tmpl.Source != path {
		t.Fatalf("expected source %q, got %q", path, tmpl.Source)
	}
	if len(tmpl.Variables) != 1 || tmpl.Variables[0].Name != "name" {
		t.Fatalf("unexpected variables: %+v", tmpl.Variables)
	}
}

func TestRenderTemplate(t *testing.T) {
	tmpl := &Template{
		Name:    "greet",
		Message: "Hello {{.name | default \"world\"}}",
		Variables: []TemplateVar{
			{Name: "name"},
		},
	}

	rendered, err := RenderTemplate(tmpl, map[string]string{})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if rendered != "Hello world" {
		t.Fatalf("unexpected render result: %q", rendered)
	}

	rendered, err = RenderTemplate(tmpl, map[string]string{"name": "Swarm"})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if rendered != "Hello Swarm" {
		t.Fatalf("unexpected render result: %q", rendered)
	}
}

func TestRenderTemplateRequired(t *testing.T) {
	tmpl := &Template{
		Name:    "required",
		Message: "Hi {{.who}}",
		Variables: []TemplateVar{
			{Name: "who", Required: true},
		},
	}

	if _, err := RenderTemplate(tmpl, map[string]string{}); err == nil {
		t.Fatalf("expected error for missing required variable")
	}

	rendered, err := RenderTemplate(tmpl, map[string]string{"who": "Swarm"})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if rendered != "Hi Swarm" {
		t.Fatalf("unexpected render result: %q", rendered)
	}
}

func TestLoadBuiltinTemplates(t *testing.T) {
	templates, err := LoadBuiltinTemplates()
	if err != nil {
		t.Fatalf("LoadBuiltinTemplates: %v", err)
	}
	if len(templates) < 5 {
		t.Fatalf("expected at least 5 builtin templates, got %d", len(templates))
	}

	for _, tmpl := range templates {
		if tmpl.Source != "builtin" {
			t.Fatalf("expected builtin source, got %q", tmpl.Source)
		}
		if tmpl.Name == "" {
			t.Fatalf("builtin template missing name")
		}
	}
}
