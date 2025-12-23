package swarmd

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTokenBucketAllow(t *testing.T) {
	cfg := RateLimitConfig{
		RequestsPerSecond: 10,
		BurstSize:         5,
	}
	bucket := newTokenBucket(cfg)

	// Should allow first 5 requests (burst)
	for i := 0; i < 5; i++ {
		if !bucket.allow() {
			t.Errorf("Request %d should be allowed (within burst)", i)
		}
	}

	// 6th request should be denied (burst exhausted)
	if bucket.allow() {
		t.Error("Request 6 should be denied (burst exhausted)")
	}
}

func TestTokenBucketRefill(t *testing.T) {
	cfg := RateLimitConfig{
		RequestsPerSecond: 100, // Fast refill for testing
		BurstSize:         1,
	}
	bucket := newTokenBucket(cfg)

	// Use the token
	if !bucket.allow() {
		t.Error("First request should be allowed")
	}

	// Should be denied immediately
	if bucket.allow() {
		t.Error("Second request should be denied")
	}

	// Wait for refill (at 100/sec, should get 1 token in 10ms)
	time.Sleep(15 * time.Millisecond)

	// Should be allowed again
	if !bucket.allow() {
		t.Error("Request after refill should be allowed")
	}
}

func TestTokenBucketStats(t *testing.T) {
	cfg := RateLimitConfig{
		RequestsPerSecond: 10,
		BurstSize:         5,
	}
	bucket := newTokenBucket(cfg)

	// Make some requests
	bucket.allow() // allowed
	bucket.allow() // allowed
	bucket.allow() // allowed
	bucket.allow() // allowed
	bucket.allow() // allowed
	bucket.allow() // denied

	available, total, denied := bucket.stats()

	if total != 6 {
		t.Errorf("TotalRequests = %d, want 6", total)
	}
	if denied != 1 {
		t.Errorf("DeniedRequests = %d, want 1", denied)
	}
	if available >= 1 {
		t.Errorf("Available = %.2f, expected < 1", available)
	}
}

func TestRateLimiterDefaultLimits(t *testing.T) {
	rl := NewRateLimiter()

	// Check that default limits are applied
	if !rl.IsEnabled() {
		t.Error("Rate limiter should be enabled by default")
	}

	// Check that we have default configs
	for method := range DefaultRateLimits {
		if !rl.Allow(method) {
			t.Errorf("First request to %s should be allowed", method)
		}
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	rl := NewRateLimiter(WithEnabled(false))

	if rl.IsEnabled() {
		t.Error("Rate limiter should be disabled")
	}

	// All requests should be allowed when disabled
	for i := 0; i < 1000; i++ {
		if !rl.Allow("/swarmd.v1.SwarmdService/SpawnAgent") {
			t.Errorf("Request %d should be allowed when rate limiting is disabled", i)
		}
	}
}

func TestRateLimiterCustomLimits(t *testing.T) {
	customLimits := map[string]RateLimitConfig{
		"/custom/method": {RequestsPerSecond: 1, BurstSize: 2},
	}

	rl := NewRateLimiter(WithMethodLimits(customLimits))

	// Should allow 2 requests (burst)
	if !rl.Allow("/custom/method") {
		t.Error("Request 1 should be allowed")
	}
	if !rl.Allow("/custom/method") {
		t.Error("Request 2 should be allowed")
	}

	// Should deny 3rd request
	if rl.Allow("/custom/method") {
		t.Error("Request 3 should be denied")
	}
}

func TestRateLimiterGlobalLimit(t *testing.T) {
	globalCfg := RateLimitConfig{
		RequestsPerSecond: 10,
		BurstSize:         3,
	}

	rl := NewRateLimiter(WithGlobalLimit(globalCfg))

	// Should allow 3 requests across any methods
	if !rl.Allow("/method1") {
		t.Error("Request 1 should be allowed")
	}
	if !rl.Allow("/method2") {
		t.Error("Request 2 should be allowed")
	}
	if !rl.Allow("/method3") {
		t.Error("Request 3 should be allowed")
	}

	// 4th request should be denied (global limit)
	if rl.Allow("/method4") {
		t.Error("Request 4 should be denied (global limit)")
	}
}

func TestRateLimiterUnknownMethod(t *testing.T) {
	rl := NewRateLimiter()

	// Unknown methods should not be rate limited
	for i := 0; i < 100; i++ {
		if !rl.Allow("/unknown/method") {
			t.Errorf("Request %d to unknown method should be allowed", i)
		}
	}
}

func TestRateLimiterStats(t *testing.T) {
	rl := NewRateLimiter()

	// Make some requests
	rl.Allow("/swarmd.v1.SwarmdService/SpawnAgent")
	rl.Allow("/swarmd.v1.SwarmdService/SpawnAgent")
	rl.Allow("/swarmd.v1.SwarmdService/KillAgent")

	stats := rl.Stats()
	if len(stats) == 0 {
		t.Error("Expected stats for configured methods")
	}

	// Find SpawnAgent stats
	var spawnStats *MethodStats
	for i := range stats {
		if stats[i].Method == "/swarmd.v1.SwarmdService/SpawnAgent" {
			spawnStats = &stats[i]
			break
		}
	}

	if spawnStats == nil {
		t.Fatal("Expected to find SpawnAgent stats")
	}
	if spawnStats.TotalRequests != 2 {
		t.Errorf("SpawnAgent TotalRequests = %d, want 2", spawnStats.TotalRequests)
	}
}

func TestRateLimiterGlobalStats(t *testing.T) {
	globalCfg := RateLimitConfig{
		RequestsPerSecond: 10,
		BurstSize:         5,
	}
	rl := NewRateLimiter(WithGlobalLimit(globalCfg))

	rl.Allow("/method1")
	rl.Allow("/method2")

	stats := rl.GlobalStats()
	if stats == nil {
		t.Fatal("Expected global stats")
	}
	if stats.TotalRequests != 2 {
		t.Errorf("Global TotalRequests = %d, want 2", stats.TotalRequests)
	}
	if stats.BurstSize != 5 {
		t.Errorf("Global BurstSize = %d, want 5", stats.BurstSize)
	}
}

func TestRateLimiterSetEnabled(t *testing.T) {
	rl := NewRateLimiter()

	if !rl.IsEnabled() {
		t.Error("Should be enabled by default")
	}

	rl.SetEnabled(false)
	if rl.IsEnabled() {
		t.Error("Should be disabled after SetEnabled(false)")
	}

	rl.SetEnabled(true)
	if !rl.IsEnabled() {
		t.Error("Should be enabled after SetEnabled(true)")
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	cfg := RateLimitConfig{
		RequestsPerSecond: 1000,
		BurstSize:         100,
	}
	rl := NewRateLimiter(WithMethodLimits(map[string]RateLimitConfig{
		"/concurrent": cfg,
	}))

	var wg sync.WaitGroup
	allowed := make(chan bool, 200)

	// Launch 200 concurrent requests
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- rl.Allow("/concurrent")
		}()
	}

	wg.Wait()
	close(allowed)

	// Count allowed and denied
	allowedCount := 0
	deniedCount := 0
	for result := range allowed {
		if result {
			allowedCount++
		} else {
			deniedCount++
		}
	}

	// Should have allowed around 100 (burst size) and denied around 100
	if allowedCount < 90 || allowedCount > 110 {
		t.Errorf("Expected ~100 allowed, got %d", allowedCount)
	}
	if deniedCount < 90 || deniedCount > 110 {
		t.Errorf("Expected ~100 denied, got %d", deniedCount)
	}
}

// Mock gRPC handler for testing interceptors
func mockUnaryHandler(ctx context.Context, req interface{}) (interface{}, error) {
	return "ok", nil
}

func TestUnaryServerInterceptor(t *testing.T) {
	rl := NewRateLimiter(WithMethodLimits(map[string]RateLimitConfig{
		"/test/method": {RequestsPerSecond: 1, BurstSize: 2},
	}))

	interceptor := rl.UnaryServerInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/test/method"}

	// First 2 requests should succeed
	for i := 0; i < 2; i++ {
		_, err := interceptor(context.Background(), nil, info, mockUnaryHandler)
		if err != nil {
			t.Errorf("Request %d should succeed: %v", i+1, err)
		}
	}

	// 3rd request should fail with ResourceExhausted
	_, err := interceptor(context.Background(), nil, info, mockUnaryHandler)
	if err == nil {
		t.Error("Request 3 should fail")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("Expected ResourceExhausted, got: %v", st.Code())
	}
}

// Mock stream server for testing
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

func mockStreamHandler(srv interface{}, stream grpc.ServerStream) error {
	return nil
}

func TestStreamServerInterceptor(t *testing.T) {
	rl := NewRateLimiter(WithMethodLimits(map[string]RateLimitConfig{
		"/test/stream": {RequestsPerSecond: 1, BurstSize: 1},
	}))

	interceptor := rl.StreamServerInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/test/stream"}
	stream := &mockServerStream{ctx: context.Background()}

	// First request should succeed
	err := interceptor(nil, stream, info, mockStreamHandler)
	if err != nil {
		t.Errorf("Request 1 should succeed: %v", err)
	}

	// 2nd request should fail
	err = interceptor(nil, stream, info, mockStreamHandler)
	if err == nil {
		t.Error("Request 2 should fail")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("Expected ResourceExhausted, got: %v", st.Code())
	}
}

func TestDefaultRateLimitsExist(t *testing.T) {
	expectedMethods := []string{
		"/swarmd.v1.SwarmdService/SpawnAgent",
		"/swarmd.v1.SwarmdService/KillAgent",
		"/swarmd.v1.SwarmdService/SendInput",
		"/swarmd.v1.SwarmdService/ListAgents",
		"/swarmd.v1.SwarmdService/GetAgent",
		"/swarmd.v1.SwarmdService/CapturePane",
		"/swarmd.v1.SwarmdService/GetTranscript",
		"/swarmd.v1.SwarmdService/GetStatus",
		"/swarmd.v1.SwarmdService/Ping",
		"/swarmd.v1.SwarmdService/StreamPaneUpdates",
		"/swarmd.v1.SwarmdService/StreamEvents",
		"/swarmd.v1.SwarmdService/StreamTranscript",
	}

	for _, method := range expectedMethods {
		if _, exists := DefaultRateLimits[method]; !exists {
			t.Errorf("Missing default rate limit for %s", method)
		}
	}
}

func TestDefaultRateLimitsAreSane(t *testing.T) {
	for method, cfg := range DefaultRateLimits {
		if cfg.RequestsPerSecond <= 0 {
			t.Errorf("%s: RequestsPerSecond should be positive, got %f", method, cfg.RequestsPerSecond)
		}
		if cfg.BurstSize <= 0 {
			t.Errorf("%s: BurstSize should be positive, got %d", method, cfg.BurstSize)
		}
		if cfg.BurstSize < int(cfg.RequestsPerSecond) {
			// Burst should typically be >= rate to allow reasonable bursting
			t.Logf("%s: Warning - BurstSize (%d) < RequestsPerSecond (%f)", method, cfg.BurstSize, cfg.RequestsPerSecond)
		}
	}
}
