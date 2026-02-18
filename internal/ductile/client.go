package ductile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// TriggerResponse is the Ductile API response for POST /plugin/{plugin}/{command}.
type TriggerResponse struct {
	JobID   string `json:"job_id"`
	Status  string `json:"status"`
	Plugin  string `json:"plugin"`
	Command string `json:"command"`
}

// JobStatusResponse is the Ductile API response for GET /job/{jobID}.
type JobStatusResponse struct {
	JobID       string          `json:"job_id"`
	Status      string          `json:"status"`
	Plugin      string          `json:"plugin"`
	Command     string          `json:"command"`
	Result      json.RawMessage `json:"result,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

// Client is an HTTP client for the Ductile gateway API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewClient creates a new Ductile API client.
func NewClient(baseURL, token string, logger *slog.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// Trigger sends POST /plugin/{plugin}/{command} and returns the job ID.
func (c *Client) Trigger(ctx context.Context, plugin, command string, payload json.RawMessage) (string, error) {
	url := fmt.Sprintf("%s/plugin/%s/%s", c.baseURL, plugin, command)

	body := "{}"
	if len(payload) > 0 {
		body = fmt.Sprintf(`{"payload":%s}`, string(payload))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("trigger %s/%s: %w", plugin, command, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("trigger %s/%s: status %d: %s", plugin, command, resp.StatusCode, string(respBody))
	}

	var triggerResp TriggerResponse
	if err := json.Unmarshal(respBody, &triggerResp); err != nil {
		return "", fmt.Errorf("parse trigger response: %w", err)
	}

	return triggerResp.JobID, nil
}

// PollJob polls GET /job/{jobID} until the job completes or the context is cancelled.
// Uses exponential backoff starting at pollInterval, capped at 30s, with a maximum of 60 attempts.
func (c *Client) PollJob(ctx context.Context, jobID string, pollInterval time.Duration) (*JobStatusResponse, error) {
	const maxAttempts = 60
	const maxBackoff = 30 * time.Second
	interval := pollInterval

	for attempt := 0; attempt < maxAttempts; attempt++ {
		status, err := c.GetJob(ctx, jobID)
		if err != nil {
			return nil, err
		}

		switch status.Status {
		case "succeeded", "failed", "timed_out", "dead":
			return status, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		interval = interval * 2
		if interval > maxBackoff {
			interval = maxBackoff
		}
	}

	return nil, fmt.Errorf("poll job %s: max attempts (%d) exhausted", jobID, maxAttempts)
}

// Callback sends a completion notification to a Ductile webhook endpoint.
func (c *Client) Callback(ctx context.Context, callbackURL string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal callback payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("callback: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("callback: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// PluginDetailResponse holds the discovery response for a plugin.
type PluginDetailResponse struct {
	Name     string        `json:"name"`
	Commands []PluginCommand `json:"commands"`
}

// PluginCommand holds metadata and schemas for a single plugin command.
type PluginCommand struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// GetPluginDetail fetches command metadata from GET /plugin/{name}.
func (c *Client) GetPluginDetail(ctx context.Context, plugin string) (*PluginDetailResponse, error) {
	url := fmt.Sprintf("%s/plugin/%s", c.baseURL, plugin)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get plugin %s: %w", plugin, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get plugin %s: status %d: %s", plugin, resp.StatusCode, string(respBody))
	}

	var detail PluginDetailResponse
	if err := json.Unmarshal(respBody, &detail); err != nil {
		return nil, fmt.Errorf("parse plugin detail: %w", err)
	}
	return &detail, nil
}

// GetJob retrieves the status of a job.
func (c *Client) GetJob(ctx context.Context, jobID string) (*JobStatusResponse, error) {
	url := fmt.Sprintf("%s/job/%s", c.baseURL, jobID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get job %s: %w", jobID, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get job %s: status %d: %s", jobID, resp.StatusCode, string(respBody))
	}

	var jobResp JobStatusResponse
	if err := json.Unmarshal(respBody, &jobResp); err != nil {
		return nil, fmt.Errorf("parse job response: %w", err)
	}

	return &jobResp, nil
}
