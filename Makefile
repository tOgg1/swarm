# Swarm Makefile
# Control plane for AI coding agents

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

# Go variables
GOCMD := go
GOBUILD := $(GOCMD) build
GOTEST := $(GOCMD) test
GOVET := $(GOCMD) vet
GOFMT := gofmt
GOMOD := $(GOCMD) mod

# Binary names
BINARY_CLI := swarm
BINARY_DAEMON := swarmd
BINARY_RUNNER := swarm-agent-runner

# Directories
BUILD_DIR := ./build
CMD_CLI := ./cmd/swarm
CMD_DAEMON := ./cmd/swarmd
CMD_RUNNER := ./cmd/swarm-agent-runner

# Installation directories
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
GOBIN ?= $(shell go env GOPATH)/bin

# Platforms for cross-compilation
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build build-cli build-daemon build-runner clean test lint fmt vet tidy install install-local install-system uninstall dev help proto proto-lint

# Default target
all: build

## Build targets

# Build both binaries
build: build-cli build-daemon build-runner

# Build the CLI/TUI binary
build-cli:
	@echo "Building $(BINARY_CLI)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_CLI) $(CMD_CLI)

# Build the daemon binary
build-daemon:
	@echo "Building $(BINARY_DAEMON)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_DAEMON) $(CMD_DAEMON)

# Build the agent runner binary
build-runner:
	@echo "Building $(BINARY_RUNNER)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_RUNNER) $(CMD_RUNNER)

# Build for all platforms
build-all:
	@for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_CLI)-$${platform%/*}-$${platform#*/} $(CMD_CLI); \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_DAEMON)-$${platform%/*}-$${platform#*/} $(CMD_DAEMON); \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_RUNNER)-$${platform%/*}-$${platform#*/} $(CMD_RUNNER); \
	done

## Development targets

# Run the CLI in development mode
dev:
	@$(GOCMD) run $(CMD_CLI)

## Installation targets

# Install to GOPATH/bin (default, no sudo required)
install: build
	@echo "Installing to $(GOBIN)..."
	@mkdir -p $(GOBIN)
	@cp $(BUILD_DIR)/$(BINARY_CLI) $(GOBIN)/$(BINARY_CLI)
	@cp $(BUILD_DIR)/$(BINARY_DAEMON) $(GOBIN)/$(BINARY_DAEMON)
	@cp $(BUILD_DIR)/$(BINARY_RUNNER) $(GOBIN)/$(BINARY_RUNNER)
	@echo "Installed $(BINARY_CLI), $(BINARY_DAEMON), and $(BINARY_RUNNER) to $(GOBIN)"
	@echo ""
	@echo "Make sure $(GOBIN) is in your PATH:"
	@echo "  export PATH=\"\$$PATH:$(GOBIN)\""

# Alias for install
install-local: install

# Install system-wide (requires sudo)
install-system: build
	@echo "Installing to $(BINDIR) (may require sudo)..."
	@mkdir -p $(BINDIR)
	@install -m 755 $(BUILD_DIR)/$(BINARY_CLI) $(BINDIR)/$(BINARY_CLI)
	@install -m 755 $(BUILD_DIR)/$(BINARY_DAEMON) $(BINDIR)/$(BINARY_DAEMON)
	@install -m 755 $(BUILD_DIR)/$(BINARY_RUNNER) $(BINDIR)/$(BINARY_RUNNER)
	@echo "Installed $(BINARY_CLI), $(BINARY_DAEMON), and $(BINARY_RUNNER) to $(BINDIR)"

# Uninstall from GOPATH/bin
uninstall:
	@echo "Removing from $(GOBIN)..."
	@rm -f $(GOBIN)/$(BINARY_CLI)
	@rm -f $(GOBIN)/$(BINARY_DAEMON)
	@rm -f $(GOBIN)/$(BINARY_RUNNER)
	@echo "Removed $(BINARY_CLI), $(BINARY_DAEMON), and $(BINARY_RUNNER) from $(GOBIN)"

# Uninstall from system
uninstall-system:
	@echo "Removing from $(BINDIR) (may require sudo)..."
	@rm -f $(BINDIR)/$(BINARY_CLI)
	@rm -f $(BINDIR)/$(BINARY_DAEMON)
	@rm -f $(BINDIR)/$(BINARY_RUNNER)
	@echo "Removed $(BINARY_CLI), $(BINARY_DAEMON), and $(BINARY_RUNNER) from $(BINDIR)"

# Install using go install (builds and installs in one step)
go-install:
	@echo "Installing $(BINARY_CLI) via go install..."
	$(GOCMD) install $(LDFLAGS) $(CMD_CLI)
	@echo "Installing $(BINARY_DAEMON) via go install..."
	$(GOCMD) install $(LDFLAGS) $(CMD_DAEMON)
	@echo "Installing $(BINARY_RUNNER) via go install..."
	$(GOCMD) install $(LDFLAGS) $(CMD_RUNNER)
	@echo "Installed to $(GOBIN)"

## Test targets

# Run all tests
test:
	@echo "Running tests..."
	$(GOTEST) -v -race -cover ./...

# Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	@mkdir -p $(BUILD_DIR)
	$(GOTEST) -v -race -coverprofile=$(BUILD_DIR)/coverage.out ./...
	$(GOCMD) tool cover -html=$(BUILD_DIR)/coverage.out -o $(BUILD_DIR)/coverage.html
	@echo "Coverage report: $(BUILD_DIR)/coverage.html"

# Run short tests only
test-short:
	$(GOTEST) -v -short ./...

## Code quality targets

# Run linter (requires golangci-lint)
lint:
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest" && exit 1)
	golangci-lint run ./...

# Format code
fmt:
	@echo "Formatting code..."
	$(GOFMT) -s -w .

# Check formatting
fmt-check:
	@echo "Checking formatting..."
	@test -z "$$($(GOFMT) -l .)" || (echo "Code is not formatted. Run 'make fmt'" && exit 1)

# Run go vet
vet:
	@echo "Running vet..."
	$(GOVET) ./...

# Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	$(GOMOD) tidy

# Run all checks (for CI)
check: fmt-check vet lint test

## Protocol Buffers

# Generate protobuf code
proto:
	@echo "Generating protobuf code..."
	@which buf > /dev/null || (echo "buf not installed. Run: go install github.com/bufbuild/buf/cmd/buf@latest" && exit 1)
	buf generate
	@echo "Generated code in gen/"

# Lint protobuf files
proto-lint:
	@echo "Linting protobuf files..."
	@which buf > /dev/null || (echo "buf not installed. Run: go install github.com/bufbuild/buf/cmd/buf@latest" && exit 1)
	buf lint

# Update buf dependencies
proto-deps:
	@echo "Updating buf dependencies..."
	buf dep update

## Cleanup

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	$(GOCMD) clean -cache -testcache

## Database

# Run database migrations
migrate-up:
	@echo "Running migrations..."
	@echo "TODO: Implement migrations"

migrate-down:
	@echo "Rolling back migrations..."
	@echo "TODO: Implement migrations"

## Help

# Show help
help:
	@echo "Swarm - Control plane for AI coding agents"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Build Targets:"
	@echo "  build          Build both CLI and daemon binaries to ./build/"
	@echo "  build-cli      Build only the CLI/TUI binary"
	@echo "  build-daemon   Build only the daemon binary"
	@echo "  build-all      Build for all platforms (cross-compile)"
	@echo "  clean          Remove build artifacts"
	@echo ""
	@echo "Install Targets:"
	@echo "  install        Build and install to GOPATH/bin (recommended)"
	@echo "  install-local  Alias for install"
	@echo "  install-system Build and install to /usr/local/bin (requires sudo)"
	@echo "  go-install     Use 'go install' directly"
	@echo "  uninstall      Remove from GOPATH/bin"
	@echo "  uninstall-system Remove from /usr/local/bin (requires sudo)"
	@echo ""
	@echo "Development Targets:"
	@echo "  dev            Run the CLI without building"
	@echo "  test           Run all tests with race detector"
	@echo "  test-coverage  Run tests with HTML coverage report"
	@echo "  test-short     Run short tests only"
	@echo "  lint           Run golangci-lint"
	@echo "  fmt            Format code with gofmt"
	@echo "  vet            Run go vet"
	@echo "  tidy           Tidy go.mod dependencies"
	@echo "  check          Run all checks (fmt, vet, lint, test)"
	@echo ""
	@echo "Protobuf Targets:"
	@echo "  proto          Generate protobuf code"
	@echo "  proto-lint     Lint protobuf files"
	@echo "  proto-deps     Update buf dependencies"
	@echo ""
	@echo "Quick Start:"
	@echo "  make build                    # Build to ./build/"
	@echo "  make install                  # Build + install to GOPATH/bin"
	@echo "  sudo make install-system      # Build + install to /usr/local/bin"
	@echo ""
	@echo "Variables (override with VAR=value):"
	@echo "  VERSION        $(VERSION)"
	@echo "  COMMIT         $(COMMIT)"
	@echo "  PREFIX         $(PREFIX)"
	@echo "  BINDIR         $(BINDIR)"
	@echo "  GOBIN          $(GOBIN)"
