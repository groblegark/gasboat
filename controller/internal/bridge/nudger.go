package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// NudgerConfig holds configuration for a Nudger.
type NudgerConfig struct {
	Daemon     BeadClient
	Logger     *slog.Logger
	MaxRetries int           // max retry attempts (default 2)
	RetryDelay time.Duration // initial delay between retries (default 1s, doubles each attempt)
}

// Nudger delivers nudge messages to agents via the Coop HTTP API.
// It centralises coop_url lookup, HTTP client management, and retry logic
// that was previously duplicated across Decisions, Mail, Claimed, Chat,
// and Bot (mentions).
type Nudger struct {
	daemon     BeadClient
	logger     *slog.Logger
	httpClient *http.Client
	maxRetries int
	retryDelay time.Duration
}

// NewNudger creates a Nudger with the given config.
func NewNudger(cfg NudgerConfig) *Nudger {
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	retryDelay := cfg.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 1 * time.Second
	}
	return &Nudger{
		daemon:     cfg.Daemon,
		logger:     cfg.Logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		maxRetries: maxRetries,
		retryDelay: retryDelay,
	}
}

// NudgeAgent resolves the agent's coop_url and delivers a nudge message.
// It retries on transient failures (connection errors, agent busy) with
// exponential backoff. Returns nil on success or if the agent has no coop_url.
func (n *Nudger) NudgeAgent(ctx context.Context, agentName, message string) error {
	if agentName == "" {
		return nil
	}

	agentBead, err := n.daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		return fmt.Errorf("find agent bead for %s: %w", agentName, err)
	}

	coopURL := beadsapi.ParseNotes(agentBead.Notes)["coop_url"]
	if coopURL == "" {
		n.logger.Debug("agent has no coop_url, skipping nudge",
			"agent", agentName)
		return nil
	}

	return n.nudgeWithRetry(ctx, coopURL, message, agentName)
}

// nudgeWithRetry sends a nudge to the given coop_url, retrying on transient
// failures with exponential backoff.
func (n *Nudger) nudgeWithRetry(ctx context.Context, coopURL, message, agentName string) error {
	delay := n.retryDelay

	for attempt := 0; attempt <= n.maxRetries; attempt++ {
		err := n.nudgeOnce(ctx, coopURL, message)
		if err == nil {
			if attempt > 0 {
				n.logger.Info("nudge delivered after retry",
					"agent", agentName, "attempt", attempt+1)
			}
			return nil
		}

		if attempt < n.maxRetries {
			n.logger.Debug("nudge failed, retrying",
				"agent", agentName, "attempt", attempt+1, "delay", delay, "error", err)
			select {
			case <-time.After(delay):
				delay *= 2
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during nudge retry: %w", ctx.Err())
			}
		} else {
			return fmt.Errorf("nudge failed after %d attempts: %w", attempt+1, err)
		}
	}
	return nil // unreachable
}

// nudgeOnce performs a single nudge HTTP request.
func (n *Nudger) nudgeOnce(ctx context.Context, coopURL, message string) error {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return fmt.Errorf("marshal nudge body: %w", err)
	}

	url := coopURL + "/api/v1/agent/nudge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create nudge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("nudge request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nudge returned status %d", resp.StatusCode)
	}

	var result struct {
		Delivered bool   `json:"delivered"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && !result.Delivered {
		return fmt.Errorf("nudge not delivered: %s", result.Reason)
	}
	return nil
}
