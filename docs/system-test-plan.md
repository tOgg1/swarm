# Swarm System Test Plan

Comprehensive test plan covering all Swarm components, from unit tests to full end-to-end scenarios.

---

## Table of Contents

1. [Test Environment Setup](#1-test-environment-setup)
2. [Automated Unit Tests](#2-automated-unit-tests)
3. [Database & Migration Tests](#3-database--migration-tests)
4. [Node Management Tests](#4-node-management-tests)
5. [Workspace Management Tests](#5-workspace-management-tests)
6. [Agent Lifecycle Tests](#6-agent-lifecycle-tests)
7. [Queue & Scheduler Tests](#7-queue--scheduler-tests)
8. [Account & Credential Tests](#8-account--credential-tests)
9. [State Detection Tests](#9-state-detection-tests)
10. [SSH & Remote Execution Tests](#10-ssh--remote-execution-tests)
11. [Swarmd Daemon Tests](#11-swarmd-daemon-tests)
12. [TUI Tests](#12-tui-tests)
13. [CLI Integration Tests](#13-cli-integration-tests)
14. [End-to-End Scenarios](#14-end-to-end-scenarios)
15. [Performance Tests](#15-performance-tests)
16. [Security Tests](#16-security-tests)
17. [Test Results Template](#17-test-results-template)

---

## 1. Test Environment Setup

### 1.1 Prerequisites

| Requirement | Version | Check Command |
|-------------|---------|---------------|
| Go | 1.25+ | `go version` |
| Git | 2.x+ | `git --version` |
| tmux | 3.x+ | `tmux -V` |
| SSH | OpenSSH 8+ | `ssh -V` |
| SQLite | 3.x | `sqlite3 --version` |

### 1.2 Build Swarm

```bash
# Clone and build
git clone https://github.com/tOgg1/swarm.git
cd swarm
make build

# Verify binaries
./build/swarm --version
./build/swarmd --version
```

### 1.3 Test Database Setup

```bash
# Use isolated test database
export SWARM_DATA_DIR=$(mktemp -d)
./build/swarm migrate up
```

### 1.4 Test Node Setup

For integration tests, you'll need:
- **Local node**: The machine running tests
- **Remote node** (optional): SSH-accessible server for remote tests
- **tmux running**: `tmux new-session -d -s test`

---

## 2. Automated Unit Tests

### 2.1 Run All Unit Tests

```bash
# Run all tests
go test ./... -v

# Run with coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Run specific package
go test ./internal/scheduler/... -v
```

### 2.2 Test Package Summary

| Package | Tests | Focus Area |
|---------|-------|------------|
| `internal/account` | Account service, cooldowns, credential resolution |
| `internal/account/caam` | CAAM vault parsing (deprecated) |
| `internal/adapters` | Agent adapters (OpenCode, Claude, Codex, Gemini, Generic) |
| `internal/agent` | Agent service, pane mapping, archive |
| `internal/agentmail` | Agent Mail MCP detection |
| `internal/beads` | Beads issue tracker integration |
| `internal/cli` | CLI command tests, output formatting |
| `internal/config` | Configuration loading, approval policies |
| `internal/db` | Repository tests for all entities |
| `internal/events` | Event publishing, logging, retention |
| `internal/logging` | Log redaction, formatting |
| `internal/models` | Model validation, account states |
| `internal/node` | Node service, doctor, fallback |
| `internal/queue` | Queue service |
| `internal/scheduler` | Dispatch conditions, scheduling |
| `internal/ssh` | SSH executors (native, system, local), keys, forwarding |
| `internal/state` | State engine, polling, transcript parsing, snapshots |
| `internal/swarmd` | Daemon, gRPC client/server, rate limiting |
| `internal/tmux` | tmux client, layouts, snapshots, transcripts |
| `internal/tui/components` | TUI components (cards, panels, viewers) |
| `internal/vault` | Credential vault, profiles, encrypted storage |
| `internal/workspace` | Workspace service, recovery, repo detection |

### 2.3 Expected Results

```bash
# All packages should pass
go test ./... 2>&1 | grep -c "^ok"
# Expected: 24 packages OK
```

---

## 3. Database & Migration Tests

### 3.1 Fresh Database Setup

```bash
# Test: Initialize new database
rm -f ~/.local/share/swarm/swarm.db
swarm migrate up

# Expected: All migrations applied (currently 4)
# Verify:
swarm migrate status --json
```

### 3.2 Migration Up/Down

```bash
# Test: Migrate up to specific version
swarm migrate up --to 2

# Test: Migrate down
swarm migrate down --steps 1

# Test: Full up then verify
swarm migrate up
swarm migrate version
# Expected: Version 4
```

### 3.3 Schema Verification

```bash
# Verify tables exist
sqlite3 ~/.local/share/swarm/swarm.db ".tables"

# Expected tables:
# - nodes
# - workspaces  
# - agents
# - accounts
# - queue_items
# - events
# - approvals
# - usage_history
# - schema_version
```

---

## 4. Node Management Tests

### 4.1 Local Node

```bash
# Test: Add local node
swarm node add --name test-local --local

# Verify:
swarm node list --json | jq '.[] | select(.name=="test-local")'
# Expected: Node with is_local=true
```

### 4.2 Remote Node (SSH)

```bash
# Test: Add remote node (requires accessible SSH host)
swarm node add --name test-remote --ssh user@hostname

# Test: Add with custom key
swarm node add --name test-remote-2 --ssh user@hostname --key ~/.ssh/custom_key

# Test: Skip connection test
swarm node add --name untested --ssh user@unreachable --no-test
```

### 4.3 Node Operations

```bash
# Test: Doctor check
swarm node doctor test-local
# Expected: Checks for tmux, git, agent CLIs

# Test: Execute command
swarm node exec test-local -- uname -a
# Expected: System info output

# Test: Refresh status
swarm node refresh test-local
swarm node list --json | jq '.[] | select(.name=="test-local") | .status'
# Expected: "online"
```

### 4.4 Node Removal

```bash
# Test: Remove node (should fail if workspaces exist)
swarm node remove test-local

# Test: Force remove
swarm node remove test-local --force
```

### 4.5 SSH Tunneling

```bash
# Test: Port forward
swarm node forward test-remote --local-port 8080 --remote 127.0.0.1:3000 &
curl http://localhost:8080
# Expected: Response from remote service

# Test: Swarmd tunnel shortcut
swarm node tunnel test-remote
# Expected: Tunnel to swarmd on remote
```

---

## 5. Workspace Management Tests

### 5.1 Create Workspace

```bash
# Setup: Create test repo
mkdir -p /tmp/test-repo && cd /tmp/test-repo && git init

# Test: Create workspace
swarm ws create --node test-local --path /tmp/test-repo

# Verify:
swarm ws list --json
# Expected: Workspace with node_id, repo_path, tmux_session
```

### 5.2 Import Existing Session

```bash
# Setup: Create tmux session manually
cd /tmp/test-repo
tmux new-session -d -s existing-session

# Test: Import
swarm ws import --node test-local --session existing-session

# Verify:
swarm ws list | grep existing-session
```

### 5.3 Workspace Status

```bash
# Test: Status command
swarm ws status <workspace-id>

# Test: JSON output
swarm ws status <workspace-id> --json
# Expected: JSON with agents, git status, tmux info
```

### 5.4 Workspace Attach

```bash
# Test: Attach to workspace tmux session
swarm ws attach <workspace-id>
# Expected: Opens tmux session
# Exit with: Ctrl-b d
```

### 5.5 Workspace Removal

```bash
# Test: Remove without destroying tmux
swarm ws remove <workspace-id>

# Test: Remove and destroy tmux session
swarm ws remove <workspace-id> --destroy
# Verify: tmux list-sessions should not show the session
```

### 5.6 Beads Integration

```bash
# Setup: Initialize beads in workspace
cd /tmp/test-repo
bd init

# Test: Beads status
swarm ws beads-status <workspace-id>
# Expected: Shows beads issues
```

---

## 6. Agent Lifecycle Tests

### 6.1 Spawn Agent

```bash
# Test: Spawn single agent
swarm agent spawn --workspace <ws-id> --type opencode --count 1

# Test: Spawn multiple agents
swarm agent spawn --workspace <ws-id> --type claude --count 3

# Verify:
swarm agent list --workspace <ws-id>
# Expected: Agent(s) listed with status
```

### 6.2 Agent Status

```bash
# Test: Get agent status
swarm agent status <agent-id>

# Test: JSON output
swarm agent status <agent-id> --json
# Expected: state, reason, queue_length, account info
```

### 6.3 Send Messages

```bash
# Test: Send inline message
swarm agent send <agent-id> "Hello, please list files"

# Test: Send from file
echo "Describe the codebase structure" > /tmp/prompt.txt
swarm agent send <agent-id> --file /tmp/prompt.txt

# Test: Send from stdin
echo "What is 2+2?" | swarm agent send <agent-id> --stdin

# Test: Send with editor (opens $EDITOR)
swarm agent send <agent-id> --editor
```

### 6.4 Agent Control

```bash
# Test: Pause agent
swarm agent pause <agent-id> --duration 5m

# Verify: Status shows paused
swarm agent status <agent-id> | grep -i paused

# Test: Resume agent
swarm agent resume <agent-id>

# Test: Interrupt agent
swarm agent interrupt <agent-id>

# Test: Restart agent
swarm agent restart <agent-id>

# Test: Terminate agent
swarm agent terminate <agent-id>
```

### 6.5 Queue Management

```bash
# Test: Queue multiple messages
cat > /tmp/prompts.txt <<EOF
First: List all files
Second: Show git status
Third: Describe main.go
EOF
swarm agent queue <agent-id> --file /tmp/prompts.txt

# Verify queue:
swarm agent status <agent-id> --json | jq '.queue_length'
# Expected: 3
```

---

## 7. Queue & Scheduler Tests

### 7.1 Queue Operations

```bash
# Covered by unit tests: internal/queue/service_test.go
go test ./internal/queue/... -v
```

### 7.2 Dispatch Conditions

```bash
# Covered by unit tests: internal/scheduler/condition_test.go
go test ./internal/scheduler/... -v

# Test conditions:
# - Wait for idle
# - Wait for cooldown clear
# - Time-based gates
```

### 7.3 Scheduler Behavior

```bash
# Test: Queue with condition
# (Requires extending CLI or using internal tests)

# Verify scheduler processes queue items in order
# Verify scheduler respects pause durations
# Verify scheduler handles agent state changes
```

---

## 8. Account & Credential Tests

### 8.1 Add Account

```bash
# Test: Interactive add
swarm accounts add
# Follow prompts: provider, profile, credential source

# Test: Non-interactive add with env var
swarm accounts add --provider anthropic --profile work \
  --credential-ref 'env:ANTHROPIC_API_KEY' --non-interactive

# Test: Add with file credential
swarm accounts add --provider openai --profile default \
  --credential-ref 'file:/path/to/key.txt' --non-interactive
```

### 8.2 List Accounts

```bash
# Test: List all
swarm accounts list

# Test: Filter by provider
swarm accounts list --provider anthropic

# Test: JSON output
swarm accounts list --json
```

### 8.3 Cooldown Management

```bash
# Test: Set cooldown
swarm accounts cooldown set <account-id> --until 30m

# Verify:
swarm accounts cooldown list

# Test: Clear cooldown
swarm accounts cooldown clear <account-id>
```

### 8.4 Account Rotation

```bash
# Test: Rotate agent to new account
swarm accounts rotate <agent-id> --reason "rate-limit"

# Verify: Agent now uses different account
swarm agent status <agent-id> --json | jq '.account_id'
```

### 8.5 Vault Credential Tests

```bash
# Test: Initialize vault
swarm vault init

# Test: Backup current auth
swarm vault backup claude work

# Test: List profiles
swarm vault list

# Test: Activate profile
swarm vault activate claude work

# Test: Status
swarm vault status

# Test: vault: credential reference
swarm accounts add --provider anthropic --profile vault-test \
  --credential-ref 'vault:claude/work' --non-interactive
```

---

## 9. State Detection Tests

### 9.1 State Engine

```bash
# Covered by unit tests
go test ./internal/state/... -v
```

### 9.2 Manual State Verification

```bash
# Test: Agent state after spawn
swarm agent spawn --workspace <ws-id> --type opencode --count 1
sleep 2
swarm agent status <agent-id> --json | jq '.state'
# Expected: "idle" or "working"

# Test: State after sending message
swarm agent send <agent-id> "Hello"
swarm agent status <agent-id> --json | jq '.state'
# Expected: "working" (briefly)

# Test: State reason
swarm agent status <agent-id> --json | jq '.state_reason'
# Expected: Human-readable explanation
```

### 9.3 Transcript Parsing

```bash
# Covered by: internal/state/transcript_parser_test.go
# Test that transcript patterns are correctly identified:
# - Idle patterns
# - Working patterns
# - Approval requests
# - Rate limit messages
# - Error patterns
```

---

## 10. SSH & Remote Execution Tests

### 10.1 SSH Backend Tests

```bash
# Unit tests
go test ./internal/ssh/... -v

# Tests cover:
# - Native Go SSH client
# - System ssh binary fallback
# - Local execution (no SSH)
# - Key management
# - Known hosts handling
# - SSH config parsing
# - Port forwarding
```

### 10.2 Remote Command Execution

```bash
# Test: Execute on remote
swarm node exec test-remote -- whoami
swarm node exec test-remote -- tmux -V
swarm node exec test-remote -- ls -la

# Test: With timeout
swarm node exec test-remote --timeout 5s -- sleep 10
# Expected: Timeout error
```

### 10.3 SSH Configuration

```bash
# Test: Node with proxy jump
swarm node add --name behind-bastion --ssh user@internal \
  --proxy-jump user@bastion

# Test: Node with control master
swarm node add --name fast-node --ssh user@host \
  --control-master

# Test: Custom SSH backend
swarm node add --name native-ssh --ssh user@host \
  --ssh-backend native
```

---

## 11. Swarmd Daemon Tests

### 11.1 Daemon Unit Tests

```bash
go test ./internal/swarmd/... -v
```

### 11.2 Start Daemon

```bash
# Test: Start daemon
./build/swarmd &
SWARMD_PID=$!

# Verify: Health check
grpcurl -plaintext localhost:50051 swarmd.v1.SwarmDaemon/Health

# Cleanup
kill $SWARMD_PID
```

### 11.3 Resource Monitoring

```bash
# Test: Resource stats (requires running daemon)
grpcurl -plaintext localhost:50051 swarmd.v1.SwarmDaemon/GetResourceStats
```

### 11.4 Rate Limiting

```bash
# Covered by: internal/swarmd/ratelimit_test.go
# Tests rate limit detection and backoff
```

---

## 12. TUI Tests

### 12.1 TUI Component Tests

```bash
go test ./internal/tui/... -v

# Component tests:
# - Agent card rendering
# - Beads panel
# - Empty state
# - Git panel
# - Spinner
# - Transcript viewer
```

### 12.2 Manual TUI Testing

```bash
# Test: Launch TUI
swarm

# Verify:
# - Fleet dashboard renders
# - Nodes list shows nodes
# - Workspaces display
# - Keyboard navigation works (j/k, Enter, q)
# - Command palette opens (?)
```

### 12.3 TUI Responsiveness

```bash
# Test: Resize terminal
# Expected: TUI adapts to new size

# Test: Rapid key presses
# Expected: No lag or missed inputs

# Test: Large data sets
# Create 10+ workspaces, 20+ agents
# Expected: Smooth scrolling, no freezes
```

---

## 13. CLI Integration Tests

### 13.1 Output Formats

```bash
# Test: Human output
swarm node list

# Test: JSON output
swarm node list --json | jq '.'

# Test: JSONL output (where supported)
swarm export events --jsonl | head -5
```

### 13.2 Watch Mode

```bash
# Test: Watch for changes
swarm export events --watch --jsonl &
WATCH_PID=$!

# Trigger event
swarm agent spawn --workspace <ws-id> --type opencode --count 1

# Verify event appears
# Cleanup
kill $WATCH_PID
```

### 13.3 Error Handling

```bash
# Test: Invalid command
swarm invalid-command 2>&1
# Expected: Error message with suggestion

# Test: Missing required flag
swarm node add 2>&1
# Expected: Error about missing --name or --ssh/--local

# Test: Invalid JSON
swarm node add --name test --local --config '{invalid'
# Expected: Clear parse error
```

### 13.4 Preflight Checks

```bash
# Test: Without database
mv ~/.local/share/swarm/swarm.db ~/.local/share/swarm/swarm.db.bak
swarm ws list 2>&1
# Expected: Error about database, suggests swarm migrate up
mv ~/.local/share/swarm/swarm.db.bak ~/.local/share/swarm/swarm.db

# Test: Without tmux (for tmux-requiring commands)
# Expected: Error about tmux not installed
```

---

## 14. End-to-End Scenarios

### 14.1 Scenario: Single Agent Workflow

```bash
# 1. Setup
swarm node add --name local --local
swarm ws create --node local --path /path/to/repo
WS_ID=$(swarm ws list --json | jq -r '.[0].id')

# 2. Spawn agent
swarm agent spawn --workspace $WS_ID --type opencode --count 1
AGENT_ID=$(swarm agent list --workspace $WS_ID --json | jq -r '.[0].id')

# 3. Interact
swarm agent send $AGENT_ID "List all Go files"
sleep 5
swarm agent status $AGENT_ID

# 4. Queue work
swarm agent queue $AGENT_ID --file prompts.txt

# 5. Monitor
swarm ws status $WS_ID

# 6. Cleanup
swarm agent terminate $AGENT_ID
swarm ws remove $WS_ID --destroy
swarm node remove local --force
```

### 14.2 Scenario: Multi-Agent Parallel Work

```bash
# 1. Setup workspace
swarm ws create --node local --path /path/to/repo
WS_ID=$(swarm ws list --json | jq -r '.[0].id')

# 2. Spawn multiple agents
swarm agent spawn --workspace $WS_ID --type opencode --count 3

# 3. Assign different tasks
AGENTS=$(swarm agent list --workspace $WS_ID --json | jq -r '.[].id')
echo "$AGENTS" | while read AGENT_ID; do
  swarm agent send $AGENT_ID "Work on assigned task"
done

# 4. Monitor all agents
swarm ws status $WS_ID

# 5. Pause all on cooldown
for AGENT_ID in $AGENTS; do
  swarm agent pause $AGENT_ID --duration 5m
done
```

### 14.3 Scenario: Account Rotation on Rate Limit

```bash
# 1. Setup multiple accounts
swarm accounts add --provider anthropic --profile account1 \
  --credential-ref 'env:ANTHROPIC_API_KEY_1' --non-interactive
swarm accounts add --provider anthropic --profile account2 \
  --credential-ref 'env:ANTHROPIC_API_KEY_2' --non-interactive

# 2. Spawn agent with first account
swarm agent spawn --workspace $WS_ID --type claude --profile account1

# 3. Simulate rate limit (set cooldown)
AGENT_ID=$(swarm agent list --workspace $WS_ID --json | jq -r '.[0].id')
ACCOUNT_ID=$(swarm agent status $AGENT_ID --json | jq -r '.account_id')
swarm accounts cooldown set $ACCOUNT_ID --until 30m

# 4. Rotate to available account
swarm accounts rotate $AGENT_ID --reason rate-limit

# 5. Verify rotation
swarm agent status $AGENT_ID --json | jq '.account_id'
# Expected: Different account
```

### 14.4 Scenario: Remote Node Workflow

```bash
# 1. Add remote node
swarm node add --name remote-server --ssh user@hostname

# 2. Verify connectivity
swarm node doctor remote-server

# 3. Create workspace on remote
swarm ws create --node remote-server --path /home/user/project
WS_ID=$(swarm ws list --json | jq -r '.[] | select(.node_name=="remote-server") | .id')

# 4. Spawn agent
swarm agent spawn --workspace $WS_ID --type opencode --count 1

# 5. Attach to remote session
swarm ws attach $WS_ID

# 6. Monitor remotely
swarm ws status $WS_ID
```

### 14.5 Scenario: Workspace Recovery

```bash
# 1. Create orphaned tmux session
tmux new-session -d -s orphan-session
tmux send-keys -t orphan-session "cd /path/to/repo" Enter

# 2. Import session
swarm ws import --node local --session orphan-session

# 3. Verify import
swarm ws list | grep orphan-session
```

---

## 15. Performance Tests

### 15.1 Agent Spawn Time

```bash
# Test: Time to spawn agent
time swarm agent spawn --workspace $WS_ID --type opencode --count 1
# Target: < 2 seconds
```

### 15.2 State Polling Latency

```bash
# Test: State update latency
# 1. Spawn agent
# 2. Measure time from agent becoming idle to Swarm detecting it
# Target: < 5 seconds (polling interval)
```

### 15.3 Queue Throughput

```bash
# Test: Queue 100 items
for i in {1..100}; do echo "Task $i"; done > /tmp/100tasks.txt
time swarm agent queue $AGENT_ID --file /tmp/100tasks.txt
# Target: < 1 second
```

### 15.4 TUI Responsiveness

```bash
# Test: TUI with many entities
# Create 50 workspaces, 100 agents
# Navigate quickly through lists
# Target: No perceptible lag
```

### 15.5 Vault Operations

```bash
# Test: Profile activation
time swarm vault activate claude work
# Target: < 100ms
```

---

## 16. Security Tests

### 16.1 Credential Handling

```bash
# Test: Credentials not in logs
ANTHROPIC_API_KEY="sk-ant-secret123" swarm agent spawn \
  --workspace $WS_ID --type claude 2>&1 | grep -c "sk-ant"
# Expected: 0 (not found)

# Test: Credentials not in database
sqlite3 ~/.local/share/swarm/swarm.db \
  "SELECT * FROM accounts" | grep -c "sk-ant"
# Expected: 0 (credential_ref stored, not actual key)
```

### 16.2 File Permissions

```bash
# Test: Database permissions
ls -la ~/.local/share/swarm/swarm.db
# Expected: -rw------- (600)

# Test: Vault file permissions
ls -la ~/.config/swarm/vault/profiles/anthropic/work/
# Expected: Directory 700, files 600
```

### 16.3 SSH Key Security

```bash
# Test: SSH keys not logged
swarm node add --name test --ssh user@host 2>&1 | grep -c "PRIVATE"
# Expected: 0

# Test: SSH agent forwarding (if enabled)
# Verify agent socket not exposed publicly
```

---

## 17. Test Results Template

### Summary

| Category | Total | Pass | Fail | Skip | Notes |
|----------|-------|------|------|------|-------|
| Unit Tests | | | | | `go test ./...` |
| Database | | | | | |
| Node Management | | | | | |
| Workspace Management | | | | | |
| Agent Lifecycle | | | | | |
| Queue & Scheduler | | | | | |
| Account & Credentials | | | | | |
| State Detection | | | | | |
| SSH & Remote | | | | | |
| Swarmd Daemon | | | | | |
| TUI | | | | | |
| CLI Integration | | | | | |
| E2E Scenarios | | | | | |
| Performance | | | | | |
| Security | | | | | |
| **Total** | | | | | |

### Environment

```
Date: YYYY-MM-DD
Tester: 
Swarm Version: 
Go Version: 
OS: 
tmux Version: 
SSH Version: 
```

### Issues Found

| ID | Category | Severity | Description | Steps to Reproduce | Status |
|----|----------|----------|-------------|-------------------|--------|
| | | | | | |

### Notes

- 
- 
- 

---

## Appendix: Quick Test Commands

```bash
# Run all unit tests
go test ./... -v

# Run with race detector
go test ./... -race

# Run with coverage
go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out

# Run specific test
go test ./internal/scheduler/... -run TestDispatchCondition -v

# Build and quick smoke test
make build && ./build/swarm --version && ./build/swarm node list

# Full E2E smoke test
./scripts/smoke-test.sh  # (if exists)
```
