package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultOpenCodeTimeout = 5 * time.Second

// OpenCodeClient handles OpenCode HTTP API calls.
type OpenCodeClient struct {
	BaseURL string
	Client  *http.Client
}

// SessionStatus captures the raw status response from OpenCode.
type SessionStatus struct {
	Raw  json.RawMessage
	Data map[string]any
}

// NewOpenCodeClient constructs a client with defaults applied.
func NewOpenCodeClient(baseURL string) *OpenCodeClient {
	return &OpenCodeClient{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Client: &http.Client{Timeout: defaultOpenCodeTimeout},
	}
}

// SendMessage appends and submits prompt text through the OpenCode TUI API.
func (c *OpenCodeClient) SendMessage(ctx context.Context, text string) error {
	baseURL, err := c.baseURL()
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := c.postJSON(ctx, baseURL+"/tui/append-prompt", openCodePromptPayload{Text: text}); err != nil {
		return fmt.Errorf("append prompt: %w", err)
	}
	if err := c.postJSON(ctx, baseURL+"/tui/submit-prompt", openCodePromptPayload{}); err != nil {
		return fmt.Errorf("submit prompt: %w", err)
	}
	return nil
}

// GetStatus fetches session status from the OpenCode server.
func (c *OpenCodeClient) GetStatus(ctx context.Context) (*SessionStatus, error) {
	baseURL, err := c.baseURL()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/session", nil)
	if err != nil {
		return nil, fmt.Errorf("build status request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("call opencode status: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return nil, err
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}

	return &SessionStatus{
		Raw:  json.RawMessage(body),
		Data: data,
	}, nil
}

type openCodePromptPayload struct {
	Text string `json:"text"`
}

func (c *OpenCodeClient) baseURL() (string, error) {
	if c == nil {
		return "", errors.New("opencode client is nil")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if baseURL == "" {
		return "", errors.New("opencode base URL is empty")
	}
	return baseURL, nil
}

func (c *OpenCodeClient) httpClient() *http.Client {
	if c.Client == nil {
		c.Client = &http.Client{Timeout: defaultOpenCodeTimeout}
	}
	if c.Client.Timeout <= 0 {
		c.Client.Timeout = defaultOpenCodeTimeout
	}
	return c.Client
}

func (c *OpenCodeClient) postJSON(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("call opencode endpoint: %w", err)
	}
	defer resp.Body.Close()

	_, err = readResponseBody(resp)
	return err
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read opencode response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet := strings.TrimSpace(string(body))
		if snippet == "" {
			snippet = resp.Status
		}
		return nil, fmt.Errorf("opencode request failed (%s): %s", resp.Status, snippet)
	}

	return body, nil
}
