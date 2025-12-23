// Package swarmd provides the daemon scaffolding for the Swarm node service.
package swarmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	swarmdv1 "github.com/opencode-ai/swarm/gen/swarmd/v1"
	"github.com/opencode-ai/swarm/internal/config"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
)

// Options configure the daemon runtime.
type Options struct {
	Hostname string
	Port     int
	Version  string
}

// Daemon is the long-running process responsible for node orchestration.
type Daemon struct {
	cfg    *config.Config
	logger zerolog.Logger
	opts   Options

	server     *Server
	grpcServer *grpc.Server
}

// New constructs a daemon with the provided configuration.
func New(cfg *config.Config, logger zerolog.Logger, opts Options) (*Daemon, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if opts.Hostname == "" {
		opts.Hostname = "127.0.0.1"
	}
	if opts.Port == 0 {
		opts.Port = DefaultPort
	}

	// Create the gRPC service implementation
	server := NewServer(logger, WithVersion(opts.Version))

	// Create the gRPC server
	grpcServer := grpc.NewServer()
	swarmdv1.RegisterSwarmdServiceServer(grpcServer, server)

	return &Daemon{
		cfg:        cfg,
		logger:     logger,
		opts:       opts,
		server:     server,
		grpcServer: grpcServer,
	}, nil
}

// Run starts the gRPC server and blocks until the context is canceled.
func (d *Daemon) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}

	bindAddr := d.bindAddr()
	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", bindAddr, err)
	}

	d.logger.Info().
		Str("bind", bindAddr).
		Str("version", d.opts.Version).
		Msg("swarmd gRPC server starting")

	// Start gRPC server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		if err := d.grpcServer.Serve(listener); err != nil {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for shutdown signal or error
	select {
	case <-ctx.Done():
		d.logger.Info().Msg("swarmd shutting down...")
		d.grpcServer.GracefulStop()
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("gRPC server error: %w", err)
		}
	}

	d.logger.Info().Msg("swarmd shutdown complete")
	return nil
}

func (d *Daemon) bindAddr() string {
	return net.JoinHostPort(d.opts.Hostname, strconv.Itoa(d.opts.Port))
}

// Server returns the underlying gRPC service implementation.
// Useful for testing.
func (d *Daemon) Server() *Server {
	return d.server
}
