// Package swarmd provides the daemon scaffolding for the Swarm node service.
package swarmd

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RateLimitConfig defines rate limits for a specific method or globally.
type RateLimitConfig struct {
	// RequestsPerSecond is the sustainable rate (tokens added per second).
	RequestsPerSecond float64

	// BurstSize is the maximum number of requests allowed in a burst.
	BurstSize int
}

// DefaultRateLimits provides sensible defaults for different RPC categories.
var DefaultRateLimits = map[string]RateLimitConfig{
	// Heavy operations - spawn/kill agents
	"/swarmd.v1.SwarmdService/SpawnAgent": {RequestsPerSecond: 5, BurstSize: 10},
	"/swarmd.v1.SwarmdService/KillAgent":  {RequestsPerSecond: 10, BurstSize: 20},

	// Input operations - moderate limits
	"/swarmd.v1.SwarmdService/SendInput": {RequestsPerSecond: 50, BurstSize: 100},

	// Read operations - higher limits
	"/swarmd.v1.SwarmdService/ListAgents":  {RequestsPerSecond: 100, BurstSize: 200},
	"/swarmd.v1.SwarmdService/GetAgent":    {RequestsPerSecond: 100, BurstSize: 200},
	"/swarmd.v1.SwarmdService/CapturePane": {RequestsPerSecond: 50, BurstSize: 100},

	// Transcript operations
	"/swarmd.v1.SwarmdService/GetTranscript": {RequestsPerSecond: 50, BurstSize: 100},

	// Health/status - very high limits (essentially unlimited)
	"/swarmd.v1.SwarmdService/GetStatus": {RequestsPerSecond: 1000, BurstSize: 1000},
	"/swarmd.v1.SwarmdService/Ping":      {RequestsPerSecond: 1000, BurstSize: 1000},

	// Streaming operations - limit connection rate, not message rate
	"/swarmd.v1.SwarmdService/StreamPaneUpdates": {RequestsPerSecond: 10, BurstSize: 20},
	"/swarmd.v1.SwarmdService/StreamEvents":      {RequestsPerSecond: 10, BurstSize: 20},
	"/swarmd.v1.SwarmdService/StreamTranscript":  {RequestsPerSecond: 10, BurstSize: 20},
}

// tokenBucket implements the token bucket algorithm for rate limiting.
type tokenBucket struct {
	mu           sync.Mutex
	tokens       float64
	lastUpdate   time.Time
	ratePerSec   float64
	maxTokens    float64
	requestCount int64 // total requests (for stats)
	deniedCount  int64 // denied requests (for stats)
}

// newTokenBucket creates a new token bucket with the given configuration.
func newTokenBucket(cfg RateLimitConfig) *tokenBucket {
	return &tokenBucket{
		tokens:     float64(cfg.BurstSize),
		lastUpdate: time.Now(),
		ratePerSec: cfg.RequestsPerSecond,
		maxTokens:  float64(cfg.BurstSize),
	}
}

// allow checks if a request is allowed and consumes a token if so.
func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.requestCount++

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(tb.lastUpdate).Seconds()
	tb.tokens += elapsed * tb.ratePerSec
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastUpdate = now

	// Check if we have a token available
	if tb.tokens >= 1.0 {
		tb.tokens--
		return true
	}

	tb.deniedCount++
	return false
}

// stats returns the current statistics for this bucket.
func (tb *tokenBucket) stats() (available float64, requestCount, deniedCount int64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	// Refill tokens for accurate available count
	now := time.Now()
	elapsed := now.Sub(tb.lastUpdate).Seconds()
	available = tb.tokens + elapsed*tb.ratePerSec
	if available > tb.maxTokens {
		available = tb.maxTokens
	}

	return available, tb.requestCount, tb.deniedCount
}

// RateLimiter manages rate limits for multiple methods.
type RateLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*tokenBucket
	configs map[string]RateLimitConfig

	// Global rate limit (applied to all methods)
	globalBucket *tokenBucket
	globalConfig *RateLimitConfig

	// Enabled flag - can disable rate limiting entirely
	enabled bool
}

// RateLimiterOption configures the RateLimiter.
type RateLimiterOption func(*RateLimiter)

// WithMethodLimits sets custom limits for specific methods.
func WithMethodLimits(limits map[string]RateLimitConfig) RateLimiterOption {
	return func(rl *RateLimiter) {
		for method, cfg := range limits {
			rl.configs[method] = cfg
		}
	}
}

// WithGlobalLimit sets a global rate limit applied to all methods.
func WithGlobalLimit(cfg RateLimitConfig) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.globalConfig = &cfg
		rl.globalBucket = newTokenBucket(cfg)
	}
}

// WithEnabled enables or disables rate limiting.
func WithEnabled(enabled bool) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.enabled = enabled
	}
}

// NewRateLimiter creates a new rate limiter with the given options.
func NewRateLimiter(opts ...RateLimiterOption) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*tokenBucket),
		configs: make(map[string]RateLimitConfig),
		enabled: true,
	}

	// Apply default limits
	for method, cfg := range DefaultRateLimits {
		rl.configs[method] = cfg
	}

	// Apply custom options
	for _, opt := range opts {
		opt(rl)
	}

	return rl
}

// Allow checks if a request to the given method is allowed.
func (rl *RateLimiter) Allow(method string) bool {
	if !rl.enabled {
		return true
	}

	// Check global limit first
	if rl.globalBucket != nil && !rl.globalBucket.allow() {
		return false
	}

	// Get or create bucket for this method
	bucket := rl.getBucket(method)
	if bucket == nil {
		// No limit configured for this method
		return true
	}

	return bucket.allow()
}

// getBucket returns the token bucket for a method, creating it if needed.
func (rl *RateLimiter) getBucket(method string) *tokenBucket {
	rl.mu.RLock()
	bucket, exists := rl.buckets[method]
	rl.mu.RUnlock()

	if exists {
		return bucket
	}

	// Check if we have a config for this method
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check after acquiring write lock
	if bucket, exists = rl.buckets[method]; exists {
		return bucket
	}

	cfg, hasCfg := rl.configs[method]
	if !hasCfg {
		return nil
	}

	bucket = newTokenBucket(cfg)
	rl.buckets[method] = bucket
	return bucket
}

// MethodStats returns rate limit statistics for a specific method.
type MethodStats struct {
	Method           string
	Available        float64
	RequestsPerSec   float64
	BurstSize        int
	TotalRequests    int64
	DeniedRequests   int64
	DeniedPercentage float64
}

// Stats returns statistics for all configured methods.
func (rl *RateLimiter) Stats() []MethodStats {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var stats []MethodStats

	for method, cfg := range rl.configs {
		ms := MethodStats{
			Method:         method,
			RequestsPerSec: cfg.RequestsPerSecond,
			BurstSize:      cfg.BurstSize,
		}

		if bucket, exists := rl.buckets[method]; exists {
			ms.Available, ms.TotalRequests, ms.DeniedRequests = bucket.stats()
			if ms.TotalRequests > 0 {
				ms.DeniedPercentage = float64(ms.DeniedRequests) / float64(ms.TotalRequests) * 100
			}
		} else {
			ms.Available = float64(cfg.BurstSize)
		}

		stats = append(stats, ms)
	}

	return stats
}

// GlobalStats returns statistics for the global rate limit.
func (rl *RateLimiter) GlobalStats() *MethodStats {
	if rl.globalBucket == nil || rl.globalConfig == nil {
		return nil
	}

	available, total, denied := rl.globalBucket.stats()
	deniedPct := 0.0
	if total > 0 {
		deniedPct = float64(denied) / float64(total) * 100
	}

	return &MethodStats{
		Method:           "global",
		Available:        available,
		RequestsPerSec:   rl.globalConfig.RequestsPerSecond,
		BurstSize:        rl.globalConfig.BurstSize,
		TotalRequests:    total,
		DeniedRequests:   denied,
		DeniedPercentage: deniedPct,
	}
}

// SetEnabled enables or disables rate limiting at runtime.
func (rl *RateLimiter) SetEnabled(enabled bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.enabled = enabled
}

// IsEnabled returns whether rate limiting is currently enabled.
func (rl *RateLimiter) IsEnabled() bool {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return rl.enabled
}

// UnaryServerInterceptor returns a gRPC unary interceptor that applies rate limiting.
func (rl *RateLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if !rl.Allow(info.FullMethod) {
			return nil, status.Errorf(codes.ResourceExhausted,
				"rate limit exceeded for method %s", info.FullMethod)
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor returns a gRPC stream interceptor that applies rate limiting.
// Note: This only limits the rate of stream creation, not individual messages.
func (rl *RateLimiter) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if !rl.Allow(info.FullMethod) {
			return status.Errorf(codes.ResourceExhausted,
				"rate limit exceeded for stream %s", info.FullMethod)
		}
		return handler(srv, ss)
	}
}
