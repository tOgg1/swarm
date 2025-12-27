package templates

import (
	"fmt"
	"strings"
	"text/template"
)

// RenderTemplate renders a template with the provided variables.
func RenderTemplate(tmpl *Template, vars map[string]string) (string, error) {
	if tmpl == nil {
		return "", fmt.Errorf("template is required")
	}

	data := make(map[string]string, len(vars))
	for key, value := range vars {
		data[key] = value
	}

	for _, variable := range tmpl.Variables {
		value := strings.TrimSpace(data[variable.Name])
		if value == "" {
			if variable.Default != "" {
				data[variable.Name] = variable.Default
				continue
			}
			if variable.Required {
				return "", fmt.Errorf("missing required variable %q", variable.Name)
			}
		}
	}

	parsed, err := template.New(tmpl.Name).
		Funcs(template.FuncMap{"default": defaultValue}).
		Option("missingkey=zero").
		Parse(tmpl.Message)
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", tmpl.Name, err)
	}

	var out strings.Builder
	if err := parsed.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render template %q: %w", tmpl.Name, err)
	}

	return out.String(), nil
}

func defaultValue(def string, value any) string {
	if value == nil {
		return def
	}

	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return def
		}
		return v
	default:
		text := strings.TrimSpace(fmt.Sprint(v))
		if text == "" {
			return def
		}
		return text
	}
}
