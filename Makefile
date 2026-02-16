# Forge Makefile
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
GO_LAYOUT_MODE ?= legacy
GO_LAYOUT_GUARD := ./scripts/go-layout-guard.sh
RUST_FIRST ?= 1

# Binary names
BINARY_CLI := forge
BINARY_DAEMON := forged
BINARY_RUNNER := forge-agent-runner
BINARY_FMAIL := fmail
BINARY_TUI := forge-tui
RUST_BINARY_CLI := rforge
RUST_BINARY_DAEMON := rforged
RUST_BINARY_FMAIL := rfmail
RUST_META_ENV := env FORGE_VERSION="$(VERSION)" FORGE_COMMIT="$(COMMIT)" FORGE_BUILD_DATE="$(DATE)"

# Directories
BUILD_DIR := ./build
GO_SRC_DIR := ./old/go
CMD_CLI := ./cmd/forge
CMD_DAEMON := ./cmd/forged
CMD_RUNNER := ./cmd/forge-agent-runner
CMD_FMAIL := ./cmd/fmail
RUST_DIR := .

# Installation directories
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
LOCAL_BIN_DIR ?= $(HOME)/.cargo/bin
INSTALL_DIR ?= $(LOCAL_BIN_DIR)
# Backward-compatible command-line override: `GOBIN=/custom/bin make install`
ifeq ($(origin GOBIN), command line)
INSTALL_DIR := $(GOBIN)
endif
RUST_INSTALL_DIR ?= $(LOCAL_BIN_DIR)

# Platforms for cross-compilation
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build go-layout-guard build-cli build-daemon build-runner build-fmail build-tui build-rust build-rust-cli build-rust-daemon build-rust-fmail build-rust-tui clean test lint fmt vet tidy install install-local install-system install-rust install-rust-system uninstall uninstall-rust uninstall-rust-system dev help proto proto-lint rust-daemon-runtime-parity forged-launchd-install forged-launchd-uninstall forged-launchd-status
.PHONY: perf-smoke perf-bench

# Default target
all: build

## Build targets

# Build both binaries
build: go-layout-guard build-cli build-daemon build-runner build-fmail build-tui

# Guard against partial Go source tree moves during rust rewrite staging.
go-layout-guard:
	@$(GO_LAYOUT_GUARD) --repo-root . --mode $(GO_LAYOUT_MODE)

# Build the CLI/TUI binary
build-cli:
	@echo "Building $(BINARY_CLI)..."
	@mkdir -p $(BUILD_DIR)
ifeq ($(RUST_FIRST),1)
	@echo "RUST_FIRST=1: using Rust $(RUST_BINARY_CLI) as $(BINARY_CLI)"
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p forge-cli --bin $(RUST_BINARY_CLI)
	@cp $(RUST_DIR)/target/release/$(RUST_BINARY_CLI) $(BUILD_DIR)/$(BINARY_CLI)
else
	@cd $(GO_SRC_DIR) && $(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_CLI) $(CMD_CLI)
endif

# Build the daemon binary
build-daemon:
	@echo "Building $(BINARY_DAEMON)..."
	@mkdir -p $(BUILD_DIR)
ifeq ($(RUST_FIRST),1)
	@echo "RUST_FIRST=1: using Rust $(RUST_BINARY_DAEMON) as $(BINARY_DAEMON)"
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p forge-daemon --bin $(RUST_BINARY_DAEMON)
	@cp $(RUST_DIR)/target/release/$(RUST_BINARY_DAEMON) $(BUILD_DIR)/$(BINARY_DAEMON)
else
	@cd $(GO_SRC_DIR) && $(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_DAEMON) $(CMD_DAEMON)
endif

# Build the agent runner binary
build-runner:
	@echo "Building $(BINARY_RUNNER)..."
	@mkdir -p $(BUILD_DIR)
ifeq ($(RUST_FIRST),1)
	@echo "RUST_FIRST=1: using Rust $(BINARY_RUNNER)"
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p forge-runner --bin $(BINARY_RUNNER)
	@cp $(RUST_DIR)/target/release/$(BINARY_RUNNER) $(BUILD_DIR)/$(BINARY_RUNNER)
else
	@cd $(GO_SRC_DIR) && $(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_RUNNER) $(CMD_RUNNER)
endif

# Build the fmail binary
build-fmail:
	@echo "Building $(BINARY_FMAIL)..."
	@mkdir -p $(BUILD_DIR)
ifeq ($(RUST_FIRST),1)
	@echo "RUST_FIRST=1: using Rust $(RUST_BINARY_FMAIL) as $(BINARY_FMAIL)"
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p fmail-cli --bin $(RUST_BINARY_FMAIL)
	@cp $(RUST_DIR)/target/release/$(RUST_BINARY_FMAIL) $(BUILD_DIR)/$(BINARY_FMAIL)
else
	@cd $(GO_SRC_DIR) && $(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_FMAIL) $(CMD_FMAIL)
endif

# Build the dedicated forge TUI binary
build-tui:
	@echo "Building $(BINARY_TUI)..."
	@mkdir -p $(BUILD_DIR)
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p forge-tui --bin $(BINARY_TUI)
	@cp $(RUST_DIR)/target/release/$(BINARY_TUI) $(BUILD_DIR)/$(BINARY_TUI)

# Build for all platforms
build-all:
ifeq ($(RUST_FIRST),1)
	@echo "build-all does not yet support RUST_FIRST=1; use make build RUST_FIRST=1" && exit 1
else
	@for platform in $(PLATFORMS); do \
		(cd $(GO_SRC_DIR) && GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_CLI)-$${platform%/*}-$${platform#*/} $(CMD_CLI)); \
		(cd $(GO_SRC_DIR) && GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_DAEMON)-$${platform%/*}-$${platform#*/} $(CMD_DAEMON)); \
		(cd $(GO_SRC_DIR) && GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_RUNNER)-$${platform%/*}-$${platform#*/} $(CMD_RUNNER)); \
		(cd $(GO_SRC_DIR) && GOOS=$${platform%/*} GOARCH=$${platform#*/} \
		$(GOBUILD) $(LDFLAGS) -o $(abspath $(BUILD_DIR))/$(BINARY_FMAIL)-$${platform%/*}-$${platform#*/} $(CMD_FMAIL)); \
	done
endif

## Rust build targets (side-by-side; non-conflicting binaries)

build-rust: build-rust-cli build-rust-daemon build-rust-fmail build-rust-tui

build-rust-cli:
	@echo "Building $(RUST_BINARY_CLI) (Rust)..."
	@mkdir -p $(BUILD_DIR)
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p forge-cli --bin $(RUST_BINARY_CLI)
	@cp $(RUST_DIR)/target/release/$(RUST_BINARY_CLI) $(BUILD_DIR)/$(RUST_BINARY_CLI)

build-rust-daemon:
	@echo "Building $(RUST_BINARY_DAEMON) (Rust)..."
	@mkdir -p $(BUILD_DIR)
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p forge-daemon --bin $(RUST_BINARY_DAEMON)
	@cp $(RUST_DIR)/target/release/$(RUST_BINARY_DAEMON) $(BUILD_DIR)/$(RUST_BINARY_DAEMON)

build-rust-fmail:
	@echo "Building $(RUST_BINARY_FMAIL) (Rust)..."
	@mkdir -p $(BUILD_DIR)
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p fmail-cli --bin $(RUST_BINARY_FMAIL)
	@cp $(RUST_DIR)/target/release/$(RUST_BINARY_FMAIL) $(BUILD_DIR)/$(RUST_BINARY_FMAIL)

build-rust-tui:
	@echo "Building $(BINARY_TUI) (Rust)..."
	@mkdir -p $(BUILD_DIR)
	@cd $(RUST_DIR) && $(RUST_META_ENV) cargo build --release -p forge-tui --bin $(BINARY_TUI)
	@cp $(RUST_DIR)/target/release/$(BINARY_TUI) $(BUILD_DIR)/$(BINARY_TUI)

## Development targets

# Run the CLI in development mode
dev:
	@$(RUST_META_ENV) cargo run -p forge-cli --bin rforge -- --help >/dev/null
	@echo "Rust dev surface ready: use 'cargo run -p forge-cli --bin rforge -- <args>'"

## Installation targets

# Install to local bin dir (default, no sudo required)
install: build
	@echo "Installing to $(INSTALL_DIR)..."
	@mkdir -p $(INSTALL_DIR)
	@install -m 755 $(BUILD_DIR)/$(BINARY_CLI) $(INSTALL_DIR)/.$(BINARY_CLI).tmp
	@mv $(INSTALL_DIR)/.$(BINARY_CLI).tmp $(INSTALL_DIR)/$(BINARY_CLI)
	@install -m 755 $(BUILD_DIR)/$(BINARY_DAEMON) $(INSTALL_DIR)/.$(BINARY_DAEMON).tmp
	@mv $(INSTALL_DIR)/.$(BINARY_DAEMON).tmp $(INSTALL_DIR)/$(BINARY_DAEMON)
	@install -m 755 $(BUILD_DIR)/$(BINARY_RUNNER) $(INSTALL_DIR)/.$(BINARY_RUNNER).tmp
	@mv $(INSTALL_DIR)/.$(BINARY_RUNNER).tmp $(INSTALL_DIR)/$(BINARY_RUNNER)
	@install -m 755 $(BUILD_DIR)/$(BINARY_FMAIL) $(INSTALL_DIR)/.$(BINARY_FMAIL).tmp
	@mv $(INSTALL_DIR)/.$(BINARY_FMAIL).tmp $(INSTALL_DIR)/$(BINARY_FMAIL)
	@install -m 755 $(BUILD_DIR)/$(BINARY_TUI) $(INSTALL_DIR)/.$(BINARY_TUI).tmp
	@mv $(INSTALL_DIR)/.$(BINARY_TUI).tmp $(INSTALL_DIR)/$(BINARY_TUI)
	@echo "Installed $(BINARY_CLI), $(BINARY_DAEMON), $(BINARY_RUNNER), $(BINARY_FMAIL), and $(BINARY_TUI) to $(INSTALL_DIR)"
	@echo ""
	@echo "Make sure $(INSTALL_DIR) is in your PATH:"
	@echo "  export PATH=\"\$$PATH:$(INSTALL_DIR)\""

# Alias for install
install-local: install

# Install system-wide (requires sudo)
install-system: build
	@echo "Installing to $(BINDIR) (may require sudo)..."
	@mkdir -p $(BINDIR)
	@install -m 755 $(BUILD_DIR)/$(BINARY_CLI) $(BINDIR)/$(BINARY_CLI)
	@install -m 755 $(BUILD_DIR)/$(BINARY_DAEMON) $(BINDIR)/$(BINARY_DAEMON)
	@install -m 755 $(BUILD_DIR)/$(BINARY_RUNNER) $(BINDIR)/$(BINARY_RUNNER)
	@install -m 755 $(BUILD_DIR)/$(BINARY_FMAIL) $(BINDIR)/$(BINARY_FMAIL)
	@install -m 755 $(BUILD_DIR)/$(BINARY_TUI) $(BINDIR)/$(BINARY_TUI)
	@echo "Installed $(BINARY_CLI), $(BINARY_DAEMON), $(BINARY_RUNNER), $(BINARY_FMAIL), and $(BINARY_TUI) to $(BINDIR)"

install-rust: build-rust
	@echo "Installing Rust side-by-side binaries to $(RUST_INSTALL_DIR)..."
	@mkdir -p $(RUST_INSTALL_DIR)
	@install -m 755 $(BUILD_DIR)/$(RUST_BINARY_CLI) $(RUST_INSTALL_DIR)/.$(RUST_BINARY_CLI).tmp
	@mv $(RUST_INSTALL_DIR)/.$(RUST_BINARY_CLI).tmp $(RUST_INSTALL_DIR)/$(RUST_BINARY_CLI)
	@install -m 755 $(BUILD_DIR)/$(RUST_BINARY_DAEMON) $(RUST_INSTALL_DIR)/.$(RUST_BINARY_DAEMON).tmp
	@mv $(RUST_INSTALL_DIR)/.$(RUST_BINARY_DAEMON).tmp $(RUST_INSTALL_DIR)/$(RUST_BINARY_DAEMON)
	@install -m 755 $(BUILD_DIR)/$(RUST_BINARY_FMAIL) $(RUST_INSTALL_DIR)/.$(RUST_BINARY_FMAIL).tmp
	@mv $(RUST_INSTALL_DIR)/.$(RUST_BINARY_FMAIL).tmp $(RUST_INSTALL_DIR)/$(RUST_BINARY_FMAIL)
	@install -m 755 $(BUILD_DIR)/$(BINARY_TUI) $(RUST_INSTALL_DIR)/.$(BINARY_TUI).tmp
	@mv $(RUST_INSTALL_DIR)/.$(BINARY_TUI).tmp $(RUST_INSTALL_DIR)/$(BINARY_TUI)
	@echo "Installed $(RUST_BINARY_CLI), $(RUST_BINARY_DAEMON), $(RUST_BINARY_FMAIL), and $(BINARY_TUI) to $(RUST_INSTALL_DIR)"

install-rust-system: build-rust
	@echo "Installing Rust side-by-side binaries to $(BINDIR) (may require sudo)..."
	@mkdir -p $(BINDIR)
	@install -m 755 $(BUILD_DIR)/$(RUST_BINARY_CLI) $(BINDIR)/$(RUST_BINARY_CLI)
	@install -m 755 $(BUILD_DIR)/$(RUST_BINARY_DAEMON) $(BINDIR)/$(RUST_BINARY_DAEMON)
	@install -m 755 $(BUILD_DIR)/$(RUST_BINARY_FMAIL) $(BINDIR)/$(RUST_BINARY_FMAIL)
	@install -m 755 $(BUILD_DIR)/$(BINARY_TUI) $(BINDIR)/$(BINARY_TUI)
	@echo "Installed $(RUST_BINARY_CLI), $(RUST_BINARY_DAEMON), $(RUST_BINARY_FMAIL), and $(BINARY_TUI) to $(BINDIR)"

# Uninstall from local bin dir
uninstall:
	@echo "Removing from $(INSTALL_DIR)..."
	@rm -f $(INSTALL_DIR)/$(BINARY_CLI)
	@rm -f $(INSTALL_DIR)/$(BINARY_DAEMON)
	@rm -f $(INSTALL_DIR)/$(BINARY_RUNNER)
	@rm -f $(INSTALL_DIR)/$(BINARY_FMAIL)
	@rm -f $(INSTALL_DIR)/$(BINARY_TUI)
	@echo "Removed $(BINARY_CLI), $(BINARY_DAEMON), $(BINARY_RUNNER), $(BINARY_FMAIL), and $(BINARY_TUI) from $(INSTALL_DIR)"

uninstall-rust:
	@echo "Removing Rust side-by-side binaries from $(RUST_INSTALL_DIR)..."
	@rm -f $(RUST_INSTALL_DIR)/$(RUST_BINARY_CLI)
	@rm -f $(RUST_INSTALL_DIR)/$(RUST_BINARY_DAEMON)
	@rm -f $(RUST_INSTALL_DIR)/$(RUST_BINARY_FMAIL)
	@rm -f $(RUST_INSTALL_DIR)/$(BINARY_TUI)
	@echo "Removed $(RUST_BINARY_CLI), $(RUST_BINARY_DAEMON), $(RUST_BINARY_FMAIL), and $(BINARY_TUI) from $(RUST_INSTALL_DIR)"

# Uninstall from system
uninstall-system:
	@echo "Removing from $(BINDIR) (may require sudo)..."
	@rm -f $(BINDIR)/$(BINARY_CLI)
	@rm -f $(BINDIR)/$(BINARY_DAEMON)
	@rm -f $(BINDIR)/$(BINARY_RUNNER)
	@rm -f $(BINDIR)/$(BINARY_FMAIL)
	@rm -f $(BINDIR)/$(BINARY_TUI)
	@echo "Removed $(BINARY_CLI), $(BINARY_DAEMON), $(BINARY_RUNNER), $(BINARY_FMAIL), and $(BINARY_TUI) from $(BINDIR)"

uninstall-rust-system:
	@echo "Removing Rust side-by-side binaries from $(BINDIR) (may require sudo)..."
	@rm -f $(BINDIR)/$(RUST_BINARY_CLI)
	@rm -f $(BINDIR)/$(RUST_BINARY_DAEMON)
	@rm -f $(BINDIR)/$(RUST_BINARY_FMAIL)
	@rm -f $(BINDIR)/$(BINARY_TUI)
	@echo "Removed $(RUST_BINARY_CLI), $(RUST_BINARY_DAEMON), $(RUST_BINARY_FMAIL), and $(BINARY_TUI) from $(BINDIR)"

forged-launchd-install:
	@scripts/install-macos-forged-launchagent.sh install

forged-launchd-uninstall:
	@scripts/install-macos-forged-launchagent.sh uninstall

forged-launchd-status:
	@scripts/install-macos-forged-launchagent.sh status

# Install using go install (builds and installs in one step)
go-install:
	@echo "Installing $(BINARY_CLI) via go install..."
	@cd $(GO_SRC_DIR) && $(GOCMD) install $(LDFLAGS) $(CMD_CLI)
	@echo "Installing $(BINARY_DAEMON) via go install..."
	@cd $(GO_SRC_DIR) && $(GOCMD) install $(LDFLAGS) $(CMD_DAEMON)
	@echo "Installing $(BINARY_RUNNER) via go install..."
	@cd $(GO_SRC_DIR) && $(GOCMD) install $(LDFLAGS) $(CMD_RUNNER)
	@echo "Installing $(BINARY_FMAIL) via go install..."
	@cd $(GO_SRC_DIR) && $(GOCMD) install $(LDFLAGS) $(CMD_FMAIL)
	@echo "Installed to $(GOBIN)"

## Test targets

# Run all tests
test:
	@echo "Running tests..."
	@cd $(GO_SRC_DIR) && $(GOTEST) -v -race -cover ./...

# Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	@mkdir -p $(BUILD_DIR)
	@cd $(GO_SRC_DIR) && $(GOTEST) -v -race -coverprofile=$(abspath $(BUILD_DIR))/coverage.out ./...
	$(GOCMD) tool cover -html=$(BUILD_DIR)/coverage.out -o $(BUILD_DIR)/coverage.html
	@echo "Coverage report: $(BUILD_DIR)/coverage.html"

# Run short tests only
test-short:
	@cd $(GO_SRC_DIR) && $(GOTEST) -v -short ./...

# Run Rust daemon runtime parity bring-up suite.
rust-daemon-runtime-parity:
	@scripts/rust-daemon-runtime-parity.sh

## Perf targets (gated; opt-in via build tags)

# Perf smoke: runs fast-ish budget checks on a synthetic fmail mailbox.
perf-smoke:
	@echo "Running fmail TUI perf smoke (tags=perf)..."
	@cd $(GO_SRC_DIR) && env -u GOROOT -u GOTOOLDIR $(GOTEST) -tags=perf ./internal/fmailtui/... -run TestPerfSmokeBudgets -count=1

# Perf benchmarks: captures baseline numbers for hot paths (provider/search).
perf-bench:
	@echo "Running fmail TUI perf benchmarks (tags=perf)..."
	@cd $(GO_SRC_DIR) && env -u GOROOT -u GOTOOLDIR $(GOTEST) -tags=perf ./internal/fmailtui/... -run '^$$' -bench Perf -benchmem -count=1

## Code quality targets

# Run linter (requires golangci-lint)
lint:
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest" && exit 1)
	@cd $(GO_SRC_DIR) && golangci-lint run ./...

# Format code
fmt:
	@echo "Formatting code..."
	@cd $(GO_SRC_DIR) && $(GOFMT) -s -w .

# Check formatting
fmt-check:
	@echo "Checking formatting..."
	@cd $(GO_SRC_DIR) && test -z "$$($(GOFMT) -l .)" || (echo "Code is not formatted. Run 'make fmt'" && exit 1)

# Run go vet
vet:
	@echo "Running vet..."
	@cd $(GO_SRC_DIR) && $(GOVET) ./...

# Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	@cd $(GO_SRC_DIR) && $(GOMOD) tidy

# Run all checks (for CI)
check: fmt-check vet lint test

## Protocol Buffers

# Generate protobuf code
proto:
	@echo "Generating protobuf code..."
	@which buf > /dev/null || (echo "buf not installed. Run: go install github.com/bufbuild/buf/cmd/buf@latest" && exit 1)
	@cd $(GO_SRC_DIR) && buf generate
	@echo "Generated code in $(GO_SRC_DIR)/gen/"

# Lint protobuf files
proto-lint:
	@echo "Linting protobuf files..."
	@which buf > /dev/null || (echo "buf not installed. Run: go install github.com/bufbuild/buf/cmd/buf@latest" && exit 1)
	@cd $(GO_SRC_DIR) && buf lint

# Update buf dependencies
proto-deps:
	@echo "Updating buf dependencies..."
	@cd $(GO_SRC_DIR) && buf dep update

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
	@echo "Forge - Control plane for AI coding agents"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Build Targets:"
	@echo "  build          Build CLI/daemon/runner/fmail/tui binaries to ./build/"
	@echo "  go-layout-guard Validate expected Go source layout for the current migration stage"
	@echo "  build-cli      Build only the CLI/TUI binary"
	@echo "  build-daemon   Build only the daemon binary"
	@echo "  build-tui      Build only the dedicated forge-tui binary"
	@echo "  build-all      Build for all platforms (cross-compile)"
	@echo "  build-rust     Build Rust binaries to ./build/ (rforge, rforged, rfmail, forge-tui)"
	@echo "  clean          Remove build artifacts"
	@echo ""
	@echo "Install Targets:"
	@echo "  install        Build and install to local bin dir (recommended; default: ~/.cargo/bin)"
	@echo "  install-local  Alias for install"
	@echo "  install-system Build and install to /usr/local/bin (requires sudo)"
	@echo "  install-rust   Build and install Rust side-by-side binaries to RUST_INSTALL_DIR (default: ~/.cargo/bin)"
	@echo "  install-rust-system Build and install Rust side-by-side binaries to /usr/local/bin (requires sudo)"
	@echo "  go-install     Use 'go install' directly"
	@echo "  uninstall      Remove from local bin dir (default: ~/.cargo/bin)"
	@echo "  uninstall-system Remove from /usr/local/bin (requires sudo)"
	@echo "  uninstall-rust Remove Rust side-by-side binaries from RUST_INSTALL_DIR"
	@echo "  uninstall-rust-system Remove Rust side-by-side binaries from /usr/local/bin (requires sudo)"
	@echo "  forged-launchd-install Install per-user forged LaunchAgent (macOS)"
	@echo "  forged-launchd-uninstall Uninstall per-user forged LaunchAgent (macOS)"
	@echo "  forged-launchd-status Show per-user forged LaunchAgent status (macOS)"
	@echo ""
	@echo "Development Targets:"
	@echo "  dev            Run the CLI without building"
	@echo "  test           Run all tests with race detector"
	@echo "  test-coverage  Run tests with HTML coverage report"
	@echo "  test-short     Run short tests only"
	@echo "  rust-daemon-runtime-parity Run Rust daemon runtime parity bring-up suite"
	@echo "  lint           Run golangci-lint"
	@echo "  fmt            Format code with gofmt"
	@echo "  vet            Run go vet"
	@echo "  tidy           Tidy legacy Go dependencies (old/go/go.mod)"
	@echo "  check          Run all checks (fmt, vet, lint, test)"
	@echo ""
	@echo "Protobuf Targets:"
	@echo "  proto          Generate protobuf code"
	@echo "  proto-lint     Lint protobuf files"
	@echo "  proto-deps     Update buf dependencies"
	@echo ""
	@echo "Quick Start:"
	@echo "  make build                    # Build to ./build/"
	@echo "  make build RUST_FIRST=0       # Build default binaries from legacy Go targets (old/go)"
	@echo "  make install                  # Build + install to local bin dir (~/.cargo/bin)"
	@echo "  sudo make install-system      # Build + install to /usr/local/bin"
	@echo ""
	@echo "Variables (override with VAR=value):"
	@echo "  VERSION        $(VERSION)"
	@echo "  COMMIT         $(COMMIT)"
	@echo "  PREFIX         $(PREFIX)"
	@echo "  BINDIR         $(BINDIR)"
	@echo "  LOCAL_BIN_DIR  $(LOCAL_BIN_DIR)"
	@echo "  INSTALL_DIR    $(INSTALL_DIR)"
	@echo "  GOBIN          $(GOBIN)"
	@echo "  GO_LAYOUT_MODE $(GO_LAYOUT_MODE)"
	@echo "  RUST_INSTALL_DIR $(RUST_INSTALL_DIR)"
	@echo "  RUST_FIRST     $(RUST_FIRST)"
