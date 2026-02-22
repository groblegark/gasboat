package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"log/slog"
)

func TestSlackNotifier_HandleInteraction_ClosesDecision(t *testing.T) {
	daemon := newMockDaemon()
	slack := NewSlackNotifier("xoxb-test", "", "C123", daemon, slog.Default())

	handler := slack.Handler()

	// Simulate a Slack block_actions interaction.
	interaction := slackInteraction{
		Type: "block_actions",
	}
	interaction.Channel.ID = "C123"
	interaction.Message.TS = "1234567890.123456"
	interaction.User.ID = "U123"
	interaction.User.Username = "testuser"
	interaction.Actions = []struct {
		ActionID string `json:"action_id"`
		BlockID  string `json:"block_id"`
		Value    string `json:"value"`
	}{
		{
			ActionID: "decision_dec-42_0",
			BlockID:  "decision_dec-42",
			Value:    "option-a",
		},
	}

	payloadJSON, _ := json.Marshal(interaction)
	form := url.Values{"payload": {string(payloadJSON)}}

	req := httptest.NewRequest(http.MethodPost, "/slack/interactions",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].BeadID != "dec-42" {
		t.Errorf("expected bead dec-42 to be closed, got %q", closed[0].BeadID)
	}
	if closed[0].Fields["chosen"] != "option-a" {
		t.Errorf("expected chosen=option-a, got %q", closed[0].Fields["chosen"])
	}
	if !strings.Contains(closed[0].Fields["rationale"], "testuser") {
		t.Errorf("expected rationale to mention testuser, got %q", closed[0].Fields["rationale"])
	}
}

func TestSlackNotifier_NotifyDecision_PostsMessage(t *testing.T) {
	daemon := newMockDaemon()
	slack := NewSlackNotifier("xoxb-test", "", "C123", daemon, slog.Default())

	// Verify message tracking.
	slack.mu.Lock()
	slack.messages["dec-99"] = "1111111.222222"
	slack.mu.Unlock()

	slack.mu.Lock()
	ts, ok := slack.messages["dec-99"]
	slack.mu.Unlock()
	if !ok || ts != "1111111.222222" {
		t.Errorf("expected message ts tracked, got ok=%v ts=%q", ok, ts)
	}
}

func TestSlackNotifier_VerifySignature(t *testing.T) {
	slack := NewSlackNotifier("xoxb-test", "test-secret", "C123", nil, slog.Default())

	handler := slack.Handler()

	// Request without signature should fail.
	req := httptest.NewRequest(http.MethodPost, "/slack/interactions",
		strings.NewReader("payload={}"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing signature, got %d", rec.Code)
	}
}

func TestSlackNotifier_HandleInteraction_IgnoresNonDecision(t *testing.T) {
	daemon := newMockDaemon()
	slack := NewSlackNotifier("xoxb-test", "", "C123", daemon, slog.Default())

	handler := slack.Handler()

	// Interaction with non-decision block_id should be ignored.
	interaction := slackInteraction{
		Type: "block_actions",
	}
	interaction.Actions = []struct {
		ActionID string `json:"action_id"`
		BlockID  string `json:"block_id"`
		Value    string `json:"value"`
	}{
		{
			ActionID: "other_action",
			BlockID:  "other_block",
			Value:    "whatever",
		},
	}

	payloadJSON, _ := json.Marshal(interaction)
	form := url.Values{"payload": {string(payloadJSON)}}

	req := httptest.NewRequest(http.MethodPost, "/slack/interactions",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// No daemon calls should have been made — pass if no panic.
}

// Ensure SlackNotifier implements Notifier.
var _ Notifier = (*SlackNotifier)(nil)

// Ensure Notifier can be used in context.
func TestSlackNotifierImplementsNotifier(t *testing.T) {
	var n Notifier = NewSlackNotifier("token", "secret", "ch", nil, slog.Default())
	_ = n.NotifyDecision(context.Background(), BeadEvent{})
}
