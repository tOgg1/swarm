// Package ssh provides abstractions for executing commands on remote nodes.
package ssh

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Executor defines a common interface for running commands over SSH.
type Executor interface {
	// Exec runs a command and returns its stdout and stderr output.
	Exec(ctx context.Context, cmd string) (stdout, stderr []byte, err error)

	// ExecInteractive runs a command, streaming stdin to the remote process.
	ExecInteractive(ctx context.Context, cmd string, stdin io.Reader) error

	// StartSession opens a long-lived SSH session for multiple commands.
	StartSession() (Session, error)

	// Close releases any resources held by the executor.
	Close() error
}

// Session represents a long-lived SSH session.
type Session interface {
	// Exec runs a command and returns its stdout and stderr output.
	Exec(ctx context.Context, cmd string) (stdout, stderr []byte, err error)

	// ExecInteractive runs a command, streaming stdin to the remote process.
	ExecInteractive(ctx context.Context, cmd string, stdin io.Reader) error

	// Close ends the session.
	Close() error
}

// ConnectionOptions configures how an SSH connection is established.
type ConnectionOptions struct {
	// Host is the target host name or IP.
	Host string

	// Port is the SSH port (defaults to 22 when unset).
	Port int

	// User is the SSH username.
	User string

	// KeyPath is an optional path to the private key.
	KeyPath string

	// AgentForwarding enables SSH agent forwarding when supported.
	AgentForwarding bool

	// ProxyJump specifies a bastion host to reach the target (user@host:port).
	ProxyJump string

	// ControlMaster configures SSH multiplexing (auto/yes/no).
	ControlMaster string

	// ControlPath is the socket path for SSH multiplexing.
	ControlPath string

	// ControlPersist controls how long master connections stay alive.
	ControlPersist string

	// Timeout controls how long to wait when establishing connections.
	Timeout time.Duration
}

// ApplySSHConfig applies settings from ~/.ssh/config to the connection options.
// It looks up the host alias and updates Host, Port, User, KeyPath, and ProxyJump
// based on matching Host directives.
//
// TODO(OrangeCreek): Full implementation in swarm-y6b.
func ApplySSHConfig(opts ConnectionOptions) (ConnectionOptions, error) {
	if strings.TrimSpace(opts.Host) == "" {
		return opts, nil
	}

	configPath, err := defaultSSHConfigPath()
	if err != nil {
		return opts, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return opts, nil
		}
		return opts, err
	}

	host := strings.TrimSpace(opts.Host)
	currentMatch := true
	lines := strings.Split(string(data), "\n")

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := strings.ToLower(fields[0])
		value := strings.Join(fields[1:], " ")

		switch key {
		case "host":
			currentMatch = matchesHostPatterns(host, fields[1:])
			continue
		case "match":
			// Ignore Match blocks for now.
			currentMatch = false
			continue
		}

		if !currentMatch {
			continue
		}

		switch key {
		case "hostname":
			if v := strings.TrimSpace(value); v != "" {
				opts.Host = v
			}
		case "user":
			if opts.User == "" {
				opts.User = strings.TrimSpace(value)
			}
		case "port":
			if opts.Port == 0 {
				port, err := strconv.Atoi(strings.TrimSpace(value))
				if err == nil {
					opts.Port = port
				}
			}
		case "identityfile":
			if opts.KeyPath == "" {
				if expanded := expandSSHPath(value); expanded != "" {
					opts.KeyPath = expanded
				}
			}
		case "proxyjump":
			if opts.ProxyJump == "" {
				proxy := normalizeProxyJump(value)
				if proxy != "" {
					opts.ProxyJump = proxy
				}
			}
		case "controlmaster":
			if opts.ControlMaster == "" {
				opts.ControlMaster = strings.TrimSpace(value)
			}
		case "controlpath":
			if opts.ControlPath == "" {
				if expanded := expandSSHPath(value); expanded != "" {
					opts.ControlPath = expanded
				}
			}
		case "controlpersist":
			if opts.ControlPersist == "" {
				opts.ControlPersist = strings.TrimSpace(value)
			}
		}
	}

	return opts, nil
}

func defaultSSHConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

func matchesHostPatterns(host string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}

	lowerHost := strings.ToLower(host)
	matched := false
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		negated := strings.HasPrefix(pattern, "!")
		if negated {
			pattern = strings.TrimPrefix(pattern, "!")
		}
		if pattern == "" {
			continue
		}

		if matchHostPattern(lowerHost, pattern) {
			if negated {
				return false
			}
			matched = true
		}
	}

	return matched
}

func matchHostPattern(host, pattern string) bool {
	lowerPattern := strings.ToLower(pattern)
	if lowerPattern == host {
		return true
	}
	matched, err := path.Match(lowerPattern, host)
	if err != nil {
		return false
	}
	return matched
}

func expandSSHPath(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.Trim(trimmed, "\"'")
	if trimmed == "" {
		return ""
	}

	expanded := os.ExpandEnv(trimmed)
	if strings.HasPrefix(expanded, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~"))
		}
	}
	return expanded
}

func normalizeProxyJump(value string) string {
	jumps := parseProxyJumpList(value)
	if len(jumps) == 0 {
		return ""
	}
	return strings.Join(jumps, ",")
}

func parseProxyJumpList(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.EqualFold(trimmed, "none") {
		return nil
	}

	parts := strings.Split(trimmed, ",")
	jumps := make([]string, 0, len(parts))
	for _, part := range parts {
		jump := strings.TrimSpace(part)
		if jump == "" {
			continue
		}
		if strings.EqualFold(jump, "none") {
			return nil
		}
		jumps = append(jumps, jump)
	}
	return jumps
}
