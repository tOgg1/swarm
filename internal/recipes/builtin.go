package recipes

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// LoadBuiltinRecipes returns the built-in recipes bundled with Swarm.
func LoadBuiltinRecipes() ([]*Recipe, error) {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("read builtin recipes: %w", err)
	}

	recipes := make([]*Recipe, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := "builtin/" + entry.Name()
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read builtin recipe %s: %w", entry.Name(), err)
		}
		recipe, err := parseRecipe(data)
		if err != nil {
			return nil, fmt.Errorf("parse builtin recipe %s: %w", entry.Name(), err)
		}
		recipe.Source = "builtin"
		recipes = append(recipes, recipe)
	}

	sort.Slice(recipes, func(i, j int) bool {
		return recipes[i].Name < recipes[j].Name
	})

	return recipes, nil
}
