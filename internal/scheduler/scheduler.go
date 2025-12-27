// Package scheduler provides the message dispatch scheduler for Swarm agents.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/swarm/internal/account"
	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/events"
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
	ErrAccountOnCooldown       = errors.New("account is on cooldown")
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

	// MaxRetries is the maximum number of dispatch retries.
	// Default: 3.
	MaxRetries int

	// RetryBackoff is the base backoff duration for retries.
	// Default: 5 seconds.
	RetryBackoff time.Duration

	// DefaultCooldownDuration is the default pause duration after rate limiting.
	// Default: 5 minutes.
	DefaultCooldownDuration time.Duration
}

// DefaultConfig returns sensible default configuration.
func DefaultConfig() Config {
	return Config{
		TickInterval:            1 * time.Second,
		DispatchTimeout:         30 * time.Second,
		MaxConcurrentDispatches: 10,
		IdleStateRequired:       true,
		AutoResumeEnabled:       true,
		MaxRetries:              3,
		RetryBackoff:            5 * time.Second,
		DefaultCooldownDuration: 5 * time.Minute,
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
	config         Config
	agentService   *agent.Service
	queueService   queue.QueueService
	stateEngine    *state.Engine
	accountService *account.Service
	publisher      events.Publisher
	logger         zerolog.Logger

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
	retryAfter   map[string]time.Time

	// Per-agent dispatch locks to prevent concurrent dispatch to the same agent.
	// Key: agentID, Value: mutex for that agent's dispatch operations.
	agentDispatchMu sync.Map // map[string]*sync.Mutex

	// Stats
	stats      SchedulerStats
	statsMu    sync.RWMutex
	dispatchCh chan DispatchEvent
}

// Option configures a Scheduler.
type Option func(*Scheduler)

// WithPublisher sets the event publisher for the scheduler.
func WithPublisher(publisher events.Publisher) Option {
	return func(s *Scheduler) {
		s.publisher = publisher
	}
}

// New creates a new Scheduler.
func New(config Config, agentService *agent.Service, queueService queue.QueueService, stateEngine *state.Engine, accountService *account.Service, opts ...Option) *Scheduler {
	if config.TickInterval <= 0 {
		config.TickInterval = DefaultConfig().TickInterval
	}
	if config.DispatchTimeout <= 0 {
		config.DispatchTimeout = DefaultConfig().DispatchTimeout
	}
	if config.MaxConcurrentDispatches <= 0 {
		config.MaxConcurrentDispatches = DefaultConfig().MaxConcurrentDispatches
	}
	if config.MaxRetries < 0 {
		config.MaxRetries = 0
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = DefaultConfig().RetryBackoff
	}
	if config.DefaultCooldownDuration <= 0 {
		config.DefaultCooldownDuration = DefaultConfig().DefaultCooldownDuration
	}

	s := &Scheduler{
		config:         config,
		agentService:   agentService,
		queueService:   queueService,
		stateEngine:    stateEngine,
		accountService: accountService,
		logger:         logging.Component("scheduler"),
		dispatchSem:    make(chan struct{}, config.MaxConcurrentDispatches),
		scheduleNow:    make(chan string, 100),
		pausedAgents:   make(map[string]struct{}),
		retryAfter:     make(map[string]time.Time),
		dispatchCh:     make(chan DispatchEvent, 100),
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
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

func (s *Scheduler) setRetryAfter(agentID string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryAfter[agentID] = until
}

func (s *Scheduler) clearRetryAfter(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.retryAfter, agentID)
}

func (s *Scheduler) isRetryBackoffActive(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	until, ok := s.retryAfter[agentID]
	if !ok {
		return false
	}
	if time.Now().UTC().Before(until) {
		return true
	}

	delete(s.retryAfter, agentID)
	return false
}

// getAgentDispatchMutex returns the mutex for dispatching to a specific agent.
// Creates a new mutex if one doesn't exist for this agent.
func (s *Scheduler) getAgentDispatchMutex(agentID string) *sync.Mutex {
	actual, _ := s.agentDispatchMu.LoadOrStore(agentID, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// tryLockAgentDispatch attempts to acquire the dispatch lock for an agent.
// Returns true if the lock was acquired, false if another dispatch is in progress.
func (s *Scheduler) tryLockAgentDispatch(agentID string) bool {
	mu := s.getAgentDispatchMutex(agentID)
	return mu.TryLock()
}

// unlockAgentDispatch releases the dispatch lock for an agent.
func (s *Scheduler) unlockAgentDispatch(agentID string) {
	mu := s.getAgentDispatchMutex(agentID)
	mu.Unlock()
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
	if s.isRetryBackoffActive(a.ID) {
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
	// Try to acquire the per-agent dispatch lock first.
	// This ensures only one dispatch happens per agent at a time.
	if !s.tryLockAgentDispatch(agentID) {
		// Another dispatch is already in progress for this agent.
		// This is expected behavior, not an error.
		s.logger.Debug().
			Str("agent_id", agentID).
			Msg("dispatch skipped: another dispatch already in progress for this agent")
		return
	}

	// Acquire global dispatch semaphore to limit total concurrent dispatches
	select {
	case s.dispatchSem <- struct{}{}:
	default:
		// Max concurrent dispatches reached, release agent lock
		s.unlockAgentDispatch(agentID)
		s.logger.Debug().
			Str("agent_id", agentID).
			Msg("dispatch skipped: max concurrent dispatches reached")
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.dispatchSem }()
		defer s.unlockAgentDispatch(agentID)

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

	// Check account cooldown before dequeuing
	if s.agentService == nil {
		s.logger.Warn().Str("agent_id", agentID).Msg("agent service unavailable, skipping dispatch")
		return
	}

	var agentInfo *models.Agent
	if s.config.IdleStateRequired || s.accountService != nil {
		var err error
		agentInfo, err = s.agentService.GetAgent(ctx, agentID)
		if err != nil {
			s.logger.Error().Err(err).Str("agent_id", agentID).Msg("failed to get agent for dispatch check")
			return
		}
	}

	if s.config.IdleStateRequired && agentInfo != nil && agentInfo.State != models.AgentStateIdle {
		s.logger.Debug().
			Str("agent_id", agentID).
			Str("state", string(agentInfo.State)).
			Msg("agent not idle, skipping dispatch")
		return
	}

	// Check account cooldown before dequeuing
	if s.accountService != nil && agentInfo != nil && agentInfo.AccountID != "" {
		onCooldown, remaining, err := s.accountService.IsOnCooldown(ctx, agentInfo.AccountID)
		if err != nil && !errors.Is(err, account.ErrAccountNotFound) {
			s.logger.Error().Err(err).
				Str("agent_id", agentID).
				Str("account_id", agentInfo.AccountID).
				Msg("failed to check account cooldown")
			return
		}

		if onCooldown {
			// Try to rotate to another account
			rotated, rotateErr := s.accountService.RotateAccountForAgent(ctx, agentInfo.AccountID, agentID, "cooldown")
			if rotateErr != nil {
				s.logger.Debug().
					Str("agent_id", agentID).
					Str("account_id", agentInfo.AccountID).
					Dur("cooldown_remaining", remaining).
					Msg("account on cooldown, no rotation available, skipping dispatch")
				return
			}

			if s.agentService == nil {
				s.logger.Warn().
					Str("agent_id", agentID).
					Str("account_id", agentInfo.AccountID).
					Msg("agent service unavailable; cannot restart with rotated account")
				return
			}

			fromAccount := agentInfo.AccountID
			if _, err := s.agentService.RestartAgentWithAccount(ctx, agentID, rotated.ID); err != nil {
				s.logger.Warn().
					Err(err).
					Str("agent_id", agentID).
					Str("from_account", fromAccount).
					Str("to_account", rotated.ID).
					Msg("failed to restart agent with rotated account")
				return
			}

			agentInfo.AccountID = rotated.ID
			s.logger.Info().
				Str("agent_id", agentID).
				Str("from_account", fromAccount).
				Str("to_account", rotated.ID).
				Msg("rotated to available account and restarted agent")
		}
	}

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

	// Publish message.dispatched event
	s.publishEvent(ctx, models.EventTypeMessageDispatched, models.EntityTypeQueue, item.ID, models.MessageDispatchedPayload{
		QueueItemID: item.ID,
		ItemType:    item.Type,
		AgentID:     agentID,
	})

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

		// Publish message.failed event
		s.publishEvent(ctx, models.EventTypeMessageFailed, models.EntityTypeQueue, item.ID, models.MessageFailedPayload{
			QueueItemID: item.ID,
			ItemType:    item.Type,
			AgentID:     agentID,
			Error:       err.Error(),
			Attempts:    item.Attempts + 1,
		})

		if retryErr := s.handleDispatchFailure(context.Background(), agentID, item, err); retryErr != nil {
			s.logger.Warn().
				Err(retryErr).
				Str("agent_id", agentID).
				Str("item_id", item.ID).
				Msg("failed to schedule dispatch retry")
		}
	} else {
		event.Success = true
		s.clearRetryAfter(agentID)
		s.logger.Info().
			Str("agent_id", agentID).
			Str("item_id", item.ID).
			Str("item_type", string(item.Type)).
			Msg("dispatch successful")

		// Publish message.completed event
		s.publishEvent(ctx, models.EventTypeMessageCompleted, models.EntityTypeQueue, item.ID, models.MessageCompletedPayload{
			QueueItemID: item.ID,
			ItemType:    item.Type,
			AgentID:     agentID,
			Duration:    time.Since(startTime).String(),
		})
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

// MaxConditionalEvaluations is the maximum number of times a conditional item
// can be re-evaluated before being skipped.
const MaxConditionalEvaluations = 100

// dispatchConditional handles conditional dispatch.
func (s *Scheduler) dispatchConditional(ctx context.Context, agentID string, item *models.QueueItem) error {
	var payload models.ConditionalPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal conditional payload: %w", err)
	}

	// Check evaluation count to prevent infinite loops
	if item.EvaluationCount >= MaxConditionalEvaluations {
		s.logger.Warn().
			Str("agent_id", agentID).
			Str("item_id", item.ID).
			Int("evaluation_count", item.EvaluationCount).
			Msg("conditional item exceeded max evaluations, skipping")

		// Mark as skipped
		if err := s.queueService.UpdateStatus(ctx, item.ID, models.QueueItemStatusSkipped, "max evaluations exceeded"); err != nil {
			s.logger.Warn().Err(err).Str("item_id", item.ID).Msg("failed to mark conditional as skipped")
		}
		return nil
	}

	// Build condition context
	agentInfo, err := s.agentService.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	condCtx := ConditionContext{
		Agent:       agentInfo,
		QueueLength: agentInfo.QueueLength,
		Now:         time.Now().UTC(),
	}

	// Evaluate the condition using the new evaluator
	evaluator := NewConditionEvaluator()
	result, err := evaluator.Evaluate(ctx, condCtx, payload)
	if err != nil {
		s.logger.Warn().
			Err(err).
			Str("agent_id", agentID).
			Str("condition_type", string(payload.ConditionType)).
			Str("expression", payload.Expression).
			Msg("condition evaluation error")
		return fmt.Errorf("failed to evaluate condition: %w", err)
	}

	if !result.Met {
		// Increment evaluation count and re-queue
		item.EvaluationCount++

		s.logger.Debug().
			Str("agent_id", agentID).
			Str("item_id", item.ID).
			Str("condition_type", string(payload.ConditionType)).
			Str("reason", result.Reason).
			Int("evaluation_count", item.EvaluationCount).
			Msg("condition not met, re-queueing")

		// Re-queue the item for later evaluation
		if err := s.queueService.InsertAt(ctx, agentID, 0, item); err != nil {
			return fmt.Errorf("failed to re-queue conditional item: %w", err)
		}
		return nil
	}

	s.logger.Debug().
		Str("agent_id", agentID).
		Str("item_id", item.ID).
		Str("condition_type", string(payload.ConditionType)).
		Str("reason", result.Reason).
		Msg("condition met, dispatching message")

	// Condition met, send the message
	opts := &agent.SendMessageOptions{
		SkipIdleCheck: false,
	}
	if err := s.agentService.SendMessage(ctx, agentID, payload.Message, opts); err != nil {
		return fmt.Errorf("failed to send conditional message: %w", err)
	}

	return nil
}

func (s *Scheduler) handleDispatchFailure(ctx context.Context, agentID string, item *models.QueueItem, dispatchErr error) error {
	if s.queueService == nil || item == nil {
		return nil
	}

	attempts := item.Attempts + 1
	maxRetries := s.config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	if attempts <= maxRetries {
		if err := s.queueService.UpdateAttempts(ctx, item.ID, attempts); err != nil {
			return err
		}
		if err := s.queueService.UpdateStatus(ctx, item.ID, models.QueueItemStatusPending, dispatchErr.Error()); err != nil {
			return err
		}

		backoff := s.retryBackoff(attempts)
		s.setRetryAfter(agentID, time.Now().UTC().Add(backoff))
		s.logger.Warn().
			Str("agent_id", agentID).
			Str("item_id", item.ID).
			Int("attempt", attempts).
			Int("max_retries", maxRetries).
			Dur("backoff", backoff).
			Msg("dispatch failed; retry scheduled")
		return nil
	}

	if err := s.queueService.UpdateAttempts(ctx, item.ID, attempts); err != nil {
		return err
	}
	if err := s.queueService.UpdateStatus(ctx, item.ID, models.QueueItemStatusFailed, dispatchErr.Error()); err != nil {
		return err
	}

	s.clearRetryAfter(agentID)
	s.logger.Warn().
		Str("agent_id", agentID).
		Str("item_id", item.ID).
		Int("attempt", attempts).
		Int("max_retries", maxRetries).
		Msg("dispatch failed; max retries exceeded")
	return nil
}

func (s *Scheduler) retryBackoff(attempt int) time.Duration {
	base := s.config.RetryBackoff
	if base <= 0 {
		base = DefaultConfig().RetryBackoff
	}
	if attempt <= 1 {
		return base
	}

	backoff := base
	for i := 1; i < attempt; i++ {
		backoff *= 2
	}
	return backoff
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

// publishEvent publishes an event if a publisher is configured.
func (s *Scheduler) publishEvent(ctx context.Context, eventType models.EventType, entityType models.EntityType, entityID string, payload any) {
	if s.publisher == nil {
		return
	}

	event := &models.Event{
		Type:       eventType,
		EntityType: entityType,
		EntityID:   entityID,
	}

	if payload != nil {
		if data, err := json.Marshal(payload); err == nil {
			event.Payload = data
		}
	}

	s.publisher.Publish(ctx, event)
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

	if change.CurrentState == models.AgentStateRateLimited {
		s.handleRateLimit(change)
	}
}

func (s *Scheduler) handleRateLimit(change state.StateChange) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if s.queueService != nil {
		duration := s.rateLimitPauseDuration(change.StateInfo)
		if duration > 0 {
			if err := s.enqueueCooldownPause(ctx, change.AgentID, duration, change.StateInfo); err != nil {
				s.logger.Warn().
					Err(err).
					Str("agent_id", change.AgentID).
					Msg("failed to enqueue cooldown pause")
			}
		}
	}

	if s.accountService == nil || s.agentService == nil {
		return
	}

	agentInfo, err := s.agentService.GetAgent(ctx, change.AgentID)
	if err != nil {
		s.logger.Warn().Err(err).Str("agent_id", change.AgentID).Msg("failed to load agent for rate limit handling")
		return
	}
	if agentInfo.AccountID == "" {
		s.logger.Debug().Str("agent_id", change.AgentID).Msg("agent has no account; skipping cooldown")
		return
	}

	if err := s.accountService.SetCooldownForRateLimit(ctx, agentInfo.AccountID, change.StateInfo.Reason); err != nil {
		s.logger.Warn().
			Err(err).
			Str("agent_id", change.AgentID).
			Str("account_id", agentInfo.AccountID).
			Msg("failed to set account cooldown after rate limit")
		return
	}

	s.logger.Info().
		Str("agent_id", change.AgentID).
		Str("account_id", agentInfo.AccountID).
		Msg("account placed on cooldown due to rate limit")
}

func (s *Scheduler) rateLimitPauseDuration(info models.StateInfo) time.Duration {
	if duration := retryAfterFromEvidence(info.Evidence); duration > 0 {
		return duration
	}
	if s.config.DefaultCooldownDuration > 0 {
		return s.config.DefaultCooldownDuration
	}
	return DefaultConfig().DefaultCooldownDuration
}

func retryAfterFromEvidence(evidence []string) time.Duration {
	for _, entry := range evidence {
		trimmed := strings.TrimSpace(entry)
		if !strings.HasPrefix(trimmed, "retry_after=") {
			continue
		}
		raw := strings.TrimPrefix(trimmed, "retry_after=")
		if raw == "" {
			continue
		}
		if duration, err := time.ParseDuration(raw); err == nil {
			if duration > 0 {
				return duration
			}
			continue
		}
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 0
}

func (s *Scheduler) enqueueCooldownPause(ctx context.Context, agentID string, duration time.Duration, info models.StateInfo) error {
	if duration <= 0 {
		return nil
	}
	seconds := durationSecondsCeil(duration)
	if seconds < 1 {
		return nil
	}

	reason := "auto-pause after rate limit"
	if strings.TrimSpace(info.Reason) != "" {
		reason = fmt.Sprintf("auto-pause after rate limit: %s", info.Reason)
	}

	payload := models.PausePayload{
		DurationSeconds: seconds,
		Reason:          reason,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal pause payload: %w", err)
	}

	item := &models.QueueItem{
		Type:    models.QueueItemTypePause,
		Status:  models.QueueItemStatusPending,
		Payload: payloadBytes,
	}

	return s.queueService.InsertAt(ctx, agentID, 1, item)
}

func durationSecondsCeil(duration time.Duration) int {
	if duration <= 0 {
		return 0
	}
	seconds := int(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}
