package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// mockNudgeDaemon implements BeadClient for nudger tests.
type mockNudgeDaemon struct {
	mockDaemon
	agentNotes string // raw notes string to return
}

func (m *mockNudgeDaemon) FindAgentBead(_ context.Context, name string) (*beadsapi.BeadDetail, error) {
	return &beadsapi.BeadDetail{
		ID:    "agent-" + name,
		Notes: m.agentNotes,
	}, nil
}

func TestNudger_NudgeAgent_Success(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		received = body["message"]
		json.NewEncoder(w).Encode(map[string]any{"delivered": true})
	}))
	defer srv.Close()

	daemon := &mockNudgeDaemon{agentNotes: "coop_url:" + srv.URL}
	n := NewNudger(NudgerConfig{
		Daemon: daemon,
		Logger: slog.Default(),
	})

	err := n.NudgeAgent(context.Background(), "test-agent", "hello")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if received != "hello" {
		t.Errorf("expected message 'hello', got %q", received)
	}
}

func TestNudger_NudgeAgent_NoCoopURL(t *testing.T) {
	daemon := &mockNudgeDaemon{agentNotes: ""} // no coop_url
	n := NewNudger(NudgerConfig{
		Daemon: daemon,
		Logger: slog.Default(),
	})

	err := n.NudgeAgent(context.Background(), "test-agent", "hello")
	if err != nil {
		t.Fatalf("expected nil error for missing coop_url, got %v", err)
	}
}

func TestNudger_NudgeAgent_EmptyName(t *testing.T) {
	n := NewNudger(NudgerConfig{
		Daemon: &mockNudgeDaemon{},
		Logger: slog.Default(),
	})

	err := n.NudgeAgent(context.Background(), "", "hello")
	if err != nil {
		t.Fatalf("expected nil error for empty agent name, got %v", err)
	}
}

func TestNudger_RetryOnBusy(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			// First 2 calls: agent busy
			json.NewEncoder(w).Encode(map[string]any{
				"delivered": false,
				"reason":    "agent is busy",
			})
			return
		}
		// Third call: success
		json.NewEncoder(w).Encode(map[string]any{"delivered": true})
	}))
	defer srv.Close()

	daemon := &mockNudgeDaemon{agentNotes: "coop_url:" + srv.URL}
	n := NewNudger(NudgerConfig{
		Daemon:     daemon,
		Logger:     slog.Default(),
		MaxRetries: 2,
		RetryDelay: 10 * time.Millisecond, // fast for testing
	})

	err := n.NudgeAgent(context.Background(), "test-agent", "wake up")
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 retries), got %d", calls.Load())
	}
}

func TestNudger_RetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"delivered": false,
			"reason":    "agent is busy",
		})
	}))
	defer srv.Close()

	daemon := &mockNudgeDaemon{agentNotes: "coop_url:" + srv.URL}
	n := NewNudger(NudgerConfig{
		Daemon:     daemon,
		Logger:     slog.Default(),
		MaxRetries: 1,
		RetryDelay: 10 * time.Millisecond,
	})

	err := n.NudgeAgent(context.Background(), "test-agent", "hello")
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
}

func TestNudger_RetryOnHTTPError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"delivered": true})
	}))
	defer srv.Close()

	daemon := &mockNudgeDaemon{agentNotes: "coop_url:" + srv.URL}
	n := NewNudger(NudgerConfig{
		Daemon:     daemon,
		Logger:     slog.Default(),
		MaxRetries: 2,
		RetryDelay: 10 * time.Millisecond,
	})

	err := n.NudgeAgent(context.Background(), "test-agent", "hello")
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls (1 initial + 1 retry), got %d", calls.Load())
	}
}
