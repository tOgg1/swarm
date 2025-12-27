package templates

import (
	"os"
	"path/filepath"
)

// TemplateSearchPaths returns template search directories in precedence order.
func TemplateSearchPaths(projectDir string) []string {
	paths := make([]string, 0, 3)
	if projectDir != "" {
		paths = append(paths, filepath.Join(projectDir, ".swarm", "templates"))
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "swarm", "templates"))
	}

	paths = append(paths, filepath.Join(string(filepath.Separator), "usr", "share", "swarm", "templates"))
	return paths
}

// LoadTemplatesFromSearchPaths loads templates from search paths with first-hit precedence.
func LoadTemplatesFromSearchPaths(projectDir string) ([]*Template, error) {
	paths := TemplateSearchPaths(projectDir)
	seen := make(map[string]*Template)
	order := make([]string, 0)

	for _, path := range paths {
		templates, err := LoadTemplatesFromDir(path)
		if err != nil {
			return nil, err
		}
		for _, tmpl := range templates {
			if _, exists := seen[tmpl.Name]; exists {
				continue
			}
			seen[tmpl.Name] = tmpl
			order = append(order, tmpl.Name)
		}
	}

	builtins, err := LoadBuiltinTemplates()
	if err != nil {
		return nil, err
	}
	for _, tmpl := range builtins {
		if _, exists := seen[tmpl.Name]; exists {
			continue
		}
		seen[tmpl.Name] = tmpl
		order = append(order, tmpl.Name)
	}

	resolved := make([]*Template, 0, len(order))
	for _, name := range order {
		resolved = append(resolved, seen[name])
	}

	return resolved, nil
}
