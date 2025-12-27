// Package recipes provides recipe loading and execution for mass agent spawning.
package recipes

import (
	"errors"
	"fmt"

	"github.com/opencode-ai/swarm/internal/models"
)

var (
	// ErrRecipeNameRequired is returned when a recipe has no name.
	ErrRecipeNameRequired = errors.New("recipe name is required")
	// ErrRecipeNoAgents is returned when a recipe has no agent specs.
	ErrRecipeNoAgents = errors.New("recipe must have at least one agent spec")
	// ErrRecipeNotFound is returned when a recipe is not found.
	ErrRecipeNotFound = errors.New("recipe not found")
)

// RecipeValidationError describes a validation error in a recipe.
type RecipeValidationError struct {
	Field   string
	Index   int
	Message string
}

func (e *RecipeValidationError) Error() string {
	if e.Index >= 0 {
		return fmt.Sprintf("recipe %s[%d]: %s", e.Field, e.Index, e.Message)
	}
	return fmt.Sprintf("recipe %s: %s", e.Field, e.Message)
}

// Recipe defines a configuration for spawning multiple agents with initial tasking.
type Recipe struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Agents      []AgentSpec `yaml:"agents"`
	Profiles    []string    `yaml:"profile_rotation_order,omitempty"`
	Source      string      // file path or "builtin"
}

// AgentSpec defines configuration for a group of agents within a recipe.
type AgentSpec struct {
	Count           int               `yaml:"count"`
	Type            models.AgentType  `yaml:"type"`
	Profile         string            `yaml:"profile,omitempty"`
	ProfileRotation string            `yaml:"profile_rotation,omitempty"` // round-robin, random, balanced
	InitialSequence string            `yaml:"initial_sequence,omitempty"`
	InitialTemplate string            `yaml:"initial_template,omitempty"`
	Environment     map[string]string `yaml:"environment,omitempty"`
}

// ProfileRotationStrategy defines how profiles are assigned to agents.
type ProfileRotationStrategy string

const (
	// ProfileRotationRoundRobin cycles through profiles in order.
	ProfileRotationRoundRobin ProfileRotationStrategy = "round-robin"
	// ProfileRotationRandom selects profiles randomly.
	ProfileRotationRandom ProfileRotationStrategy = "random"
	// ProfileRotationBalanced prefers profiles with most remaining cooldown time.
	ProfileRotationBalanced ProfileRotationStrategy = "balanced"
)

// TotalAgents returns the total number of agents this recipe will spawn.
func (r *Recipe) TotalAgents() int {
	total := 0
	for _, spec := range r.Agents {
		total += spec.Count
	}
	return total
}

// Validate checks that the recipe has valid configuration.
func (r *Recipe) Validate() error {
	if r.Name == "" {
		return ErrRecipeNameRequired
	}
	if len(r.Agents) == 0 {
		return ErrRecipeNoAgents
	}
	for i, spec := range r.Agents {
		if spec.Count <= 0 {
			return &RecipeValidationError{
				Field:   "agents",
				Index:   i,
				Message: "count must be positive",
			}
		}
		if spec.Type == "" {
			return &RecipeValidationError{
				Field:   "agents",
				Index:   i,
				Message: "type is required",
			}
		}
	}
	return nil
}
