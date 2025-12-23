package swarmd

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/config"
	"github.com/rs/zerolog"
)

func TestNewDefaultsHostname(t *testing.T) {
	cfg := config.DefaultConfig()
	daemon, err := New(cfg, zerolog.Nop(), Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	want := fmt.Sprintf("127.0.0.1:%d", DefaultPort)
	if got := daemon.bindAddr(); got != want {
		t.Fatalf("bindAddr() = %q, want %q", got, want)
	}
}

func TestRunReturnsOnCanceledContext(t *testing.T) {
	cfg := config.DefaultConfig()
	// Use a high ephemeral port to avoid conflicts with other tests
	daemon, err := New(cfg, zerolog.Nop(), Options{Port: 50099})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Run in goroutine since Run blocks until context is canceled
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx)
	}()

	// Give the server a moment to start, then cancel
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}
