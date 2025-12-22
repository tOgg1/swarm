// Package scheduler provides the message dispatch scheduler for Swarm agents.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/queue"
	"github.com/opencode-ai/swarm/internal/state"
	"github.com/rs/zerolog"
)

// Scheduler errors.
var (
	ErrSchedulerAlreadyRunning = errors.New("scheduler already running")
	ErrSchedulerNotRunning     = errors.New("scheduler not running")
	ErrAgentNotFound           = errors.New("agent not found")
	ErrAgentPaused             = errors.New("agent is paused")
	ErrAgentNotEligible        = errors.New("agent is not eligible for dispatch")
	ErrQueueEmpty              = errors.New("queue is empty")
	ErrDispatchFailed          = errors.New("dispatch failed")
)

// Config contains scheduler configuration.
type Config struct {
	// TickInterval is how often the scheduler checks for work.
	// Default: 1 second.
	TickInterval time.Duration

	// DispatchTimeout is the maximum time allowed for a single dispatch.
	// Default: 30 seconds.
	DispatchTimeout time.Duration

	// MaxConcurrentDispatches limits how many dispatches can happen at once.
	// Default: 10.
	MaxConcurrentDispatches int

	// IdleStateRequired requires agents to be idle before dispatch.
	// Default: true.
	IdleStateRequired bool

	// AutoResumeEnabled enables automatic resume of paused agents.
	// Default: true.
	AutoResumeEnabled bool
}

// DefaultConfig returns sensible default configuration.
func DefaultConfig() Config {
	return Config{
		TickInterval:            1 * time.Second,
		DispatchTimeout:         30 * time.Second,
		MaxConcurrentDispatches: 10,
		IdleStateRequired:       true,
		AutoResumeEnabled:       true,
	}
}

// DispatchEvent represents a dispatch action taken by the scheduler.
type DispatchEvent struct {
	// AgentID is the agent that received the dispatch.
	AgentID string

	// ItemID is the queue item that was dispatched.
	ItemID string

	// ItemType is the type of item dispatched.
	ItemType models.QueueItemType

	// Success indicates if the dispatch succeeded.
	Success bool

	// Error contains error details if dispatch failed.
	Error string

	// Timestamp is when the dispatch occurred.
	Timestamp time.Time

	// Duration is how long the dispatch took.
	Duration time.Duration
}

// SchedulerStats contains scheduler statistics.
type SchedulerStats struct {
	// Running indicates if the scheduler is active.
	Running bool

	// Paused indicates if the scheduler is paused.
	Paused bool

	// StartedAt is when the scheduler was started.
	StartedAt *time.Time

	// TotalDispatches is the total number of dispatches attempted.
	TotalDispatches int64

	// SuccessfulDispatches is the number of successful dispatches.
	SuccessfulDispatches int64

	// FailedDispatches is the number of failed dispatches.
	FailedDispatches int64

	// LastDispatchAt is when the last dispatch occurred.
	LastDispatchAt *time.Time

	// PausedAgents is the count of currently paused agents.
	PausedAgents int
}

// Scheduler manages message dispatch to agents.
type Scheduler struct {
	config       Config
	agentService *agent.Service
	queueService queue.QueueService
	stateEngine  *state.Engine
	logger       zerolog.Logger

	// Runtime state
	mu           sync.RWMutex
	running      bool
	paused       bool
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	dispatchSem  chan struct{}
	scheduleNow  chan string // channel to trigger immediate dispatch for an agent
	pausedAgents map[string]struct{}

	// Stats
	stats      SchedulerStats
	statsMu    sync.RWMutex
	dispatchCh chan DispatchEvent
}

// New creates a new Scheduler.
func New(config Config, agentService *agent.Service, queueService queue.QueueService, stateEngine *state.Engine) *Scheduler {
	if config.TickInterval <= 0 {
		config.TickInterval = DefaultConfig().TickInterval
	}
	if config.DispatchTimeout <= 0 {
		config.DispatchTimeout = DefaultConfig().DispatchTimeout
	}
	if config.MaxConcurrentDispatches <= 0 {
		config.MaxConcurrentDispatches = DefaultConfig().MaxConcurrentDispatches
	}

	return &Scheduler{
		config:       config,
		agentService: agentService,
		queueService: queueService,
		stateEngine:  stateEngine,
		logger:       logging.Component("scheduler"),
		dispatchSem:  make(chan struct{}, config.MaxConcurrentDispatches),
		scheduleNow:  make(chan string, 100),
		pausedAgents: make(map[string]struct{}),
		dispatchCh:   make(chan DispatchEvent, 100),
	}
}

// Start begins the scheduler's background processing loop.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return ErrSchedulerAlreadyRunning
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	s.running = true
	s.paused = false

	now := time.Now().UTC()
	s.statsMu.Lock()
	s.stats.Running = true
	s.stats.Paused = false
	s.stats.StartedAt = &now
	s.statsMu.Unlock()

	s.logger.Info().
		Dur("tick_interval", s.config.TickInterval).
		Int("max_concurrent", s.config.MaxConcurrentDispatches).
		Msg("scheduler starting")

	// Start the main scheduling loop
	s.wg.Add(1)
	go s.runLoop()

	// Subscribe to state changes for auto-dispatch on idle
	if s.stateEngine != nil {
		if err := s.stateEngine.SubscribeFunc("scheduler", s.onStateChange); err != nil {
			s.logger.Warn().Err(err).Msg("failed to subscribe to state changes")
		}
	}

	return nil
}

// Stop halts the scheduler and waits for pending work to complete.
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return ErrSchedulerNotRunning
	}

	s.logger.Info().Msg("scheduler stopping")

	// Unsubscribe from state changes
	if s.stateEngine != nil {
		_ = s.stateEngine.Unsubscribe("scheduler")
	}

	// Cancel the context and wait for goroutines
	s.cancel()
	s.running = false
	s.mu.Unlock()

	// Wait for all goroutines to finish
	s.wg.Wait()

	s.statsMu.Lock()
	s.stats.Running = false
	s.statsMu.Unlock()

	s.logger.Info().Msg("scheduler stopped")
	return nil
}

// ScheduleNow triggers an immediate dispatch attempt for a specific agent.
// This bypasses the normal tick interval.
func (s *Scheduler) ScheduleNow(agentID string) error {
	s.mu.RLock()
	running := s.running
	paused := s.paused
	s.mu.RUnlock()

	if !running {
		return ErrSchedulerNotRunning
	}
	if paused {
		return ErrSchedulerNotRunning
	}

	select {
	case s.scheduleNow <- agentID:
		s.logger.Debug().Str("agent_id", agentID).Msg("immediate dispatch triggered")
		return nil
	default:
		// Channel full, schedule will happen on next tick anyway
		s.logger.Debug().Str("agent_id", agentID).Msg("schedule channel full, will dispatch on next tick")
		return nil
	}
}

// Pause temporarily suspends the scheduler without stopping it.
func (s *Scheduler) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return ErrSchedulerNotRunning
	}
	if s.paused {
		return nil // Already paused
	}

	s.paused = true
	s.statsMu.Lock()
	s.stats.Paused = true
	s.statsMu.Unlock()

	s.logger.Info().Msg("scheduler paused")
	return nil
}

// Resume resumes a paused scheduler.
func (s *Scheduler) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return ErrSchedulerNotRunning
	}
	if !s.paused {
		return nil // Already running
	}

	s.paused = false
	s.statsMu.Lock()
	s.stats.Paused = false
	s.statsMu.Unlock()

	s.logger.Info().Msg("scheduler resumed")
	return nil
}

// PauseAgent pauses scheduling for a specific agent.
func (s *Scheduler) PauseAgent(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pausedAgents[agentID] = struct{}{}

	s.statsMu.Lock()
	s.stats.PausedAgents = len(s.pausedAgents)
	s.statsMu.Unlock()

	s.logger.Debug().Str("agent_id", agentID).Msg("agent paused in scheduler")
	return nil
}

// ResumeAgent resumes scheduling for a specific agent.
func (s *Scheduler) ResumeAgent(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.pausedAgents, agentID)

	s.statsMu.Lock()
	s.stats.PausedAgents = len(s.pausedAgents)
	s.statsMu.Unlock()

	s.logger.Debug().Str("agent_id", agentID).Msg("agent resumed in scheduler")
	return nil
}

// IsAgentPaused checks if an agent is paused in the scheduler.
func (s *Scheduler) IsAgentPaused(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, paused := s.pausedAgents[agentID]
	return paused
}

// Stats returns current scheduler statistics.
func (s *Scheduler) Stats() SchedulerStats {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	return s.stats
}

// DispatchEvents returns the channel of dispatch events.
// Consumers should read from this channel to receive dispatch notifications.
func (s *Scheduler) DispatchEvents() <-chan DispatchEvent {
	return s.dispatchCh
}

// runLoop is the main scheduling loop.
func (s *Scheduler) runLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return

		case agentID := <-s.scheduleNow:
			// Immediate dispatch request
			s.mu.RLock()
			paused := s.paused
			s.mu.RUnlock()

			if !paused {
				s.tryDispatch(agentID)
			}

		case <-ticker.C:
			// Regular tick
			s.mu.RLock()
			paused := s.paused
			s.mu.RUnlock()

			if !paused {
				s.tick()
			}
		}
	}
}

// tick performs one scheduling cycle.
func (s *Scheduler) tick() {
	ctx := s.ctx

	// Get all agents
	agents, err := s.agentService.ListAgents(ctx, agent.ListAgentsOptions{
		IncludeQueueLength: true,
	})
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to list agents")
		return
	}

	// Check for auto-resume of paused agents
	if s.config.AutoResumeEnabled {
		s.checkAutoResume(ctx, agents)
	}

	// Find eligible agents and dispatch
	for _, a := range agents {
		if s.isEligibleForDispatch(a) {
			s.tryDispatch(a.ID)
		}
	}
}

// checkAutoResume checks for agents that should auto-resume.
func (s *Scheduler) checkAutoResume(ctx context.Context, agents []*models.Agent) {
	now := time.Now().UTC()

	for _, a := range agents {
		if a.State == models.AgentStatePaused && a.PausedUntil != nil {
			if now.After(*a.PausedUntil) {
				s.logger.Debug().
					Str("agent_id", a.ID).
					Time("paused_until", *a.PausedUntil).
					Msg("auto-resuming agent")

				if err := s.agentService.ResumeAgent(ctx, a.ID); err != nil {
					s.logger.Warn().Err(err).Str("agent_id", a.ID).Msg("failed to auto-resume agent")
				} else {
					// Also resume in scheduler
					s.ResumeAgent(a.ID)
				}
			}
		}
	}
}

// isEligibleForDispatch checks if an agent is eligible for dispatch.
func (s *Scheduler) isEligibleForDispatch(a *models.Agent) bool {
	// Check if agent is paused in scheduler
	if s.IsAgentPaused(a.ID) {
		return false
	}

	// Check agent state
	if a.State == models.AgentStatePaused {
		return false
	}
	if a.State == models.AgentStateStopped {
		return false
	}

	// If idle state is required, check for idle
	if s.config.IdleStateRequired && a.State != models.AgentStateIdle {
		return false
	}

	// Check if there's anything in the queue
	if a.QueueLength <= 0 {
		return false
	}

	return true
}

// tryDispatch attempts to dispatch the next item to an agent.
func (s *Scheduler) tryDispatch(agentID string) {
	// Acquire dispatch semaphore
	select {
	case s.dispatchSem <- struct{}{}:
	default:
		// Max concurrent dispatches reached
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.dispatchSem }()

		s.dispatchToAgent(agentID)
	}()
}

// dispatchToAgent dispatches the next queue item to an agent.
func (s *Scheduler) dispatchToAgent(agentID string) {
	ctx, cancel := context.WithTimeout(s.ctx, s.config.DispatchTimeout)
	defer cancel()

	// Guard against nil queue service
	if s.queueService == nil {
		return
	}

	startTime := time.Now()
	var event *DispatchEvent

	defer func() {
		if event != nil {
			event.Duration = time.Since(startTime)
			s.recordDispatch(*event)
		}
	}()

	// Get the next item from the queue
	item, err := s.queueService.Dequeue(ctx, agentID)
	if err != nil {
		if errors.Is(err, queue.ErrQueueEmpty) {
			// No items to dispatch, not an error - don't record
			return
		}
		event = &DispatchEvent{
			AgentID:   agentID,
			Timestamp: startTime,
			Success:   false,
			Error:     fmt.Sprintf("failed to dequeue: %v", err),
		}
		s.logger.Error().Err(err).Str("agent_id", agentID).Msg("failed to dequeue item")
		return
	}

	// Initialize event now that we have an item
	event = &DispatchEvent{
		AgentID:   agentID,
		Timestamp: startTime,
		ItemID:    item.ID,
		ItemType:  item.Type,
	}

	// Handle different item types
	switch item.Type {
	case models.QueueItemTypeMessage:
		err = s.dispatchMessage(ctx, agentID, item)
	case models.QueueItemTypePause:
		err = s.dispatchPause(ctx, agentID, item)
	case models.QueueItemTypeConditional:
		err = s.dispatchConditional(ctx, agentID, item)
	default:
		err = fmt.Errorf("unknown item type: %s", item.Type)
	}

	if err != nil {
		event.Success = false
		event.Error = err.Error()
		s.logger.Error().
			Err(err).
			Str("agent_id", agentID).
			Str("item_id", item.ID).
			Str("item_type", string(item.Type)).
			Msg("dispatch failed")
	} else {
		event.Success = true
		s.logger.Info().
			Str("agent_id", agentID).
			Str("item_id", item.ID).
			Str("item_type", string(item.Type)).
			Msg("dispatch successful")
	}
}

// dispatchMessage sends a message to an agent.
func (s *Scheduler) dispatchMessage(ctx context.Context, agentID string, item *models.QueueItem) error {
	var payload models.MessagePayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal message payload: %w", err)
	}

	// Send via agent service
	opts := &agent.SendMessageOptions{
		SkipIdleCheck: false, // Respect idle check
	}
	if err := s.agentService.SendMessage(ctx, agentID, payload.Text, opts); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// dispatchPause pauses an agent for a duration.
func (s *Scheduler) dispatchPause(ctx context.Context, agentID string, item *models.QueueItem) error {
	var payload models.PausePayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal pause payload: %w", err)
	}

	duration := time.Duration(payload.DurationSeconds) * time.Second

	// Pause the agent
	if err := s.agentService.PauseAgent(ctx, agentID, duration); err != nil {
		return fmt.Errorf("failed to pause agent: %w", err)
	}

	// Also pause in scheduler
	s.PauseAgent(agentID)

	s.logger.Debug().
		Str("agent_id", agentID).
		Dur("duration", duration).
		Str("reason", payload.Reason).
		Msg("agent paused by queue item")

	return nil
}

// dispatchConditional handles conditional dispatch.
func (s *Scheduler) dispatchConditional(ctx context.Context, agentID string, item *models.QueueItem) error {
	var payload models.ConditionalPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal conditional payload: %w", err)
	}

	// Check the condition
	conditionMet, err := s.evaluateCondition(ctx, agentID, payload)
	if err != nil {
		return fmt.Errorf("failed to evaluate condition: %w", err)
	}

	if !conditionMet {
		// Re-queue the item for later evaluation
		// Note: We already dequeued, so we need to re-add it
		if err := s.queueService.InsertAt(ctx, agentID, 0, item); err != nil {
			return fmt.Errorf("failed to re-queue conditional item: %w", err)
		}
		return nil
	}

	// Condition met, send the message
	opts := &agent.SendMessageOptions{
		SkipIdleCheck: false,
	}
	if err := s.agentService.SendMessage(ctx, agentID, payload.Message, opts); err != nil {
		return fmt.Errorf("failed to send conditional message: %w", err)
	}

	return nil
}

// evaluateCondition evaluates a conditional payload.
func (s *Scheduler) evaluateCondition(ctx context.Context, agentID string, payload models.ConditionalPayload) (bool, error) {
	switch payload.ConditionType {
	case models.ConditionTypeWhenIdle:
		// Check if agent is idle
		a, err := s.agentService.GetAgent(ctx, agentID)
		if err != nil {
			return false, err
		}
		return a.State == models.AgentStateIdle, nil

	case models.ConditionTypeAfterCooldown:
		// Check if enough time has passed since last activity
		a, err := s.agentService.GetAgent(ctx, agentID)
		if err != nil {
			return false, err
		}
		if a.LastActivity == nil {
			return true, nil // No last activity, condition met
		}
		// Default 30 second cooldown
		cooldown := 30 * time.Second
		return time.Since(*a.LastActivity) >= cooldown, nil

	case models.ConditionTypeAfterPrevious:
		// This condition is met when the previous item completed
		// Since we're processing in order, this is always true
		return true, nil

	case models.ConditionTypeCustomExpression:
		// Custom expressions not yet implemented
		s.logger.Warn().
			Str("agent_id", agentID).
			Str("expression", payload.Expression).
			Msg("custom expressions not yet implemented")
		return false, fmt.Errorf("custom expressions not implemented")

	default:
		return false, fmt.Errorf("unknown condition type: %s", payload.ConditionType)
	}
}

// recordDispatch records a dispatch event in stats.
func (s *Scheduler) recordDispatch(event DispatchEvent) {
	// Update stats
	s.statsMu.Lock()
	s.stats.TotalDispatches++
	if event.Success {
		s.stats.SuccessfulDispatches++
	} else {
		s.stats.FailedDispatches++
	}
	now := event.Timestamp
	s.stats.LastDispatchAt = &now
	s.statsMu.Unlock()

	// Send to dispatch channel (non-blocking)
	select {
	case s.dispatchCh <- event:
	default:
		// Channel full, drop event
	}
}

// onStateChange handles agent state change notifications.
func (s *Scheduler) onStateChange(change state.StateChange) {
	// When an agent becomes idle, try to dispatch
	if change.CurrentState == models.AgentStateIdle {
		s.logger.Debug().
			Str("agent_id", change.AgentID).
			Str("from_state", string(change.PreviousState)).
			Msg("agent became idle, triggering dispatch check")

		_ = s.ScheduleNow(change.AgentID)
	}
}
