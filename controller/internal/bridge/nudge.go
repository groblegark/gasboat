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

// nudgeCoopResult holds the parsed coop nudge response body.
type nudgeCoopResult struct {
	Delivered bool   `json:"delivered"`
	Reason    string `json:"reason"`
}

// nudgeRetryConfig controls retry behavior for busy-agent nudges.
var nudgeRetryConfig = struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}{
	maxAttempts: 3,
	baseDelay:   2 * time.Second,
	maxDelay:    8 * time.Second,
}

// NudgeAgent looks up an agent's coop_url from its agent bead and delivers
// a nudge message with retry. This is the single entry point for all
// agent nudge delivery in the bridge package.
func NudgeAgent(ctx context.Context, daemon BeadClient, client *http.Client, logger *slog.Logger, agentName, message string) error {
	if agentName == "" {
		return fmt.Errorf("empty agent name")
	}

	agentBead, err := daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		return fmt.Errorf("find agent bead %q: %w", agentName, err)
	}

	coopURL := beadsapi.ParseNotes(agentBead.Notes)["coop_url"]
	if coopURL == "" {
		return fmt.Errorf("agent %q has no coop_url", agentName)
	}

	if err := nudgeCoop(ctx, client, coopURL, message); err != nil {
		logger.Warn("nudge failed", "agent", agentName, "error", err)
		return err
	}

	logger.Debug("nudge delivered", "agent", agentName)
	return nil
}

// nudgeCoop POSTs a nudge message to a coop agent endpoint with retry.
// When the agent explicitly reports busy ({"delivered":false,"reason":"..."}),
// it retries with exponential backoff up to nudgeRetryConfig.maxAttempts times.
// If the response body is empty or unparseable, the nudge is treated as
// delivered (backwards-compatible with older coop versions).
func nudgeCoop(ctx context.Context, client *http.Client, coopURL, message string) error {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return fmt.Errorf("marshal nudge body: %w", err)
	}

	url := coopURL + "/api/v1/agent/nudge"

	var lastErr error
	delay := nudgeRetryConfig.baseDelay

	for attempt := 1; attempt <= nudgeRetryConfig.maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create nudge request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("nudge request failed: %w", err)
		}

		var result nudgeCoopResult
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("nudge returned status %d", resp.StatusCode)
		}

		// If body was empty or unparseable, treat as delivered (backwards-compat).
		if decodeErr != nil {
			return nil
		}

		if result.Delivered {
			return nil
		}

		// Explicit busy response with a reason — retry with backoff.
		lastErr = fmt.Errorf("nudge not delivered: %s", result.Reason)
		if attempt < nudgeRetryConfig.maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
			if delay > nudgeRetryConfig.maxDelay {
				delay = nudgeRetryConfig.maxDelay
			}
		}
	}

	return lastErr
}
