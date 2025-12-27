package recipes

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadRecipe reads a single recipe from disk.
func LoadRecipe(path string) (*Recipe, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("recipe path is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read recipe %s: %w", path, err)
	}

	recipe, err := parseRecipe(data)
	if err != nil {
		return nil, fmt.Errorf("parse recipe %s: %w", path, err)
	}
	recipe.Source = path
	return recipe, nil
}

// LoadRecipesFromDir loads all recipes from a directory.
func LoadRecipesFromDir(dir string) ([]*Recipe, error) {
	if strings.TrimSpace(dir) == "" {
		return []*Recipe{}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Recipe{}, nil
		}
		return nil, fmt.Errorf("read recipes dir %s: %w", dir, err)
	}

	recipes := make([]*Recipe, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, name)
		recipe, err := LoadRecipe(path)
		if err != nil {
			return nil, err
		}
		recipes = append(recipes, recipe)
	}

	sort.Slice(recipes, func(i, j int) bool {
		return recipes[i].Name < recipes[j].Name
	})

	return recipes, nil
}

func parseRecipe(data []byte) (*Recipe, error) {
	var recipe Recipe
	if err := yaml.Unmarshal(data, &recipe); err != nil {
		return nil, err
	}

	recipe.Name = strings.TrimSpace(recipe.Name)
	if err := recipe.Validate(); err != nil {
		return nil, err
	}

	return &recipe, nil
}
