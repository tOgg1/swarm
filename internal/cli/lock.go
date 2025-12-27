// Package cli provides lock management commands.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	defaultAgentMailURL     = "http://127.0.0.1:8765/mcp/"
	defaultAgentMailTimeout = 5 * time.Second
	defaultLockTTL          = time.Hour
	minLockTTL              = time.Minute
)

var (
	lockClaimAgent     string
	lockClaimPaths     []string
	lockClaimTTL       time.Duration
	lockClaimExclusive bool
	lockClaimReason    string
	lockClaimForce     bool

	lockReleaseAgent string
	lockReleasePaths []string
	lockReleaseIDs   []string

	lockStatusAgent string
	lockStatusPaths []string

	lockCheckPaths []string
)

func init() {
	rootCmd.AddCommand(lockCmd)
	lockCmd.AddCommand(lockClaimCmd)
	lockCmd.AddCommand(lockReleaseCmd)
	lockCmd.AddCommand(lockStatusCmd)
	lockCmd.AddCommand(lockCheckCmd)

	lockClaimCmd.Flags().StringVarP(&lockClaimAgent, "agent", "a", "", "agent name (Agent Mail)")
	lockClaimCmd.Flags().StringSliceVarP(&lockClaimPaths, "path", "p", nil, "file path or glob pattern (repeatable)")
	lockClaimCmd.Flags().DurationVar(&lockClaimTTL, "ttl", defaultLockTTL, "lock duration (e.g., 30m)")
	lockClaimCmd.Flags().BoolVar(&lockClaimExclusive, "exclusive", true, "exclusive lock (true/false)")
	lockClaimCmd.Flags().StringVar(&lockClaimReason, "reason", "", "reason for the lock")
	lockClaimCmd.Flags().BoolVar(&lockClaimForce, "force", false, "force release conflicting locks (requires confirmation)")

	lockReleaseCmd.Flags().StringVarP(&lockReleaseAgent, "agent", "a", "", "agent name (Agent Mail)")
	lockReleaseCmd.Flags().StringSliceVarP(&lockReleasePaths, "path", "p", nil, "file path or glob pattern (repeatable)")
	lockReleaseCmd.Flags().StringSliceVar(&lockReleaseIDs, "lock-id", nil, "lock ID to release (repeatable)")

	lockStatusCmd.Flags().StringVarP(&lockStatusAgent, "agent", "a", "", "filter by agent name (Agent Mail)")
	lockStatusCmd.Flags().StringSliceVarP(&lockStatusPaths, "path", "p", nil, "filter by file path or glob pattern")

	lockCheckCmd.Flags().StringSliceVarP(&lockCheckPaths, "path", "p", nil, "file path to check (repeatable)")
}

var lockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Manage advisory file locks",
	Long:  "Manage advisory file locks via Agent Mail for multi-agent coordination.",
}

var lockClaimCmd = &cobra.Command{
	Use:   "claim",
	Short: "Claim file locks",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		if len(lockClaimPaths) == 0 {
			return errors.New("at least one --path is required")
		}

		cfg, err := resolveAgentMailConfig()
		if err != nil {
			return err
		}

		agentName, err := resolveAgentMailAgent(lockClaimAgent, cfg.Agent)
		if err != nil {
			return err
		}

		ttlSeconds := int(lockClaimTTL.Round(time.Second).Seconds())
		if ttlSeconds < int(minLockTTL.Seconds()) {
			return fmt.Errorf("ttl must be at least %s", minLockTTL)
		}

		client := newAgentMailClient(cfg.URL, cfg.Timeout)
		claimResult, err := claimLocks(ctx, client, cfg.Project, agentName, lockClaimPaths, ttlSeconds, lockClaimExclusive, lockClaimReason)
		if err != nil {
			return err
		}

		if len(claimResult.Conflicts) > 0 {
			if lockClaimForce {
				if !confirm("Force release conflicting locks?") {
					return errors.New("lock claim aborted")
				}

				if err := forceReleaseConflicts(ctx, client, cfg.Project, agentName, claimResult.Conflicts, lockClaimReason); err != nil {
					return err
				}

				claimResult, err = claimLocks(ctx, client, cfg.Project, agentName, lockClaimPaths, ttlSeconds, lockClaimExclusive, lockClaimReason)
				if err != nil {
					return err
				}
			} else {
				if IsJSONOutput() || IsJSONLOutput() {
					return WriteOutput(os.Stdout, claimResult)
				}
				printLockConflicts(claimResult.Conflicts)
				return errors.New("lock conflicts detected")
			}
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, claimResult)
		}

		fmt.Println("Lock claimed:")
		fmt.Printf("  Agent:   %s\n", agentName)
		fmt.Printf("  Paths:   %s\n", strings.Join(lockClaimPaths, ", "))
		fmt.Printf("  TTL:     %s\n", lockClaimTTL)
		if len(claimResult.Granted) > 0 {
			fmt.Println("  Grants:")
			for _, grant := range claimResult.Granted {
				expires, _ := parseAgentMailTime(grant.ExpiresTS)
				fmt.Printf("    - %s (id %d, expires %s)\n", grant.PathPattern, grant.ID, formatTimeUntil(expires))
			}
		}

		return nil
	},
}

var lockReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release file locks",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		cfg, err := resolveAgentMailConfig()
		if err != nil {
			return err
		}

		agentName, err := resolveAgentMailAgent(lockReleaseAgent, cfg.Agent)
		if err != nil {
			return err
		}

		lockIDs, err := parseLockIDs(lockReleaseIDs)
		if err != nil {
			return err
		}

		client := newAgentMailClient(cfg.URL, cfg.Timeout)
		result, err := releaseLocks(ctx, client, cfg.Project, agentName, lockReleasePaths, lockIDs)
		if err != nil {
			return err
		}

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, result)
		}

		fmt.Printf("Released %d lock(s)\n", result.Released)
		return nil
	},
}

var lockStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show lock status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		cfg, err := resolveAgentMailConfig()
		if err != nil {
			return err
		}

		client := newAgentMailClient(cfg.URL, cfg.Timeout)
		claims, err := listFileReservations(ctx, client, cfg.Project, true)
		if err != nil {
			return err
		}

		filtered := filterFileReservations(claims, lockStatusAgent, lockStatusPaths)

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, filtered)
		}

		if len(filtered) == 0 {
			fmt.Println("No locks found")
			return nil
		}

		rows := make([][]string, 0, len(filtered))
		for _, claim := range filtered {
			expires, _ := parseAgentMailTime(claim.ExpiresTS)
			rows = append(rows, []string{
				fmt.Sprintf("%d", claim.ID),
				claim.Agent,
				claim.PathPattern,
				formatTimeUntil(expires),
				formatYesNo(claim.Exclusive),
			})
		}

		return writeTable(os.Stdout, []string{"LOCK-ID", "AGENT", "PATH", "EXPIRES", "EXCLUSIVE"}, rows)
	},
}

var lockCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check if a path is locked",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		if len(lockCheckPaths) == 0 {
			return errors.New("--path is required")
		}

		cfg, err := resolveAgentMailConfig()
		if err != nil {
			return err
		}

		client := newAgentMailClient(cfg.URL, cfg.Timeout)
		claims, err := listFileReservations(ctx, client, cfg.Project, true)
		if err != nil {
			return err
		}

		results := buildCheckResults(lockCheckPaths, claims)

		if IsJSONOutput() || IsJSONLOutput() {
			return WriteOutput(os.Stdout, results)
		}

		for _, result := range results {
			if len(result.Claims) == 0 {
				fmt.Printf("Path is clear: %s\n", result.Path)
				continue
			}
			fmt.Printf("Path is locked: %s\n", result.Path)
			for _, claim := range result.Claims {
				expires, _ := parseAgentMailTime(claim.ExpiresTS)
				fmt.Printf("  Holder: %s\n", claim.Agent)
				fmt.Printf("  Pattern: %s\n", claim.PathPattern)
				fmt.Printf("  Expires: %s\n", formatTimeUntil(expires))
			}
		}

		return nil
	},
}

type agentMailConfig struct {
	URL     string
	Project string
	Agent   string
	Timeout time.Duration
}

type agentMailClient struct {
	url        string
	httpClient *http.Client
}

type mcpResourceRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Params  mcpResourceParams `json:"params"`
}

type mcpResourceParams struct {
	URI string `json:"uri"`
}

type mcpResourceResponse struct {
	Result mcpResourceResult `json:"result"`
	Error  *mcpResponseError `json:"error"`
}

type mcpResourceResult struct {
	Contents []mcpResourceContent `json:"contents"`
}

type mcpResourceContent struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type mcpResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  mcpToolParams `json:"params"`
}

type mcpToolParams struct {
	Name      string      `json:"name"`
	Arguments interface{} `json:"arguments"`
}

type mcpToolResponse struct {
	Result json.RawMessage   `json:"result"`
	Error  *mcpResponseError `json:"error"`
}

type fileReservation struct {
	ID          int64  `json:"id"`
	Agent       string `json:"agent"`
	PathPattern string `json:"path_pattern"`
	Exclusive   bool   `json:"exclusive"`
	Reason      string `json:"reason"`
	CreatedTS   string `json:"created_ts"`
	ExpiresTS   string `json:"expires_ts"`
	ReleasedTS  string `json:"released_ts"`
}

type lockClaimResponse struct {
	Granted   []fileReservationGrant    `json:"granted"`
	Conflicts []fileReservationConflict `json:"conflicts"`
}

type fileReservationGrant struct {
	ID          int64  `json:"id"`
	PathPattern string `json:"path_pattern"`
	Exclusive   bool   `json:"exclusive"`
	Reason      string `json:"reason"`
	ExpiresTS   string `json:"expires_ts"`
}

type fileReservationConflict struct {
	Path    string                  `json:"path"`
	Holders []fileReservationHolder `json:"holders"`
}

type fileReservationHolder struct {
	ID          int64  `json:"id"`
	Agent       string `json:"agent"`
	PathPattern string `json:"path_pattern"`
	Exclusive   bool   `json:"exclusive"`
	ExpiresTS   string `json:"expires_ts"`
}

type lockReleaseResponse struct {
	Released   int    `json:"released"`
	ReleasedAt string `json:"released_at"`
}

type lockCheckResult struct {
	Path   string            `json:"path"`
	Claims []fileReservation `json:"claims"`
}

func resolveAgentMailConfig() (agentMailConfig, error) {
	cfg := agentMailConfigFromEnv()

	project := strings.TrimSpace(cfg.Project)
	if project == "" {
		cwd, err := os.Getwd()
		if err == nil {
			project = getGitRoot(cwd)
		}
	}
	if project == "" {
		return agentMailConfig{}, errors.New("agent mail project not configured (set SWARM_AGENT_MAIL_PROJECT or run inside a git repo)")
	}

	urlValue := strings.TrimSpace(cfg.URL)
	if urlValue == "" {
		urlValue = defaultAgentMailURL
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultAgentMailTimeout
	}

	return agentMailConfig{
		URL:     urlValue,
		Project: project,
		Agent:   strings.TrimSpace(cfg.Agent),
		Timeout: timeout,
	}, nil
}

func resolveAgentMailAgent(flagValue, fallback string) (string, error) {
	value := strings.TrimSpace(flagValue)
	if value != "" {
		return value, nil
	}
	value = strings.TrimSpace(fallback)
	if value != "" {
		return value, nil
	}
	return "", errors.New("agent name is required (use --agent or set SWARM_AGENT_MAIL_AGENT)")
}

func newAgentMailClient(url string, timeout time.Duration) *agentMailClient {
	if strings.TrimSpace(url) == "" {
		url = defaultAgentMailURL
	}
	if timeout <= 0 {
		timeout = defaultAgentMailTimeout
	}
	return &agentMailClient{
		url: url,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *agentMailClient) readResource(ctx context.Context, uri string) ([]byte, error) {
	request := mcpResourceRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("cli-lock-%d", time.Now().UnixNano()),
		Method:  "resources/read",
		Params: mcpResourceParams{
			URI: uri,
		},
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode mcp request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build mcp request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call mcp server: %w", err)
	}
	defer resp.Body.Close()

	var response mcpResourceResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode mcp response: %w", err)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", response.Error.Code, response.Error.Message)
	}
	if len(response.Result.Contents) == 0 {
		return nil, errors.New("empty mcp resource response")
	}
	content := response.Result.Contents[0]
	if strings.TrimSpace(content.Text) == "" {
		return nil, errors.New("empty mcp resource content")
	}
	return []byte(content.Text), nil
}

func (c *agentMailClient) callTool(ctx context.Context, name string, args interface{}, out interface{}) error {
	request := mcpToolRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("cli-lock-%d", time.Now().UnixNano()),
		Method:  "tools/call",
		Params: mcpToolParams{
			Name:      name,
			Arguments: args,
		},
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode mcp request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build mcp request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call mcp server: %w", err)
	}
	defer resp.Body.Close()

	var response mcpToolResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("decode mcp response: %w", err)
	}
	if response.Error != nil {
		return fmt.Errorf("mcp error %d: %s", response.Error.Code, response.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(response.Result) == 0 {
		return errors.New("empty mcp tool response")
	}
	if err := json.Unmarshal(response.Result, out); err != nil {
		return fmt.Errorf("parse mcp tool response: %w", err)
	}
	return nil
}

func claimLocks(ctx context.Context, client *agentMailClient, project, agentName string, paths []string, ttlSeconds int, exclusive bool, reason string) (*lockClaimResponse, error) {
	args := map[string]interface{}{
		"project_key": project,
		"agent_name":  agentName,
		"paths":       paths,
		"ttl_seconds": ttlSeconds,
		"exclusive":   exclusive,
	}
	if strings.TrimSpace(reason) != "" {
		args["reason"] = reason
	}

	var result lockClaimResponse
	if err := client.callTool(ctx, "file_reservation_paths", args, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func forceReleaseConflicts(ctx context.Context, client *agentMailClient, project, agentName string, conflicts []fileReservationConflict, reason string) error {
	ids := make([]int64, 0)
	for _, conflict := range conflicts {
		for _, holder := range conflict.Holders {
			if holder.ID == 0 {
				continue
			}
			ids = append(ids, holder.ID)
		}
	}

	if len(ids) == 0 {
		return errors.New("no lock IDs available for force release")
	}

	for _, id := range ids {
		args := map[string]interface{}{
			"project_key":         project,
			"agent_name":          agentName,
			"file_reservation_id": id,
			"notify_previous":     true,
		}
		if strings.TrimSpace(reason) != "" {
			args["note"] = reason
		}
		if err := client.callTool(ctx, "force_release_file_reservation", args, nil); err != nil {
			return err
		}
	}

	return nil
}

func releaseLocks(ctx context.Context, client *agentMailClient, project, agentName string, paths []string, lockIDs []int) (*lockReleaseResponse, error) {
	args := map[string]interface{}{
		"project_key": project,
		"agent_name":  agentName,
	}
	if len(paths) > 0 {
		args["paths"] = paths
	}
	if len(lockIDs) > 0 {
		args["file_reservation_ids"] = lockIDs
	}

	var result lockReleaseResponse
	if err := client.callTool(ctx, "release_file_reservations", args, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func listFileReservations(ctx context.Context, client *agentMailClient, project string, activeOnly bool) ([]fileReservation, error) {
	uri := fileReservationsURI(project, activeOnly)
	data, err := client.readResource(ctx, uri)
	if err != nil {
		return nil, err
	}

	var claims []fileReservation
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, fmt.Errorf("parse agent mail locks: %w", err)
	}

	return claims, nil
}

func fileReservationsURI(project string, activeOnly bool) string {
	return fmt.Sprintf(
		"resource://file_reservations/%s?active_only=%t",
		url.PathEscape(strings.TrimSpace(project)),
		activeOnly,
	)
}

func filterFileReservations(claims []fileReservation, agentFilter string, paths []string) []fileReservation {
	if agentFilter == "" && len(paths) == 0 {
		return claims
	}

	filtered := make([]fileReservation, 0, len(claims))
	for _, claim := range claims {
		if agentFilter != "" && !strings.EqualFold(claim.Agent, agentFilter) {
			continue
		}
		if len(paths) > 0 {
			matched := false
			for _, pathValue := range paths {
				if matchesPathPattern(pathValue, claim.PathPattern) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		filtered = append(filtered, claim)
	}

	return filtered
}

func buildCheckResults(paths []string, claims []fileReservation) []lockCheckResult {
	results := make([]lockCheckResult, 0, len(paths))
	for _, pathValue := range paths {
		result := lockCheckResult{Path: pathValue}
		for _, claim := range claims {
			if matchesPathPattern(pathValue, claim.PathPattern) {
				result.Claims = append(result.Claims, claim)
			}
		}
		results = append(results, result)
	}
	return results
}

func matchesPathPattern(pathValue, pattern string) bool {
	pathValue = normalizeLockPath(pathValue)
	pattern = normalizeLockPath(pattern)
	if pathValue == "" || pattern == "" {
		return false
	}
	if pathValue == pattern {
		return true
	}
	if ok, _ := path.Match(pattern, pathValue); ok {
		return true
	}
	if ok, _ := path.Match(pathValue, pattern); ok {
		return true
	}
	return false
}

func normalizeLockPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "/")
	return value
}

func parseLockIDs(values []string) ([]int, error) {
	if len(values) == 0 {
		return nil, nil
	}

	ids := make([]int, 0, len(values))
	for _, value := range values {
		for _, part := range splitCommaList(value) {
			if part == "" {
				continue
			}
			id, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid lock id %q", part)
			}
			ids = append(ids, id)
		}
	}

	return ids, nil
}

func printLockConflicts(conflicts []fileReservationConflict) {
	if len(conflicts) == 0 {
		return
	}
	fmt.Println("Conflicts detected:")
	for _, conflict := range conflicts {
		fmt.Printf("  Path: %s\n", conflict.Path)
		for _, holder := range conflict.Holders {
			expires, _ := parseAgentMailTime(holder.ExpiresTS)
			fmt.Printf("    - %s (id %d, expires %s)\n", holder.Agent, holder.ID, formatTimeUntil(expires))
		}
	}
}

func formatTimeUntil(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	remaining := time.Until(t)
	if remaining < 0 {
		return "expired"
	}
	if remaining < time.Minute {
		return "in <1m"
	}
	if remaining < time.Hour {
		return fmt.Sprintf("in %dm", int(remaining.Minutes()))
	}
	if remaining < 24*time.Hour {
		return fmt.Sprintf("in %dh", int(remaining.Hours()))
	}
	return fmt.Sprintf("in %dd", int(remaining.Hours()/24))
}

func parseAgentMailTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}
