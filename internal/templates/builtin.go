package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// LoadBuiltinTemplates returns the built-in templates bundled with Swarm.
func LoadBuiltinTemplates() ([]*Template, error) {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("read builtin templates: %w", err)
	}

	templates := make([]*Template, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := "builtin/" + entry.Name()
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read builtin template %s: %w", entry.Name(), err)
		}
		tmpl, err := parseTemplate(data)
		if err != nil {
			return nil, fmt.Errorf("parse builtin template %s: %w", entry.Name(), err)
		}
		tmpl.Source = "builtin"
		templates = append(templates, tmpl)
	}

	sort.Slice(templates, func(i, j int) bool {
		return templates[i].Name < templates[j].Name
	})

	return templates, nil
}
