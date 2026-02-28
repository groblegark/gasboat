package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
