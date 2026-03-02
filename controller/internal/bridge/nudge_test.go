package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"
)

func TestNudgeCoop_Delivered(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: true})
	}))
	defer srv.Close()

	err := nudgeCoop(context.Background(), srv.Client(), srv.URL, "hello")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestNudgeCoop_EmptyBody_TreatedAsDelivered(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := nudgeCoop(context.Background(), srv.Client(), srv.URL, "hello")
	if err != nil {
		t.Fatalf("expected nil error for empty body, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry for empty body), got %d", calls)
	}
}

func TestNudgeCoop_BusyThenDelivered_Retries(t *testing.T) {
	// Speed up test by reducing retry delays.
	orig := nudgeRetryConfig
	nudgeRetryConfig.baseDelay = 10 * time.Millisecond
	nudgeRetryConfig.maxDelay = 50 * time.Millisecond
	defer func() { nudgeRetryConfig = orig }()

	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()

		if n == 1 {
			_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: false, Reason: "agent_busy"})
		} else {
			_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: true})
		}
	}))
	defer srv.Close()

	err := nudgeCoop(context.Background(), srv.Client(), srv.URL, "hello")
	if err != nil {
		t.Fatalf("expected delivery on retry, got %v", err)
	}
	mu.Lock()
	c := calls
	mu.Unlock()
	if c != 2 {
		t.Errorf("expected 2 calls (1 busy + 1 success), got %d", c)
	}
}

func TestNudgeCoop_AllBusy_ReturnsError(t *testing.T) {
	orig := nudgeRetryConfig
	nudgeRetryConfig.baseDelay = 10 * time.Millisecond
	nudgeRetryConfig.maxDelay = 50 * time.Millisecond
	defer func() { nudgeRetryConfig = orig }()

	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: false, Reason: "agent_busy"})
	}))
	defer srv.Close()

	err := nudgeCoop(context.Background(), srv.Client(), srv.URL, "hello")
	if err == nil {
		t.Fatal("expected error after all retries exhausted")
	}
	mu.Lock()
	c := calls
	mu.Unlock()
	if c != nudgeRetryConfig.maxAttempts {
		t.Errorf("expected %d retry attempts, got %d", nudgeRetryConfig.maxAttempts, c)
	}
}

func TestNudgeCoop_WorkingAgent_SendsEscapeBeforeRetry(t *testing.T) {
	orig := nudgeRetryConfig
	nudgeRetryConfig.baseDelay = 10 * time.Millisecond
	nudgeRetryConfig.maxDelay = 50 * time.Millisecond
	defer func() { nudgeRetryConfig = orig }()

	var mu sync.Mutex
	var paths []string
	nudgeCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/agent/nudge") {
			nudgeCount++
			n := nudgeCount
			mu.Unlock()
			if n == 1 {
				_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: false, Reason: "agent is working"})
			} else {
				_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: true})
			}
		} else {
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	err := nudgeCoop(context.Background(), srv.Client(), srv.URL, "hello")
	if err != nil {
		t.Fatalf("expected delivery after interrupt, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Should have: nudge (working) → keys (Escape) → nudge (delivered)
	if nudgeCount != 2 {
		t.Errorf("expected 2 nudge calls, got %d", nudgeCount)
	}

	hasKeys := false
	for _, p := range paths {
		if strings.HasSuffix(p, "/input/keys") {
			hasKeys = true
			break
		}
	}
	if !hasKeys {
		t.Errorf("expected Escape key POST to /input/keys, paths: %v", paths)
	}
}

func TestNudgeCoop_PromptAgent_SendsEscapeBeforeRetry(t *testing.T) {
	orig := nudgeRetryConfig
	nudgeRetryConfig.baseDelay = 10 * time.Millisecond
	nudgeRetryConfig.maxDelay = 50 * time.Millisecond
	defer func() { nudgeRetryConfig = orig }()

	var mu sync.Mutex
	var keysSent bool
	nudgeCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/input/keys") {
			keysSent = true
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		nudgeCount++
		n := nudgeCount
		mu.Unlock()

		if n == 1 {
			_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: false, Reason: "agent is prompt"})
		} else {
			_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: true})
		}
	}))
	defer srv.Close()

	err := nudgeCoop(context.Background(), srv.Client(), srv.URL, "hello")
	if err != nil {
		t.Fatalf("expected delivery after interrupt, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !keysSent {
		t.Error("expected Escape key to be sent for 'prompt' state")
	}
}

func TestIsWorkingReason(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		{"agent is working", true},
		{"agent is prompt", true},
		{"working", true},
		{"agent_busy", false},
		{"rate_limited", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isWorkingReason(tt.reason); got != tt.want {
			t.Errorf("isWorkingReason(%q) = %v, want %v", tt.reason, got, tt.want)
		}
	}
}

func TestNudgeAgent_LooksUpCoopURL(t *testing.T) {
	var nudged bool
	coopSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nudged = true
		_ = json.NewEncoder(w).Encode(nudgeCoopResult{Delivered: true})
	}))
	defer coopSrv.Close()

	daemon := newMockDaemon()
	daemon.beads["test-agent"] = &beadsapi.BeadDetail{
		ID:    "test-agent",
		Notes: "coop_url: " + coopSrv.URL,
	}

	err := NudgeAgent(context.Background(), daemon, coopSrv.Client(), slog.Default(), "test-agent", "test message")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !nudged {
		t.Error("expected coop to be nudged")
	}
}

func TestNudgeAgent_EmptyAgentName_ReturnsError(t *testing.T) {
	err := NudgeAgent(context.Background(), nil, nil, slog.Default(), "", "msg")
	if err == nil {
		t.Fatal("expected error for empty agent name")
	}
}

func TestNudgeAgent_NoCoopURL_ReturnsError(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["test-agent"] = &beadsapi.BeadDetail{
		ID:    "test-agent",
		Notes: "", // no coop_url
	}

	err := NudgeAgent(context.Background(), daemon, &http.Client{}, slog.Default(), "test-agent", "msg")
	if err == nil {
		t.Fatal("expected error when agent has no coop_url")
	}
}
