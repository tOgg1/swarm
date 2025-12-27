package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/opencode-ai/swarm/internal/logging"
)

var (
	// ErrMissingWorkspaceID indicates WorkspaceID was not provided.
	ErrMissingWorkspaceID = errors.New("workspace id is required")
	// ErrMissingAgentID indicates AgentID was not provided.
	ErrMissingAgentID = errors.New("agent id is required")
	// ErrMissingCommand indicates no command was provided to run.
	ErrMissingCommand = errors.New("command is required")
)

const (
	EventTypeHeartbeat    = "heartbeat"
	EventTypeInputSent    = "input_sent"
	EventTypeOutputLine   = "output_line"
	EventTypePromptReady  = "prompt_ready"
	EventTypeBusy         = "busy"
	EventTypePause        = "pause"
	EventTypeCooldown     = "cooldown"
	EventTypeSwapAccount  = "swap_account"
	EventTypeExit         = "exit"
	EventTypeControlError = "control_error"
)

var (
	defaultHeartbeatInterval = 5 * time.Second
	defaultTailLines         = 50
	defaultTailBytes         = 4096
	maxPendingBytes          = 16384
	maxEventLineLength       = 1024
	defaultPromptRegex       = regexp.MustCompile(`(?i)(\bready\b|\bidle\b|waiting for input|[>$%])\s*$`)
	defaultBusyRegex         = regexp.MustCompile(`(?i)(thinking|working|processing|generating)\b`)
)

// RunnerEvent represents a structured event emitted by the runner.
type RunnerEvent struct {
	Type        string    `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	Data        any       `json:"data,omitempty"`
}

// Runner manages a PTY-wrapped agent CLI process and emits structured events.
type Runner struct {
	WorkspaceID string
	AgentID     string
	Command     []string

	PromptRegex *regexp.Regexp
	BusyRegex   *regexp.Regexp

	HeartbeatInterval time.Duration
	TailLines         int
	TailBytes         int

	EventSink     EventSink
	ControlReader io.Reader
	OutputWriter  io.Writer
	Now           func() time.Time

	pty    *os.File
	cmd    *exec.Cmd
	output *LineRing

	stateMu      sync.Mutex
	ready        bool
	pausedUntil  time.Time
	lastActivity time.Time

	writeMu sync.Mutex
}

// ControlCommand describes a command sent to the runner via stdin or socket.
type ControlCommand struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Message   string `json:"message,omitempty"`
	Duration  string `json:"duration,omitempty"`
	Until     string `json:"until,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// HeartbeatData is emitted with heartbeat events.
type HeartbeatData struct {
	LastActivity time.Time `json:"last_activity"`
	IdleFor      string    `json:"idle_for"`
	Tail         []string  `json:"tail,omitempty"`
}

// InputSentData is emitted when input is sent to the agent.
type InputSentData struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// OutputLineData is emitted for each output line.
type OutputLineData struct {
	Line      string `json:"line"`
	Truncated bool   `json:"truncated,omitempty"`
}

// PromptReadyData is emitted when prompt readiness is detected.
type PromptReadyData struct {
	Reason string `json:"reason,omitempty"`
}

// BusyData is emitted when the runner detects a busy state.
type BusyData struct {
	Reason string `json:"reason,omitempty"`
}

// ControlData describes control actions like pause or cooldown.
type ControlData struct {
	Action    string `json:"action"`
	Duration  string `json:"duration,omitempty"`
	Until     string `json:"until,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// ControlErrorData captures control parsing errors.
type ControlErrorData struct {
	Error string `json:"error"`
	Raw   string `json:"raw,omitempty"`
}

// ExitData is emitted when the agent process exits.
type ExitData struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// DefaultPromptRegex returns the default prompt-ready detection regex.
func DefaultPromptRegex() *regexp.Regexp {
	return defaultPromptRegex
}

// DefaultBusyRegex returns the default busy detection regex.
func DefaultBusyRegex() *regexp.Regexp {
	return defaultBusyRegex
}

// Run starts the agent command, captures output, and emits events until exit.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.validate(); err != nil {
		return err
	}

	r.applyDefaults()
	logger := logging.Component("agent-runner")

	r.cmd = exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
	r.cmd.Env = os.Environ()

	ptyFile, err := pty.Start(r.cmd)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	r.pty = ptyFile
	defer r.pty.Close()

	r.setLastActivity(r.now())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	outputErrCh := make(chan error, 1)
	go r.readOutput(runCtx, outputErrCh)

	if r.ControlReader != nil {
		go r.controlLoop(runCtx)
	}

	go r.heartbeatLoop(runCtx)

	err = r.cmd.Wait()
	cancel()

	exitCode := 0
	exitErr := ""
	if err != nil {
		exitErr = err.Error()
		if exitStatus := new(exec.ExitError); errors.As(err, &exitStatus) {
			exitCode = exitStatus.ExitCode()
		} else {
			exitCode = 1
		}
	}

	r.emit(runCtx, EventTypeExit, ExitData{
		ExitCode: exitCode,
		Error:    exitErr,
	})

	if r.EventSink != nil {
		if closeErr := r.EventSink.Close(); closeErr != nil {
			logger.Warn().Err(closeErr).Msg("failed to close event sink")
		}
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ctx.Err()
		}
		return err
	}

	select {
	case outputErr := <-outputErrCh:
		if outputErr != nil && !errors.Is(outputErr, io.EOF) {
			logger.Warn().Err(outputErr).Msg("output reader error")
		}
	default:
	}

	return nil
}

// SendInput writes a message to the agent process.
func (r *Runner) SendInput(ctx context.Context, text string) error {
	if r == nil {
		return fmt.Errorf("runner is nil")
	}
	if r.pty == nil {
		return fmt.Errorf("runner has not started")
	}

	if err := r.waitForResume(ctx); err != nil {
		return err
	}

	payload := text
	if !strings.HasSuffix(payload, "\n") {
		payload += "\n"
	}

	r.writeMu.Lock()
	_, err := io.WriteString(r.pty, payload)
	r.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("write input: %w", err)
	}

	r.setLastActivity(r.now())
	preview, truncated := truncateText(text, maxEventLineLength)
	if preview != "" || text == "" {
		r.emit(ctx, EventTypeInputSent, InputSentData{Text: preview, Truncated: truncated})
	}

	if r.isReady() {
		r.setReady(ctx, false, "input_sent")
	}

	return nil
}

func (r *Runner) validate() error {
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return ErrMissingWorkspaceID
	}
	if strings.TrimSpace(r.AgentID) == "" {
		return ErrMissingAgentID
	}
	if len(r.Command) == 0 || strings.TrimSpace(r.Command[0]) == "" {
		return ErrMissingCommand
	}
	return nil
}

func (r *Runner) applyDefaults() {
	if r.HeartbeatInterval <= 0 {
		r.HeartbeatInterval = defaultHeartbeatInterval
	}
	if r.TailLines <= 0 {
		r.TailLines = defaultTailLines
	}
	if r.TailBytes <= 0 {
		r.TailBytes = defaultTailBytes
	}
	if r.PromptRegex == nil {
		r.PromptRegex = defaultPromptRegex
	}
	if r.BusyRegex == nil {
		r.BusyRegex = defaultBusyRegex
	}
	if r.EventSink == nil {
		r.EventSink = NoopSink{}
	}
	if r.OutputWriter == nil {
		r.OutputWriter = io.Discard
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.output == nil {
		r.output = NewLineRing(r.TailLines)
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) readOutput(ctx context.Context, errCh chan<- error) {
	logger := logging.Component("agent-runner")
	buf := make([]byte, 4096)
	pending := make([]byte, 0, 4096)
	tail := make([]byte, 0, r.TailBytes)

	for {
		n, err := r.pty.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, writeErr := r.OutputWriter.Write(chunk); writeErr != nil {
				logger.Warn().Err(writeErr).Msg("failed to write output")
			}

			r.setLastActivity(r.now())

			tail = append(tail, chunk...)
			if len(tail) > r.TailBytes {
				tail = tail[len(tail)-r.TailBytes:]
			}

			pending = append(pending, chunk...)
			lines, remainder := splitLines(pending)
			pending = remainder
			if len(pending) > maxPendingBytes {
				pending = pending[len(pending)-maxPendingBytes:]
			}

			for _, line := range lines {
				r.handleLine(ctx, line)
			}

			r.detectState(ctx, tail, chunk)
		}

		if err != nil {
			if err == io.EOF {
				errCh <- nil
				return
			}
			errCh <- err
			return
		}
	}
}

func (r *Runner) handleLine(ctx context.Context, line string) {
	r.output.Add(line)
	preview, truncated := truncateText(line, maxEventLineLength)
	r.emit(ctx, EventTypeOutputLine, OutputLineData{Line: preview, Truncated: truncated})
}

func (r *Runner) detectState(ctx context.Context, tail []byte, chunk []byte) {
	if r.PromptRegex != nil && r.PromptRegex.Match(tail) {
		r.setReady(ctx, true, "prompt_match")
		return
	}

	if r.BusyRegex != nil && r.BusyRegex.Match(tail) {
		r.setReady(ctx, false, "busy_match")
		return
	}

	if r.isReady() && containsNonWhitespace(chunk) {
		r.setReady(ctx, false, "output_received")
	}
}

func (r *Runner) setReady(ctx context.Context, ready bool, reason string) {
	r.stateMu.Lock()
	if r.ready == ready {
		r.stateMu.Unlock()
		return
	}
	r.ready = ready
	r.stateMu.Unlock()

	if ready {
		r.emit(ctx, EventTypePromptReady, PromptReadyData{Reason: reason})
		return
	}

	r.emit(ctx, EventTypeBusy, BusyData{Reason: reason})
}

func (r *Runner) isReady() bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.ready
}

func (r *Runner) setLastActivity(ts time.Time) {
	r.stateMu.Lock()
	r.lastActivity = ts
	r.stateMu.Unlock()
}

func (r *Runner) getLastActivity() time.Time {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.lastActivity
}

func (r *Runner) setPausedUntil(until time.Time) {
	r.stateMu.Lock()
	if until.After(r.pausedUntil) {
		r.pausedUntil = until
	}
	r.stateMu.Unlock()
}

func (r *Runner) getPausedUntil() time.Time {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.pausedUntil
}

func (r *Runner) waitForResume(ctx context.Context) error {
	for {
		until := r.getPausedUntil()
		if until.IsZero() {
			return nil
		}
		now := r.now()
		if !until.After(now) {
			return nil
		}
		timer := time.NewTimer(time.Until(until))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *Runner) heartbeatLoop(ctx context.Context) {
	if r.HeartbeatInterval <= 0 {
		return
	}

	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last := r.getLastActivity()
			now := r.now()
			idleFor := now.Sub(last)
			tail := truncateLines(r.output.Snapshot(), maxEventLineLength)
			r.emit(ctx, EventTypeHeartbeat, HeartbeatData{
				LastActivity: last,
				IdleFor:      idleFor.String(),
				Tail:         tail,
			})
		}
	}
}

func (r *Runner) controlLoop(ctx context.Context) {
	logger := logging.Component("agent-runner")
	scanner := bufio.NewScanner(r.ControlReader)
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		cmd, ok := parseControlCommand(trimmed)
		if !ok {
			if err := r.SendInput(ctx, line); err != nil {
				logger.Warn().Err(err).Msg("failed to forward input")
			}
			continue
		}

		r.handleControl(ctx, cmd, trimmed)
	}

	if err := scanner.Err(); err != nil {
		r.emit(ctx, EventTypeControlError, ControlErrorData{Error: err.Error()})
		logger.Warn().Err(err).Msg("control reader error")
	}
}

func (r *Runner) handleControl(ctx context.Context, cmd ControlCommand, raw string) {
	logger := logging.Component("agent-runner")
	cmdType := strings.TrimSpace(cmd.Type)
	switch cmdType {
	case "send_message", "send":
		text := strings.TrimSpace(cmd.Text)
		if text == "" {
			text = strings.TrimSpace(cmd.Message)
		}
		if text == "" {
			r.emit(ctx, EventTypeControlError, ControlErrorData{Error: "send_message requires text", Raw: raw})
			return
		}
		if err := r.SendInput(ctx, text); err != nil {
			logger.Warn().Err(err).Msg("failed to send input")
		}
	case "pause":
		dur, err := parseDuration(cmd.Duration)
		if err != nil {
			r.emit(ctx, EventTypeControlError, ControlErrorData{Error: err.Error(), Raw: raw})
			return
		}
		until := r.now().Add(dur)
		r.setPausedUntil(until)
		r.emit(ctx, EventTypePause, ControlData{Action: "pause", Duration: dur.String(), Until: until.Format(time.RFC3339)})
	case "cooldown":
		until, err := parseCooldown(cmd.Until, cmd.Duration, r.now())
		if err != nil {
			r.emit(ctx, EventTypeControlError, ControlErrorData{Error: err.Error(), Raw: raw})
			return
		}
		r.setPausedUntil(until)
		r.emit(ctx, EventTypeCooldown, ControlData{Action: "cooldown", Until: until.Format(time.RFC3339)})
	case "swap_account":
		accountID := strings.TrimSpace(cmd.AccountID)
		r.emit(ctx, EventTypeSwapAccount, ControlData{Action: "swap_account", AccountID: accountID})
	default:
		r.emit(ctx, EventTypeControlError, ControlErrorData{Error: "unknown control command", Raw: raw})
		logger.Warn().Str("command", cmdType).Msg("unknown control command")
	}
}

func (r *Runner) emit(ctx context.Context, eventType string, data any) {
	if r.EventSink == nil {
		return
	}

	event := RunnerEvent{
		Type:        eventType,
		Timestamp:   r.now(),
		WorkspaceID: r.WorkspaceID,
		AgentID:     r.AgentID,
		Data:        data,
	}

	if err := r.EventSink.Emit(ctx, event); err != nil {
		logger := logging.Component("agent-runner")
		logger.Warn().Err(err).Msg("failed to emit event")
	}
}

func parseControlCommand(line string) (ControlCommand, bool) {
	if !strings.HasPrefix(strings.TrimSpace(line), "{") {
		return ControlCommand{}, false
	}

	var probe map[string]any
	if err := json.Unmarshal([]byte(line), &probe); err != nil {
		return ControlCommand{}, false
	}

	if _, ok := probe["type"].(string); !ok {
		return ControlCommand{}, false
	}

	var cmd ControlCommand
	if err := json.Unmarshal([]byte(line), &cmd); err != nil {
		return ControlCommand{}, false
	}

	return cmd, true
}

func parseDuration(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("duration is required")
	}

	dur, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %w", err)
	}

	if dur <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}

	return dur, nil
}

func parseCooldown(until, duration string, now time.Time) (time.Time, error) {
	if strings.TrimSpace(until) != "" {
		timeValue, err := time.Parse(time.RFC3339, until)
		if err == nil {
			return timeValue.UTC(), nil
		}
		timeValue, err = time.Parse(time.RFC3339Nano, until)
		if err == nil {
			return timeValue.UTC(), nil
		}
	}

	if strings.TrimSpace(duration) != "" {
		dur, err := parseDuration(duration)
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(dur), nil
	}

	return time.Time{}, fmt.Errorf("cooldown requires until or duration")
}

func splitLines(buffer []byte) ([]string, []byte) {
	parts := bytes.Split(buffer, []byte{'\n'})
	if len(parts) == 0 {
		return nil, buffer
	}

	lines := make([]string, 0, len(parts))
	for i, part := range parts {
		if i == len(parts)-1 && len(buffer) > 0 && buffer[len(buffer)-1] != '\n' {
			return lines, part
		}
		line := strings.TrimRight(string(part), "\r")
		lines = append(lines, line)
	}
	return lines, nil
}

func containsNonWhitespace(data []byte) bool {
	for _, b := range data {
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return true
		}
	}
	return false
}

func truncateText(value string, max int) (string, bool) {
	if max <= 0 {
		return value, false
	}
	if len(value) <= max {
		return value, false
	}
	return value[:max], true
}

func truncateLines(lines []string, max int) []string {
	if len(lines) == 0 {
		return nil
	}

	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		truncated, _ := truncateText(line, max)
		trimmed = append(trimmed, truncated)
	}
	return trimmed
}
