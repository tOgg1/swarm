package recipes

import (
	"os"
	"path/filepath"
)

// RecipeSearchPaths returns recipe search directories in precedence order.
func RecipeSearchPaths(projectDir string) []string {
	paths := make([]string, 0, 3)
	if projectDir != "" {
		paths = append(paths, filepath.Join(projectDir, ".swarm", "recipes"))
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "swarm", "recipes"))
	}

	paths = append(paths, filepath.Join(string(filepath.Separator), "usr", "share", "swarm", "recipes"))
	return paths
}

// LoadRecipesFromSearchPaths loads recipes from search paths with first-hit precedence.
func LoadRecipesFromSearchPaths(projectDir string) ([]*Recipe, error) {
	paths := RecipeSearchPaths(projectDir)
	seen := make(map[string]*Recipe)
	order := make([]string, 0)

	for _, path := range paths {
		recipes, err := LoadRecipesFromDir(path)
		if err != nil {
			return nil, err
		}
		for _, recipe := range recipes {
			if _, exists := seen[recipe.Name]; exists {
				continue
			}
			seen[recipe.Name] = recipe
			order = append(order, recipe.Name)
		}
	}

	builtins, err := LoadBuiltinRecipes()
	if err != nil {
		return nil, err
	}
	for _, recipe := range builtins {
		if _, exists := seen[recipe.Name]; exists {
			continue
		}
		seen[recipe.Name] = recipe
		order = append(order, recipe.Name)
	}

	resolved := make([]*Recipe, 0, len(order))
	for _, name := range order {
		resolved = append(resolved, seen[name])
	}

	return resolved, nil
}

// FindRecipe loads a specific recipe by name.
func FindRecipe(projectDir, name string) (*Recipe, error) {
	recipes, err := LoadRecipesFromSearchPaths(projectDir)
	if err != nil {
		return nil, err
	}
	for _, recipe := range recipes {
		if recipe.Name == name {
			return recipe, nil
		}
	}
	return nil, ErrRecipeNotFound
}
