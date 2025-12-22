// Package adapters provides the adapter registry for agent CLI integrations.
package adapters

import (
	"fmt"
	"sync"

	"github.com/opencode-ai/swarm/internal/models"
)

// Registry manages registered agent adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]AgentAdapter
}

// NewRegistry creates a new adapter registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]AgentAdapter),
	}
}

// Register adds an adapter to the registry.
// Returns an error if an adapter with the same name is already registered.
func (r *Registry) Register(adapter AgentAdapter) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := adapter.Name()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("adapter %q already registered", name)
	}

	r.adapters[name] = adapter
	return nil
}

// MustRegister adds an adapter to the registry, panicking on error.
func (r *Registry) MustRegister(adapter AgentAdapter) {
	if err := r.Register(adapter); err != nil {
		panic(err)
	}
}

// Get retrieves an adapter by name.
// Returns nil if the adapter is not found.
func (r *Registry) Get(name string) AgentAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.adapters[name]
}

// GetByAgentType retrieves an adapter by agent type.
// Returns nil if no adapter matches.
func (r *Registry) GetByAgentType(agentType models.AgentType) AgentAdapter {
	return r.Get(string(agentType))
}

// List returns all registered adapters.
func (r *Registry) List() []AgentAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()

	adapters := make([]AgentAdapter, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		adapters = append(adapters, adapter)
	}
	return adapters
}

// Names returns the names of all registered adapters.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}

// Unregister removes an adapter from the registry.
// Returns true if the adapter was removed, false if it wasn't found.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.adapters[name]; exists {
		delete(r.adapters, name)
		return true
	}
	return false
}

// DefaultRegistry is the global adapter registry.
var DefaultRegistry = NewRegistry()

// Register adds an adapter to the default registry.
func Register(adapter AgentAdapter) error {
	return DefaultRegistry.Register(adapter)
}

// MustRegister adds an adapter to the default registry, panicking on error.
func MustRegister(adapter AgentAdapter) {
	DefaultRegistry.MustRegister(adapter)
}

// Get retrieves an adapter from the default registry by name.
func Get(name string) AgentAdapter {
	return DefaultRegistry.Get(name)
}

// GetByAgentType retrieves an adapter from the default registry by agent type.
func GetByAgentType(agentType models.AgentType) AgentAdapter {
	return DefaultRegistry.GetByAgentType(agentType)
}

// List returns all adapters from the default registry.
func List() []AgentAdapter {
	return DefaultRegistry.List()
}

// Names returns the names of all adapters in the default registry.
func Names() []string {
	return DefaultRegistry.Names()
}
